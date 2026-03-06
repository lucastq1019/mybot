package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"mybot/internal/brain"
	"mybot/internal/config"
	"mybot/internal/evolution"
	"mybot/internal/llm"
	"mybot/internal/memory"
)

const (
	DefaultSocketPath = ".cata/cata.sock"
)

// SocketServer 处理客户端连接
type SocketServer struct {
	server *Server
	ln     net.Listener
}

// Request 客户端请求
type Request struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Response 服务器响应
type Response struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// NewSocketServer 创建 socket 服务器
func NewSocketServer(srv *Server) (*SocketServer, error) {
	socketPath := getSocketPath()
	
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	// 删除已存在的 socket 文件
	if _, err := os.Stat(socketPath); err == nil {
		os.Remove(socketPath)
	}

	// 创建 Unix socket 监听器
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	return &SocketServer{
		server: srv,
		ln:     ln,
	}, nil
}

// getSocketPath 获取 socket 文件路径（使用项目根目录，与 brain 配置一致）
func getSocketPath() string {
	baseDir := config.GetBrainBaseDir()
	if baseDir != "" {
		return filepath.Join(baseDir, ".cata", "cata.sock")
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, DefaultSocketPath)
}

// Start 启动 socket 服务器
func (ss *SocketServer) Start() {
	log.Printf("Socket server listening on: %s", ss.ln.Addr().String())
	
	go func() {
		for {
			conn, err := ss.ln.Accept()
			if err != nil {
				// 检查是否因为关闭而错误
				select {
				case <-ss.server.ctx.Done():
					return
				default:
					log.Printf("Error accepting connection: %v", err)
					continue
				}
			}
			
			// 处理每个连接
			go ss.handleConnection(conn)
		}
	}()
}

// Stop 停止 socket 服务器
func (ss *SocketServer) Stop() {
	if ss.ln != nil {
		ss.ln.Close()
		socketPath := getSocketPath()
		os.Remove(socketPath)
		log.Println("Socket server stopped")
	}
}

// handleConnection 处理客户端连接
func (ss *SocketServer) handleConnection(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 解析请求
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			ss.sendResponse(conn, Response{
				Success: false,
				Message: fmt.Sprintf("Invalid request: %v", err),
			})
			continue
		}

		// 处理命令
		resp := ss.handleCommand(req)
		ss.sendResponse(conn, resp)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from connection: %v", err)
	}
}

// handleCommand 处理命令
func (ss *SocketServer) handleCommand(req Request) Response {
	switch req.Command {
	case "recall":
		return ss.handleRecall(req.Args)
	case "digest":
		return ss.handleDigest(req.Args)
	case "consolidate":
		return ss.handleConsolidate(req.Args)
	case "skill_list":
		return ss.handleSkillList()
	case "skill_get":
		return ss.handleSkillGet(req.Args)
	case "skill_enable":
		return ss.handleSkillEnable(req.Args)
	case "skill_disable":
		return ss.handleSkillDisable(req.Args)
	case "ping":
		return Response{Success: true, Message: "pong"}
	case "evolve":
		return ss.handleEvolve(req.Args)
	case "task":
		return ss.handleTask(req.Args)
	default:
		return Response{
			Success: false,
			Message: fmt.Sprintf("Unknown command: %s", req.Command),
		}
	}
}

// handleRecall 处理 recall 命令
func (ss *SocketServer) handleRecall(args []string) Response {
	if len(args) < 1 {
		return Response{
			Success: false,
			Message: "Usage: recall <query> [topK] [--llm]",
		}
	}

	query := args[0]
	topK := 5
	useLLM := false
	
	// 解析参数
	for i := 1; i < len(args); i++ {
		if args[i] == "--llm" {
			useLLM = true
		} else if topK == 5 {
			// 尝试解析为 topK
			if _, err := fmt.Sscanf(args[i], "%d", &topK); err != nil {
				topK = 5
			}
		}
	}

	// 使用 LLM 预处理（如果启用）
	results, err := ss.server.memMgr.RecallWithPreprocess(query, topK, useLLM)
	if err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Recall failed: %v", err),
		}
	}

	return Response{
		Success: true,
		Message: fmt.Sprintf("Found %d results", len(results)),
		Data:    results,
	}
}

