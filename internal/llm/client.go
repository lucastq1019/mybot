package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"mybot/internal/brain"
	"mybot/internal/config"
)

const (
	// DefaultOpenAIAPIURL OpenAI API 地址
	DefaultOpenAIAPIURL = "https://api.openai.com/v1/chat/completions"
	// DefaultModel 默认模型
	DefaultModel = "gpt-3.5-turbo"
	// DefaultMaxTokens 默认最大 token 数
	DefaultMaxTokens = 2000
	// DefaultTimeout 默认超时时间
	DefaultTimeout = 60 * time.Second
)

// Client LLM 客户端
type Client struct {
	apiKey     string
	apiURL     string
	model      string
	maxTokens  int
	timeout    time.Duration
	httpClient *http.Client
	provider   Provider
}

// Role 表示 LLM 使用角色（不同用途可绑定不同模型）
type Role string

const (
	RoleDefault   Role = "default"
	RoleChat      Role = "chat"
	RoleSummarize Role = "summarize"
	RoleIndex     Role = "index"
	RoleCompress  Role = "compress"
	RoleEvolution Role = "evolution"
)

var (
	bootLeaderOnce   sync.Once
	bootLeaderPrompt string
)

// loadBootLeaderPrompt 只读一次 brain/boot-leader.md，用作所有对话的通用系统提示词前缀。
func loadBootLeaderPrompt() string {
	bootLeaderOnce.Do(func() {
		path := brain.BootLeaderPath()
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("Warning: failed to read boot-leader.md from %s: %v", path, err)
			return
		}
		bootLeaderPrompt = string(data)
	})
	return bootLeaderPrompt
}

// withBootLeaderSystemMessage 确保每次请求的消息列表前面都有 boot-leader.md 作为系统提示词。
// 如果调用方已经自己把 boot-leader 内容放在第一个 system 中，则不会重复注入。
func withBootLeaderSystemMessage(messages []Message) []Message {
	prompt := strings.TrimSpace(loadBootLeaderPrompt())
	if prompt == "" {
		return messages
	}

	if len(messages) > 0 && messages[0].Role == "system" && strings.TrimSpace(messages[0].Content) == prompt {
		return messages
	}

	out := make([]Message, 0, len(messages)+1)
	out = append(out, Message{Role: "system", Content: prompt})
	out = append(out, messages...)
	return out
}

// resolveModelForRole 根据全局配置与角色解析应使用的模型名称。
// 优先级：cfg.Models[role] -> cfg.Models["default"] -> cfg.Model -> 由 NewClientFromConfig 内部根据环境变量与 Provider 决定。
func resolveModelForRole(cfg config.LLMConfig, role Role) string {
	if cfg.Models != nil {
		if model, ok := cfg.Models[string(role)]; ok && strings.TrimSpace(model) != "" {
			return strings.TrimSpace(model)
		}
		if model, ok := cfg.Models[string(RoleDefault)]; ok && strings.TrimSpace(model) != "" {
			return strings.TrimSpace(model)
		}
	}

	if strings.TrimSpace(cfg.Model) != "" {
		return strings.TrimSpace(cfg.Model)
	}

	// 为空时交由 NewClientFromConfig 使用环境变量与 Provider 默认值决定
	return ""
}

// NewClientForRole 使用全局配置和角色创建 LLM 客户端。
// - 当配置文件启用 LLM 时，从 config.Config.LLM 读取 Provider/APIURL/APIKey/MaxTokens/Timeout，并按角色解析模型名。
// - 当配置未启用或尚未加载时，回退到 NewClient（环境变量与默认策略）。
func NewClientForRole(role Role) (*Client, error) {
	if config.Config != nil && config.Config.LLM.Enabled {
		llmCfg := config.Config.LLM
		model := resolveModelForRole(llmCfg, role)
		return NewClientFromConfig(
			llmCfg.Provider,
			llmCfg.APIKey,
			llmCfg.APIURL,
			model,
			llmCfg.MaxTokens,
			time.Duration(llmCfg.Timeout)*time.Second,
		)
	}

	// 配置未启用或未加载，沿用现有环境变量与默认逻辑
	return NewClient()
}

// NewClient 创建新的 LLM 客户端（从环境变量或配置读取）
func NewClient() (*Client, error) {
	return NewClientFromConfig("", "", "", "", 0, 0)
}

