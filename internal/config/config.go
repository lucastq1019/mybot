package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultBrainDirName 默认 brain 目录名称
	DefaultBrainDirName = "brain"
	// DefaultConfigFileName 默认配置文件名称
	DefaultConfigFileName = ".cata/config.json"
	// EnvBrainDir 环境变量名，用于指定 brain 目录的绝对路径
	EnvBrainDir = "CATA_BRAIN_DIR"
	// EnvConfigFile 环境变量名，用于指定配置文件路径
	EnvConfigFile = "CATA_CONFIG_FILE"
)

var (
	// BrainDir brain 目录的绝对路径
	BrainDir string
	// BrainBaseDir brain 目录的基目录（用于 git 操作）
	BrainBaseDir string
	// Config 全局配置对象
	Config *AppConfig
)

// AppConfig 应用配置
type AppConfig struct {
	// Brain 配置
	Brain BrainConfig `json:"brain"`

	// LLM API 配置
	LLM LLMConfig `json:"llm"`

	// Server 配置
	Server ServerConfig `json:"server"`

	// Evolution 配置
	Evolution EvolutionConfig `json:"evolution"`
}

// BrainConfig Brain 目录配置
type BrainConfig struct {
	// Dir brain 目录的绝对路径（优先级最高）
	Dir string `json:"dir"`
	// BaseDir brain 基目录（用于 git 操作，默认与 Dir 相同）
	BaseDir string `json:"base_dir"`
}