// handleDigest 处理 digest 命令
func (ss *SocketServer) handleDigest(args []string) Response {
	// 解析参数：--since 7d, --week, --month 等
	query := ""
	timeRange := "7d" // 默认 7 天
	
	for i, arg := range args {
		if arg == "--since" && i+1 < len(args) {
			timeRange = args[i+1]
		} else if arg == "--week" {
			timeRange = "7d"
		} else if arg == "--month" {
			timeRange = "30d"
		} else if query == "" {
			query = arg
		}
	}
	
	if query == "" {
		query = "all" // 默认查询所有
	}
	
	// 根据时间范围 Recall（使用 LLM 预处理）
	results, err := ss.server.memMgr.RecallWithPreprocess(query, 20, true)
	if err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Recall failed: %v", err),
		}
	}
	
	// 格式化结果
	summary := memory.FormatMemoryPiecesForSummary(results)
	
	// 调用 LLM 生成摘要（如果有 LLM 集成）
	var llmSummary string
	if config.Config != nil && config.Config.LLM.Enabled || llm.IsAvailable() {
		// 使用按角色配置的摘要模型
		llmClient, err := llm.NewClientForRole(llm.RoleSummarize)
		if err == nil {
			instructions := fmt.Sprintf(
				"你是一个专业的记忆摘要助手。请为以下内容生成一个简洁、结构化的摘要。" +
				"摘要应该保留关键信息、重要事件和决策，使用 Markdown 格式。",
			)
			llmSummary, err = llmClient.Summarize(summary, instructions)
			if err != nil {
				llmSummary = fmt.Sprintf("LLM summary generation failed: %v", err)
			}
		}
	}
	
	return Response{
		Success: true,
		Message: fmt.Sprintf("Digest for %s (time range: %s)", query, timeRange),
		Data: map[string]interface{}{
			"query":      query,
			"time_range": timeRange,
			"summary":    summary,
			"llm_summary": llmSummary,
			"count":      len(results),
		},
	}
}

// handleConsolidate 处理 consolidate 命令
func (ss *SocketServer) handleConsolidate(args []string) Response {
	if len(args) < 2 {
		return Response{
			Success: false,
			Message: "Usage: consolidate <topic> <content>",
		}
	}

	topic := args[0]
	content := strings.Join(args[1:], " ")

	if err := ss.server.memMgr.Consolidate(topic, content); err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Consolidate failed: %v", err),
		}
	}

	return Response{
		Success: true,
		Message: "Content consolidated successfully",
	}
}

// handleSkillList 处理 skill list 命令，合并注册表与 brain/core.md skills-index，便于与技能调用规则一致。
func (ss *SocketServer) handleSkillList() Response {
	registry := ss.server.registry
	config := registry.GetConfig()
	registered := registry.List()

	// 已注册技能名 -> 详情（含 cron、cli_command）
	byName := make(map[string]map[string]interface{})
	for _, skill := range registered {
		enabled := config.IsSkillEnabled(skill.Name())
		byName[skill.Name()] = map[string]interface{}{
			"name":         skill.Name(),
			"enabled":      enabled,
			"cron":         skill.CronSchedule(),
			"cli_command":  skill.CLICommand(),
			"implemented":  true,
		}
	}

	// 用 skills-index 补全 description、tags，并标出仅存在于索引的技能
	if idx, err := ss.server.GetSkillsIndexLoader().Get(); err == nil && idx != nil {
		for _, meta := range idx.Skills {
			if info, ok := byName[meta.Name]; ok {
				info["description"] = meta.Description
				info["tags"] = meta.Tags
				info["dependencies"] = meta.Dependencies
			} else {
				byName[meta.Name] = map[string]interface{}{
					"name":         meta.Name,
					"description":  meta.Description,
					"tags":         meta.Tags,
					"dependencies": meta.Dependencies,
					"implemented":  false,
				}
			}
		}
	}

	skillInfo := make([]map[string]interface{}, 0, len(byName))
	for _, info := range byName {
		skillInfo = append(skillInfo, info)
	}

	return Response{
		Success: true,
		Message: fmt.Sprintf("Found %d skills (registry + brain skills-index)", len(skillInfo)),
		Data:    skillInfo,
	}
}