// NewClientFromConfig 从配置创建客户端
func NewClientFromConfig(provider, apiKey, apiURL, model string, maxTokens int, timeout time.Duration) (*Client, error) {
	// 如果 provider 为空，根据环境变量自动检测
	if provider == "" {
		provider = os.Getenv("LLM_PROVIDER")
		if provider == "" {
			// 根据可用的 API Key 自动检测提供商
			if os.Getenv("DASHSCOPE_API_KEY") != "" {
				provider = "qwen"
			} else if os.Getenv("ANTHROPIC_API_KEY") != "" {
				provider = "claude"
			} else if os.Getenv("OPENAI_API_KEY") != "" {
				provider = "openai"
			} else {
				provider = "openai" // 默认提供商
			}
		}
	}

	// 如果 apiKey 为空，根据 provider 从环境变量读取
	if apiKey == "" {
		switch provider {
		case "qwen", "tongyi", "dashscope":
			apiKey = os.Getenv("DASHSCOPE_API_KEY")
		case "claude", "anthropic":
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		case "openai", "":
			apiKey = os.Getenv("OPENAI_API_KEY")
		}
		
		if apiKey == "" {
			return nil, fmt.Errorf("LLM API key not set (set %s_API_KEY or configure in config file)", strings.ToUpper(provider))
		}
	}

	if apiURL == "" {
		// 优先使用通用环境变量
		apiURL = os.Getenv("LLM_API_URL")
		if apiURL == "" {
			// 根据 provider 设置默认 URL
			switch provider {
			case "qwen", "tongyi", "dashscope":
				// 使用 OpenAI 兼容模式（推荐）
				apiURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
			case "claude", "anthropic":
				apiURL = "https://api.anthropic.com/v1/messages"
			default:
				apiURL = os.Getenv("OPENAI_API_URL")
				if apiURL == "" {
					apiURL = DefaultOpenAIAPIURL
				}
			}
		}
	}

	if model == "" {
		// 优先使用通用环境变量
		model = os.Getenv("LLM_MODEL")
		if model == "" {
			// 根据 provider 设置默认模型
			switch provider {
			case "qwen", "tongyi", "dashscope":
				model = "Qwen-Omni" // 千问默认模型
			case "claude", "anthropic":
				model = "claude-3-sonnet-20240229"
			default:
				model = os.Getenv("OPENAI_MODEL")
				if model == "" {
					model = DefaultModel
				}
			}
		}
	}

	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	// 配置 HTTP 客户端，支持 HTTP/HTTPS 代理
	httpClient := &http.Client{
		Timeout: timeout,
	}
	
	// 检查环境变量中的代理设置（注意：SOCKS5 代理需要额外配置）
	proxyURL := ""
	if proxyURL = os.Getenv("HTTP_PROXY"); proxyURL == "" {
		if proxyURL = os.Getenv("HTTPS_PROXY"); proxyURL == "" {
			proxyURL = os.Getenv("ALL_PROXY")
		}
	}
	
	if proxyURL != "" {
		parsedProxy, err := url.Parse(proxyURL)
		if err == nil {
			// 只支持 HTTP/HTTPS 代理，SOCKS5 需要额外处理
			if parsedProxy.Scheme == "http" || parsedProxy.Scheme == "https" {
				httpClient.Transport = &http.Transport{
					Proxy: http.ProxyURL(parsedProxy),
				}
				log.Printf("Using HTTP proxy: %s", proxyURL)
			} else if parsedProxy.Scheme == "socks5" {
				// SOCKS5 代理需要 golang.org/x/net/proxy 包支持
				// 如果检测到 SOCKS5，记录警告但不阻止运行
				log.Printf("WARNING: SOCKS5 proxy detected (%s) but not fully supported. If you see EOF errors, try: 1) Start your proxy server, 2) Use HTTP proxy instead, or 3) Unset proxy env vars", proxyURL)
			}
		}
	}

	// 记录客户端创建信息（用于排查问题）
	log.Printf("Creating LLM Client: Provider=%s, URL=%s, Model=%s, APIKey present=%v", 
		provider, apiURL, model, apiKey != "")

	return &Client{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		maxTokens:  maxTokens,
		timeout:    timeout,
		provider:   GetProvider(provider),
		httpClient: httpClient,
	}, nil
}

// NewClientWithConfig 使用自定义配置创建客户端
func NewClientWithConfig(apiKey, apiURL, model string, maxTokens int, timeout time.Duration) *Client {
	return NewClientWithProvider(apiKey, apiURL, model, "openai", maxTokens, timeout)
}