// LLMConfig LLM API 配置
type LLMConfig struct {
	// Provider LLM 提供商（openai, anthropic 等）
	Provider string `json:"provider"`
	// APIKey API 密钥
	APIKey string `json:"api_key"`
	// APIURL API 地址
	APIURL string `json:"api_url"`
	// Model 模型名称
	Model string `json:"model"`
	// Models 按角色配置的模型名称映射（如 default/compress/index/summarize/evolution）
	Models map[string]string `json:"models"`
	// MaxTokens 最大 token 数
	MaxTokens int `json:"max_tokens"`
	// Timeout 超时时间（秒）
	Timeout int `json:"timeout"`
	// Enabled 是否启用 LLM 功能
	Enabled bool `json:"enabled"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	// SocketPath Socket 文件路径（相对于 BrainBaseDir）
	SocketPath string `json:"socket_path"`
	// LogLevel 日志级别（debug, info, warn, error）
	LogLevel string `json:"log_level"`
}

// EvolutionConfig 自主演进配置
type EvolutionConfig struct {
	// Enabled 是否启用自主演进
	Enabled bool `json:"enabled"`
	// CycleInterval 循环间隔（秒）
	CycleInterval int `json:"cycle_interval"`
	// TaskQueueInterval 任务队列检查间隔（秒）
	TaskQueueInterval int `json:"task_queue_interval"`
	// FallbackLowSuccessRate 回退策略中“低成功率”阈值（0-1）
	FallbackLowSuccessRate float64 `json:"fallback_low_success_rate"`
	// FallbackMinRecentTasks 触发低成功率判断所需的最少近期任务数
	FallbackMinRecentTasks int `json:"fallback_min_recent_tasks"`
	// FallbackPendingTasksThreshold 回退策略中 pending tasks 的触发阈值
	FallbackPendingTasksThreshold int `json:"fallback_pending_tasks_threshold"`
}

// LoadConfig 加载配置文件
func LoadConfig() (*AppConfig, error) {
	configPath := getConfigPath()

	// 如果配置文件存在，加载它
	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		var cfg AppConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}

		// 应用环境变量覆盖
		applyEnvOverrides(&cfg)

		// 验证和设置默认值
		if err := validateAndSetDefaults(&cfg); err != nil {
			return nil, err
		}

		// 设置全局配置
		Config = &cfg
		return &cfg, nil
	}

	// 配置文件不存在，使用默认配置
	cfg := getDefaultConfig()
	applyEnvOverrides(cfg)
	validateAndSetDefaults(cfg)

	// 设置全局配置
	Config = cfg
	return cfg, nil
}

// SaveConfig 保存配置文件
func SaveConfig(config *AppConfig) error {
	configPath := getConfigPath()

	// 确保配置目录存在
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0644)
}

// getConfigPath 获取配置文件路径
func getConfigPath() string {
	// 优先使用环境变量
	if envPath := os.Getenv(EnvConfigFile); envPath != "" {
		return envPath
	}

	// 使用项目根目录或当前工作目录
	baseDir := findProjectRoot()
	if baseDir == "" {
		wd, _ := os.Getwd()
		baseDir = wd
	}

	return filepath.Join(baseDir, DefaultConfigFileName)
}

// GetConfigPath 获取配置文件路径（导出函数）
func GetConfigPath() string {
	return getConfigPath()
}

// getDefaultConfig 获取默认配置
func getDefaultConfig() *AppConfig {
	return &AppConfig{
		Brain: BrainConfig{
			Dir:     "", // 将在 validateAndSetDefaults 中设置
			BaseDir: "", // 将在 validateAndSetDefaults 中设置
		},
		LLM: LLMConfig{
			Provider:  getDefaultProvider(),
			APIKey:     getDefaultAPIKey(),
			APIURL:     getDefaultAPIURL(),
			Model:      getDefaultModel(),
			MaxTokens:  2000,
			Timeout:    60,
			Enabled:    getDefaultAPIKey() != "",
		},
		Server: ServerConfig{
			SocketPath: ".cata/cata.sock",
			LogLevel:   "info",
		},
		Evolution: EvolutionConfig{
			Enabled:                       true,
			CycleInterval:                 3600, // 1 小时
			TaskQueueInterval:             30,   // 30 秒
			FallbackLowSuccessRate:        0.5,
			FallbackMinRecentTasks:        3,
			FallbackPendingTasksThreshold: 1,
		},
	}
}

// applyEnvOverrides 应用环境变量覆盖
func applyEnvOverrides(config *AppConfig) {
	// Brain 目录
	if envDir := os.Getenv(EnvBrainDir); envDir != "" {
		config.Brain.Dir = envDir
	}

	// LLM API Key（支持多种提供商的环境变量）
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		config.LLM.APIKey = apiKey
		if config.LLM.Provider == "" {
			config.LLM.Provider = "openai"
		}
		config.LLM.Enabled = true
	}
	// API Key 优先使用配置文件中的值，仅在配置为空时使用环境变量
	if apiKey := os.Getenv("DASHSCOPE_API_KEY"); apiKey != "" && config.LLM.APIKey == "" {
		config.LLM.APIKey = apiKey
		if config.LLM.Provider == "" {
			config.LLM.Provider = "qwen"
		}
		config.LLM.Enabled = true
	}
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" && config.LLM.APIKey == "" {
		config.LLM.APIKey = apiKey
		if config.LLM.Provider == "" {
			config.LLM.Provider = "claude"
		}
		config.LLM.Enabled = true
	}
	// 通用 LLM 配置环境变量（仅在配置文件中的值为空时使用）
	// 优先使用配置文件中的值
	if config.LLM.APIURL == "" {
		if apiURL := os.Getenv("LLM_API_URL"); apiURL != "" {
			config.LLM.APIURL = apiURL
		} else if apiURL := os.Getenv("OPENAI_API_URL"); apiURL != "" {
			config.LLM.APIURL = apiURL
		}
	}
	// Model 和 Provider 也优先使用配置文件中的值
	if config.LLM.Model == "" {
		if model := os.Getenv("LLM_MODEL"); model != "" {
			config.LLM.Model = model
		} else if model := os.Getenv("OPENAI_MODEL"); model != "" {
			config.LLM.Model = model
		}
	}
	if config.LLM.Provider == "" {
		if provider := os.Getenv("LLM_PROVIDER"); provider != "" {
			config.LLM.Provider = provider
		}
	}
}

// validateAndSetDefaults 验证配置并设置默认值
func validateAndSetDefaults(config *AppConfig) error {
	// 设置 Brain 目录
	if config.Brain.Dir == "" {
		// 使用环境变量或查找项目根目录
		if envDir := os.Getenv(EnvBrainDir); envDir != "" {
			absPath, err := filepath.Abs(envDir)
			if err != nil {
				return fmt.Errorf("invalid CATA_BRAIN_DIR path: %w", err)
			}
			config.Brain.Dir = absPath
			config.Brain.BaseDir = absPath
		} else {
			projectRoot := findProjectRoot()
			if projectRoot != "" {
				config.Brain.Dir = filepath.Join(projectRoot, DefaultBrainDirName)
				config.Brain.BaseDir = projectRoot
			} else {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get working directory: %w", err)
				}
				config.Brain.Dir = filepath.Join(wd, DefaultBrainDirName)
				config.Brain.BaseDir = wd
			}
		}
	} else {
		// 确保是绝对路径
		absPath, err := filepath.Abs(config.Brain.Dir)
		if err != nil {
			return fmt.Errorf("invalid brain dir path: %w", err)
		}
		config.Brain.Dir = absPath

		// 设置 BaseDir
		if config.Brain.BaseDir == "" {
			config.Brain.BaseDir = absPath
		} else {
			absBaseDir, err := filepath.Abs(config.Brain.BaseDir)
			if err != nil {
				return fmt.Errorf("invalid brain base dir path: %w", err)
			}
			config.Brain.BaseDir = absBaseDir
		}
	}

	// 设置全局变量
	BrainDir = config.Brain.Dir
	BrainBaseDir = config.Brain.BaseDir

	// 如果 LLM 配置为空，从环境变量自动填充
	if config.LLM.Provider == "" {
		config.LLM.Provider = getDefaultProvider()
	}
	if config.LLM.APIKey == "" {
		config.LLM.APIKey = getDefaultAPIKey()
	}
	if config.LLM.APIURL == "" {
		config.LLM.APIURL = getDefaultAPIURLForProvider(config.LLM.Provider)
	}
	if config.LLM.Model == "" {
		config.LLM.Model = getDefaultModelForProvider(config.LLM.Provider)
	}
	// 如果环境变量有 API key，自动启用 LLM（即使配置文件中 enabled 为 false）
	if config.LLM.APIKey != "" {
		config.LLM.Enabled = true
	}

	// Evolution 回退策略默认值与边界修正
	if config.Evolution.FallbackLowSuccessRate <= 0 || config.Evolution.FallbackLowSuccessRate >= 1 {
		config.Evolution.FallbackLowSuccessRate = 0.5
	}
	if config.Evolution.FallbackMinRecentTasks <= 0 {
		config.Evolution.FallbackMinRecentTasks = 3
	}
	if config.Evolution.FallbackPendingTasksThreshold <= 0 {
		config.Evolution.FallbackPendingTasksThreshold = 1
	}

	return nil
}

// InitBrainPath 初始化 brain 目录路径（向后兼容）
func InitBrainPath() error {
	if Config == nil {
		var err error
		Config, err = LoadConfig()
		if err != nil {
			return err
		}
	}
	return nil
}

// GetBrainDir 获取 brain 目录路径
func GetBrainDir() string {
	if Config == nil {
		InitBrainPath()
	}
	if BrainDir == "" {
		wd, _ := os.Getwd()
		BrainDir = filepath.Join(wd, DefaultBrainDirName)
	}
	return BrainDir
}

// GetBrainBaseDir 获取 brain 基目录路径（用于 git 操作）
func GetBrainBaseDir() string {
	if Config == nil {
		InitBrainPath()
	}
	if BrainBaseDir == "" {
		BrainBaseDir = GetBrainDir()
	}
	return BrainBaseDir
}

// GetBrainPath 获取 brain 目录下的文件路径
func GetBrainPath(relPath string) string {
	return filepath.Join(GetBrainDir(), relPath)
}

// findProjectRoot 查找项目根目录（包含 go.mod 或 .git 的目录）
func findProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}

	dir := wd
	for {
		// 检查是否存在 go.mod 或 .git
		goModPath := filepath.Join(dir, "go.mod")
		gitPath := filepath.Join(dir, ".git")

		if _, err := os.Stat(goModPath); err == nil {
			return dir
		}
		if _, err := os.Stat(gitPath); err == nil {
			return dir
		}

		// 向上查找
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 已到达根目录
		}
		dir = parent
	}

	return ""
}

// getDefaultProvider 获取默认提供商（根据环境变量）
func getDefaultProvider() string {
	if provider := os.Getenv("LLM_PROVIDER"); provider != "" {
		return provider
	}
	if os.Getenv("DASHSCOPE_API_KEY") != "" {
		return "qwen"
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "claude"
	}
	return "openai"
}

// getDefaultAPIKey 获取默认 API Key（根据环境变量）
func getDefaultAPIKey() string {
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("DASHSCOPE_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key
	}
	return ""
}

// getDefaultAPIURL 获取默认 API URL（根据提供商）
func getDefaultAPIURL() string {
	if url := os.Getenv("LLM_API_URL"); url != "" {
		return url
	}
	if url := os.Getenv("OPENAI_API_URL"); url != "" {
		return url
	}
	// 根据提供商返回默认 URL
	provider := getDefaultProvider()
	switch provider {
	case "qwen", "tongyi", "dashscope":
		// 使用 OpenAI 兼容模式（推荐）
		return "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	case "claude", "anthropic":
		return "https://api.anthropic.com/v1/messages"
	default:
		return "https://api.openai.com/v1/chat/completions"
	}
}

// getDefaultAPIURLForProvider 根据指定的 provider 获取默认 URL
func getDefaultAPIURLForProvider(provider string) string {
	if url := os.Getenv("LLM_API_URL"); url != "" {
		return url
	}
	switch provider {
	case "qwen", "tongyi", "dashscope":
		// 使用 OpenAI 兼容模式（推荐）
		return "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	case "claude", "anthropic":
		return "https://api.anthropic.com/v1/messages"
	default:
		if url := os.Getenv("OPENAI_API_URL"); url != "" {
			return url
		}
		return "https://api.openai.com/v1/chat/completions"
	}
}

// getDefaultModel 获取默认模型（根据提供商）
func getDefaultModel() string {
	if model := os.Getenv("LLM_MODEL"); model != "" {
		return model
	}
	if model := os.Getenv("OPENAI_MODEL"); model != "" {
		return model
	}
	// 根据提供商返回默认模型
	provider := getDefaultProvider()
	switch provider {
	case "qwen", "tongyi", "dashscope":
		return "qwen-turbo"
	case "claude", "anthropic":
		return "claude-3-sonnet-20240229"
	default:
		return "gpt-3.5-turbo"
	}
}

// getDefaultModelForProvider 根据指定的 provider 获取默认模型
func getDefaultModelForProvider(provider string) string {
	if model := os.Getenv("LLM_MODEL"); model != "" {
		return model
	}
	switch provider {
	case "qwen", "tongyi", "dashscope":
		return "qwen-turbo"
	case "claude", "anthropic":
		return "claude-3-sonnet-20240229"
	default:
		if model := os.Getenv("OPENAI_MODEL"); model != "" {
			return model
		}
		return "gpt-3.5-turbo"
	}
}