// handleSkillGet 返回指定技能的 SKILL.md 全文，供 Agent（Cursor/LLM）按 core.md 规则执行 MD 技能。
// 与 brain 对齐：技能索引与路径来自 skills-index.json，MD 技能由 Agent 读入并执行，server 仅提供内容。
func (ss *SocketServer) handleSkillGet(args []string) Response {
	if len(args) < 1 {
		return Response{
			Success: false,
			Message: "Usage: skill_get <skill-name>",
		}
	}
	skillName := args[0]
	meta, err := ss.server.GetSkillsIndexLoader().SkillByName(skillName)
	if err != nil || meta == nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Skill not found in skills-index: %s", skillName),
		}
	}
	absPath := filepath.Join(brain.BaseDir(), meta.Path)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to read SKILL.md: %v", err),
		}
	}
	return Response{
		Success: true,
		Message: fmt.Sprintf("Skill: %s", skillName),
		Data: map[string]interface{}{
			"name":        meta.Name,
			"path":        meta.Path,
			"description": meta.Description,
			"tags":        meta.Tags,
			"content":     string(content),
		},
	}
}

// handleEvolve 处理 evolve 命令
func (ss *SocketServer) handleEvolve(args []string) Response {
	if len(args) == 0 {
		return Response{
			Success: false,
			Message: "Usage: evolve <subcommand> [args]",
		}
	}

	subcommand := args[0]

	switch subcommand {
	case "status":
		return ss.handleEvolveStatus()
	case "history":
		return ss.handleEvolveHistory()
	case "once":
		return ss.handleEvolveOnce()
	default:
		return Response{
			Success: false,
			Message: fmt.Sprintf("Unknown evolve subcommand: %s", subcommand),
		}
	}
}

// handleEvolveStatus 处理 evolve status 命令
func (ss *SocketServer) handleEvolveStatus() Response {
	if ss.server.evolution == nil {
		return Response{
			Success: false,
			Message: "Evolution engine not available",
		}
	}

	// 获取状态分析器
	analyzer := evolution.NewStateAnalyzer(ss.server.memMgr)
	state, err := analyzer.Analyze()
	if err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to analyze state: %v", err),
		}
	}

	return Response{
		Success: true,
		Message: "Evolution status",
		Data: map[string]interface{}{
			"memory_state": map[string]interface{}{
				"archive_file_count": state.MemoryState.ArchiveFileCount,
				"archive_total_size": state.MemoryState.ArchiveTotalSize,
				"index_entry_count":  state.MemoryState.IndexEntryCount,
				"needs_summarize":    state.MemoryState.NeedsSummarize,
				"summarize_reason":   state.MemoryState.SummarizeReason,
			},
			"task_state": map[string]interface{}{
				"success_rate":   state.TaskState.SuccessRate,
				"pending_tasks":  state.TaskState.PendingTasks,
				"last_task_time": state.TaskState.LastTaskTime,
			},
			"evolution_state": map[string]interface{}{
				"capabilities_count": len(state.EvolutionState.Capabilities),
				"last_evolution":     state.EvolutionState.LastEvolution,
			},
		},
	}
}