// NewClientWithProvider 使用提供商创建客户端
func NewClientWithProvider(apiKey, apiURL, model, provider string, maxTokens int, timeout time.Duration) *Client {
	if apiURL == "" {
		apiURL = DefaultOpenAIAPIURL
	}
	if model == "" {
		model = DefaultModel
	}
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		maxTokens:  maxTokens,
		timeout:    timeout,
		provider:   GetProvider(provider),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Message 消息结构
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 聊天请求
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	MaxTokens int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	// Tools / ToolChoice 用于 OpenAI 风格的 tool calling（可选）
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice interface{} `json:"tool_choice,omitempty"`
}

// ChatResponse 聊天响应
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// Summarize 生成摘要
func (c *Client) Summarize(content string, instructions string) (string, error) {
	systemPrompt := "你是一个专业的文本摘要助手。请根据用户提供的内容生成简洁、准确的摘要。"
	if instructions != "" {
		systemPrompt = instructions
	}

	userPrompt := fmt.Sprintf("请为以下内容生成摘要：\n\n%s", content)

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	req := ChatRequest{
		Model:    c.model,
		Messages: messages,
		MaxTokens: c.maxTokens,
		Temperature: 0.3, // 较低温度以获得更一致的摘要
	}

	resp, _, err := c.chat(req, nil, "")
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}

	if resp.Error != nil {
		return "", fmt.Errorf("API error: %s (type: %s, code: %s)", 
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// PreprocessQuery 预处理查询：将自然语言转换为检索关键词和类别
type QueryPreprocessResult struct {
	Keywords []string `json:"keywords"` // 检索关键词列表
	Category string   `json:"category"` // 类别：preference、fact、logic
	Domain   string   `json:"domain"`   // 领域：dev、learning、life
	Intent   string   `json:"intent"`  // 检索意图描述
}

// PreprocessQuery 预处理查询：自然语言 → 检索意图 + 关键词 + category
func (c *Client) PreprocessQuery(query string) (*QueryPreprocessResult, error) {
	systemPrompt := `你是一个查询预处理助手。请分析用户的自然语言查询，提取检索关键词、类别和领域。

类别（category）：
- preference: 偏好、习惯、目标、身份认同相关
- fact: 事实、事件、记录相关
- logic: 逻辑、推理、设计、架构相关

领域（domain）：
- dev: 开发、技术、项目相关
- learning: 学习、笔记、知识相关
- life: 生活、健康、习惯相关

请以 JSON 格式返回结果，包含 keywords（关键词数组）、category、domain 和 intent（检索意图描述）。`

	userPrompt := fmt.Sprintf("请分析以下查询并提取检索信息：\n\n%s", query)

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	req := ChatRequest{
		Model:    c.model,
		Messages: messages,
		MaxTokens: 500,
		Temperature: 0.3,
	}

	resp, _, err := c.chat(req, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to preprocess query: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("API error: %s (type: %s, code: %s)", 
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// 解析 JSON 响应
	result := &QueryPreprocessResult{}
	responseText := resp.Choices[0].Message.Content

	// 尝试提取 JSON（可能包含 markdown 代码块）
	jsonStart := strings.Index(responseText, "{")
	jsonEnd := strings.LastIndex(responseText, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		responseText = responseText[jsonStart : jsonEnd+1]
	}

	if err := json.Unmarshal([]byte(responseText), result); err != nil {
		// 如果解析失败，尝试简单提取关键词
		return c.fallbackPreprocess(query), nil
	}

	// 验证和清理结果
	if len(result.Keywords) == 0 {
		// 如果没有关键词，使用原始查询
		result.Keywords = []string{query}
	}
	if result.Category == "" {
		result.Category = "fact" // 默认类别
	}
	if result.Domain == "" {
		result.Domain = "" // 空字符串表示不过滤
	}

	return result, nil
}

// fallbackPreprocess 简单的回退预处理（当 LLM 不可用或解析失败时）
func (c *Client) fallbackPreprocess(query string) *QueryPreprocessResult {
	keywords := strings.Fields(query)
	category := "fact"
	domain := ""

	queryLower := strings.ToLower(query)
	
	// 简单的类别判断
	if strings.Contains(queryLower, "偏好") || strings.Contains(queryLower, "习惯") || 
	   strings.Contains(queryLower, "目标") || strings.Contains(queryLower, "身份") {
		category = "preference"
	} else if strings.Contains(queryLower, "项目") || strings.Contains(queryLower, "设计") || 
	          strings.Contains(queryLower, "架构") || strings.Contains(queryLower, "逻辑") {
		category = "logic"
	}

	// 简单的领域判断
	if strings.Contains(queryLower, "开发") || strings.Contains(queryLower, "项目") || 
	   strings.Contains(queryLower, "技术") || strings.Contains(queryLower, "代码") {
		domain = "dev"
	} else if strings.Contains(queryLower, "学习") || strings.Contains(queryLower, "笔记") || 
	          strings.Contains(queryLower, "知识") {
		domain = "learning"
	} else if strings.Contains(queryLower, "生活") || strings.Contains(queryLower, "健康") || 
	          strings.Contains(queryLower, "习惯") {
		domain = "life"
	}

	return &QueryPreprocessResult{
		Keywords: keywords,
		Category: category,
		Domain:   domain,
		Intent:   fmt.Sprintf("检索与 '%s' 相关的内容", query),
	}
}

// Chat 发送聊天请求
func (c *Client) Chat(messages []Message) (string, error) {
	req := ChatRequest{
		Model:      c.model,
		Messages:   messages,
		MaxTokens:  c.maxTokens,
		Temperature: 0.7,
	}

	resp, _, err := c.chat(req, nil, "")
	if err != nil {
		return "", fmt.Errorf("failed to chat: %w", err)
	}

	if resp.Error != nil {
		return "", fmt.Errorf("API error: %s (type: %s, code: %s)", 
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// ChatWithTools 调用带有 tools 列表的对话接口，返回助手回复和工具调用列表。
// maxTokens / temperature 传 0 则回退到客户端默认值。
func (c *Client) ChatWithTools(messages []Message, tools []Tool, toolChoice string, maxTokens int, temperature float64) (string, []ToolCall, error) {
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}
	if temperature == 0 {
		temperature = 0.7
	}

	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	resp, toolCalls, err := c.chat(req, tools, toolChoice)
	if err != nil {
		return "", nil, fmt.Errorf("failed to chat with tools: %w", err)
	}

	if resp.Error != nil {
		return "", nil, fmt.Errorf("API error: %s (type: %s, code: %s)", 
			resp.Error.Message, resp.Error.Type, resp.Error.Code)
	}

	if len(resp.Choices) == 0 {
		return "", nil, fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, toolCalls, nil
}

// chat 发送 HTTP 请求到 LLM API（内部统一入口）
func (c *Client) chat(req ChatRequest, tools []Tool, toolChoice string) (*ChatResponse, []ToolCall, error) {
	// 检查 API key 是否为空
	if c.apiKey == "" {
		return nil, nil, fmt.Errorf("API key is empty")
	}

	// 在所有请求前统一注入 boot-leader.md 作为系统提示词
	req.Messages = withBootLeaderSystemMessage(req.Messages)

	// 记录实际使用的 URL 和配置（用于调试和排查问题）
	log.Printf("LLM Request: URL=%s, Model=%s, Provider=%T, APIKey present=%v", 
		c.apiURL, c.model, c.provider, c.apiKey != "")

	// 使用 provider 构建请求（如传入 tools，则附带）
	httpReq, err := c.provider.BuildRequest(c.apiURL, c.apiKey, c.model, req.Messages, req.MaxTokens, req.Temperature, tools, toolChoice)
	if err != nil {
		return nil, nil, err
	}

	// 调试：检查 header 是否设置（仅在开发时启用）
	if os.Getenv("DEBUG_LLM") == "true" {
		log.Printf("DEBUG: API URL: %s", c.apiURL)
		log.Printf("DEBUG: Provider: %T", c.provider)
		log.Printf("DEBUG: API Key length: %d", len(c.apiKey))
		authHeader := httpReq.Header.Get("Authorization")
		if len(authHeader) > 20 {
			log.Printf("DEBUG: Authorization header: %s...", authHeader[:20])
		} else {
			log.Printf("DEBUG: Authorization header: %s", authHeader)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// EOF 错误可能是连接问题或 API key 问题
		log.Printf("HTTP Request failed: URL=%s, Error=%v", c.apiURL, err)
		errStr := err.Error()
		if errStr == "EOF" || strings.Contains(errStr, "EOF") {
			authHeader := httpReq.Header.Get("Authorization")
			authPresent := authHeader != ""
			authPrefix := ""
			if len(authHeader) > 20 {
				authPrefix = authHeader[:20]
			} else {
				authPrefix = authHeader
			}
			
			// 针对千问的 EOF 错误提供更具体的建议
			helpMsg := ""
			if strings.Contains(c.apiURL, "dashscope") {
				helpMsg = "\nFor Qwen/DashScope EOF errors, try:\n" +
					"1. Verify API key matches your region (China/International/US)\n" +
					"2. Try international endpoint: https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions\n" +
					"3. Verify API key is valid and has proper permissions\n" +
					"4. Check network connectivity to DashScope servers"
			}
			
			return nil, nil, fmt.Errorf("connection closed unexpectedly (EOF). URL=%s, AuthHeader present=%v, AuthPrefix=%s.%s\nPossible causes: 1) API key invalid/missing or region mismatch, 2) Network issue, 3) API endpoint incorrect", 
				c.apiURL, authPresent, authPrefix, helpMsg)
		}
		return nil, nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Failed to read response body: %v", err)
		return nil, nil, fmt.Errorf("failed to read response: %w", err)
	}

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		errorMsg := string(body)
		if len(errorMsg) > 500 {
			errorMsg = errorMsg[:500] + "..."
		}
		log.Printf("API returned non-200 status: %d, URL=%s, Body: %s", resp.StatusCode, c.apiURL, errorMsg)
		
		// 对于 404 错误，提供更具体的提示
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil, fmt.Errorf("API endpoint not found (404). URL=%s. Please check: 1) URL is correct (should end with /chat/completions), 2) Model name is valid (e.g., qwen-plus, qwen-turbo), 3) API endpoint matches your region", c.apiURL)
		}
		
		return nil, nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, errorMsg)
	}

	// 使用 provider 解析响应（得到文本与 tools 调用）
	content, toolCalls, err := c.provider.ParseResponse(body)
	if err != nil {
		return nil, nil, err
	}

	// 将本次 LLM 交互写入可选的日志文件（通过 LLM_LOG_FILE 控制，避免影响正常 stdout 日志）。
	c.appendLLMLog(req, tools, toolChoice, content, toolCalls, body)

	// 转换为 ChatResponse 格式（向后兼容）
	chatResp := &ChatResponse{
		Choices: []struct {
			Index        int     `json:"index"`
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
	}

	return chatResp, toolCalls, nil
}

// appendLLMLog 将一次 LLM 调用追加写入到日志文件。
// 日志文件由环境变量 LLM_LOG_FILE 控制；未设置则不写入文件。
// 采用 JSON Lines 格式，便于后续分析与采样。
func (c *Client) appendLLMLog(req ChatRequest, tools []Tool, toolChoice string, content string, toolCalls []ToolCall, rawBody []byte) {
	logPath := os.Getenv("LLM_LOG_FILE")
	if logPath == "" {
		logPath = "llm.log"
	}

	entry := map[string]interface{}{
		"timestamp":   time.Now().Format(time.RFC3339),
		"url":         c.apiURL,
		"model":       c.model,
		"messages":    req.Messages,
		"max_tokens":  req.MaxTokens,
		"temperature": req.Temperature,
		"tools":       tools,
		"tool_choice": toolChoice,
		"response": map[string]interface{}{
			"content":    content,
			"tool_calls": toolCalls,
		},
	}

	// 为避免日志文件过大，可选地截断原始响应体
	const maxRawBodyBytes = 4096 * 4096
	if len(rawBody) > 0 {
		if len(rawBody) > maxRawBodyBytes {
			entry["raw_body"] = string(rawBody[:maxRawBodyBytes]) + "..."
		} else {
			entry["raw_body"] = string(rawBody)
		}
	}

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("Failed to marshal LLM log entry: %v", err)
		return
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open LLM log file %s: %v", logPath, err)
		return
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("Failed to write LLM log entry to %s: %v", logPath, err)
	}
}

// IsAvailable 检查 LLM 客户端是否可用（检查 API key）
func IsAvailable() bool {
	// 检查任何可用的 API Key
	return os.Getenv("OPENAI_API_KEY") != "" ||
		os.Getenv("DASHSCOPE_API_KEY") != "" ||
		os.Getenv("ANTHROPIC_API_KEY") != "" ||
		os.Getenv("LLM_API_KEY") != ""
}