// handleEvolveHistory 处理 evolve history 命令
func (ss *SocketServer) handleEvolveHistory() Response {
	// 读取 evolution_log.json
	logFile := evolution.EvolutionLogFilePath
	data, err := os.ReadFile(logFile)
	if err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to read evolution log: %v", err),
		}
	}

	var log evolution.EvolutionLog
	if err := json.Unmarshal(data, &log); err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to parse evolution log: %v", err),
		}
	}

	// 返回最近 20 条记录
	start := len(log.Entries) - 20
	if start < 0 {
		start = 0
	}

	return Response{
		Success: true,
		Message: fmt.Sprintf("Found %d evolution entries", len(log.Entries)),
		Data:    log.Entries[start:],
	}
}

// handleEvolveOnce 处理 evolve once 命令（手动触发一次演进循环）
func (ss *SocketServer) handleEvolveOnce() Response {
	if ss.server.evolution == nil {
		return Response{
			Success: false,
			Message: "Evolution engine not available",
		}
	}

	// 在后台执行一次演进循环
	go func() {
		if err := ss.server.evolution.ExecuteAutonomousCycle(ss.server.ctx); err != nil {
			log.Printf("Error executing evolution cycle: %v", err)
		}
	}()

	return Response{
		Success: true,
		Message: "Evolution cycle triggered, check status later",
	}
}

// handleTask 处理 task 命令
func (ss *SocketServer) handleTask(args []string) Response {
	if len(args) == 0 {
		return Response{
			Success: false,
			Message: "Usage: task <create|list|status> [args]",
		}
	}

	subcommand := args[0]

	switch subcommand {
	case "create":
		return ss.handleTaskCreate(args[1:])
	case "list":
		return ss.handleTaskList()
	case "status":
		if len(args) < 2 {
			return Response{
				Success: false,
				Message: "Usage: task status <task-id>",
			}
		}
		return ss.handleTaskStatus(args[1])
	default:
		return Response{
			Success: false,
			Message: fmt.Sprintf("Unknown task subcommand: %s", subcommand),
		}
	}
}

// handleTaskCreate 处理 task create 命令
func (ss *SocketServer) handleTaskCreate(args []string) Response {
	if ss.server.evolution == nil {
		return Response{
			Success: false,
			Message: "Evolution engine not available",
		}
	}

	if len(args) < 1 {
		return Response{
			Success: false,
			Message: "Usage: task create <type> [steps...] [--async]\n       task create \"<你的需求描述（长串字符串）>\" [--async]\nTypes: summarize, consolidate, recall, learn, optimize, reflect, idle, integrate, custom\n直接传入一句需求时自动按 custom 执行，由 cata 解析并执行",
		}
	}

	knownTypes := map[string]bool{
		"summarize": true, "consolidate": true, "recall": true, "learn": true,
		"optimize": true, "reflect": true, "idle": true, "integrate": true,
	}

	// 解析 --async，剩余部分为 type + steps 或仅需求字符串
	rest := []string{}
	async := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--async" {
			async = true
		} else {
			rest = append(rest, args[i])
		}
	}

	var taskType string
	var steps []string
	if len(rest) >= 1 && knownTypes[rest[0]] {
		taskType = rest[0]
		steps = rest[1:]
		// 构建 ActionPlan
	} else if len(rest) >= 1 {
		// 首参数不是已知类型：整段视为用户需求（custom）
		taskType = "custom"
		steps = []string{strings.Join(rest, " ")}
	} else {
		return Response{
			Success: false,
			Message: "Usage: task create \"<需求描述>\" or task create <type> [steps...]",
		}
	}

	reason := fmt.Sprintf("Task created via catacli: %s", taskType)
	if taskType == "custom" && len(steps) > 0 {
		reason = steps[0]
		if len(reason) > 200 {
			reason = reason[:200] + "..."
		}
	}
	actionPlan := &evolution.ActionPlan{
		Action:          taskType,
		Reason:          reason,
		Steps:           steps,
		ExpectedOutcome: fmt.Sprintf("Execute %s task successfully", taskType),
		Priority:        5,
	}

	if async {
		// 异步执行：加入队列
		queuedTask, err := ss.server.evolution.EnqueueTask(actionPlan, "user")
		if err != nil {
			return Response{
				Success: false,
				Message: fmt.Sprintf("Failed to enqueue task: %v", err),
			}
		}

		return Response{
			Success: true,
			Message: fmt.Sprintf("Task queued successfully: %s", queuedTask.ID),
			Data: map[string]interface{}{
				"task_id":   queuedTask.ID,
				"type":      queuedTask.Type,
				"status":    queuedTask.Status,
				"created_at": queuedTask.CreatedAt,
				"message":   "Task will be executed by cata automatically",
			},
		}
	} else {
		// 同步执行：立即执行并返回结果
		result, err := ss.server.evolution.ExecuteTask(ss.server.ctx, actionPlan)
		if err != nil {
			return Response{
				Success: false,
				Message: fmt.Sprintf("Task execution failed: %v", err),
			}
		}

		return Response{
			Success: true,
			Message: fmt.Sprintf("Task executed successfully: %s", result.Output),
			Data: map[string]interface{}{
				"task_id":  actionPlan.Action,
				"output":   result.Output,
				"learning": result.Learning,
				"success":  result.Success,
			},
		}
	}
}

// handleTaskList 处理 task list 命令
func (ss *SocketServer) handleTaskList() Response {
	if ss.server.evolution == nil {
		return Response{
			Success: false,
			Message: "Evolution engine not available",
		}
	}

	// 从任务队列获取任务列表
	queue := ss.server.evolution.GetTaskQueue()
	tasks := queue.ListTasks("", 50) // 获取最近 50 条任务

	return Response{
		Success: true,
		Message: fmt.Sprintf("Found %d tasks", len(tasks)),
		Data:    tasks,
	}
}

// handleTaskStatus 处理 task status 命令
func (ss *SocketServer) handleTaskStatus(taskID string) Response {
	if ss.server.evolution == nil {
		return Response{
			Success: false,
			Message: "Evolution engine not available",
		}
	}

	// 从任务队列查找任务
	queue := ss.server.evolution.GetTaskQueue()
	task := queue.GetTask(taskID)
	if task != nil {
		return Response{
			Success: true,
			Message: "Task found",
			Data:    task,
		}
	}

	return Response{
		Success: false,
		Message: fmt.Sprintf("Task not found: %s", taskID),
	}
}

// handleSkillEnable 处理 skill enable 命令
func (ss *SocketServer) handleSkillEnable(args []string) Response {
	if len(args) < 1 {
		return Response{
			Success: false,
			Message: "Usage: skill_enable <skill-name>",
		}
	}

	skillName := args[0]
	if err := ss.server.registry.EnableSkill(skillName); err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to enable skill: %v", err),
		}
	}

	return Response{
		Success: true,
		Message: fmt.Sprintf("Skill '%s' enabled", skillName),
	}
}

// handleSkillDisable 处理 skill disable 命令
func (ss *SocketServer) handleSkillDisable(args []string) Response {
	if len(args) < 1 {
		return Response{
			Success: false,
			Message: "Usage: skill_disable <skill-name>",
		}
	}

	skillName := args[0]
	if err := ss.server.registry.DisableSkill(skillName); err != nil {
		return Response{
			Success: false,
			Message: fmt.Sprintf("Failed to disable skill: %v", err),
		}
	}

	return Response{
		Success: true,
		Message: fmt.Sprintf("Skill '%s' disabled", skillName),
	}
}

// sendResponse 发送响应
func (ss *SocketServer) sendResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("Error marshaling response: %v", err)
		return
	}

	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		log.Printf("Error writing response: %v", err)
	}
}
