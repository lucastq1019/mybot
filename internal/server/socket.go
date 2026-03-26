package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mybot/internal/config"
	"mybot/internal/evolution"
	"mybot/internal/memory"
)

const (
	DefaultSocketPath = ".cata/cata.sock"

	cmdRecall      = "recall"
	cmdDigest      = "digest"
	cmdConsolidate = "consolidate"
	cmdEvolve      = "evolve"
	cmdTask        = "task"
	cmdPing        = "ping"

	msgPong                 = "pong"
	msgUnknownCommandPrefix = "unknown command: "

	usageRecall      = "usage: recall <query> [topK] [--llm]"
	usageConsolidate = "usage: consolidate <topic> <content>"
	usageTask        = "usage: task <create|list|status> [args]"
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
	case cmdRecall:
		return ss.handleRecall(req.Args)
	case cmdDigest:
		return ss.handleDigest(req.Args)
	case cmdConsolidate:
		return ss.handleConsolidate(req.Args)
	case cmdEvolve:
		return ss.handleEvolve(req.Args)
	case cmdTask:
		return ss.handleTask(req.Args)
	case cmdPing:
		return Response{Success: true, Message: msgPong}
	default:
		return Response{
			Success: false,
			Message: msgUnknownCommandPrefix + req.Command,
		}
	}
}

// handleRecall 处理 recall 命令
func (ss *SocketServer) handleRecall(args []string) Response {
	if len(args) < 1 {
		return Response{
			Success: false,
			Message: usageRecall,
		}
	}

	query := args[0]
	topK := 5
	useLLM := false

	// 解析参数
	for i := 1; i < len(args); i++ {
		if args[i] == "--llm" {
			useLLM = true
		} else if parsed, err := strconv.Atoi(args[i]); err == nil {
			topK = parsed
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

	return Response{
		Success: true,
		Message: fmt.Sprintf("Digest for %s (time range: %s)", query, timeRange),
		Data: map[string]interface{}{
			"query":     query,
			"time_range": timeRange,
			"summary":   summary,
			"count":     len(results),
		},
	}
}

// handleConsolidate 处理 consolidate 命令
func (ss *SocketServer) handleConsolidate(args []string) Response {
	if len(args) < 2 {
		return Response{
			Success: false,
			Message: usageConsolidate,
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

func (ss *SocketServer) handleEvolve(args []string) Response {
	if len(args) == 0 {
		return Response{Success: false, Message: "usage: evolve <status|history|once>"}
	}

	switch args[0] {
	case "status":
		return ss.handleEvolveStatus()
	case "history":
		return ss.handleEvolveHistory()
	case "once":
		return ss.handleEvolveOnce()
	default:
		return Response{Success: false, Message: "unknown evolve subcommand: " + args[0]}
	}
}

func (ss *SocketServer) handleEvolveStatus() Response {
	if ss.server.evolution == nil {
		return Response{Success: false, Message: "evolution engine not available"}
	}

	analyzer := evolution.NewStateAnalyzer(ss.server.memMgr)
	state, err := analyzer.Analyze()
	if err != nil {
		return Response{Success: false, Message: fmt.Sprintf("failed to analyze state: %v", err)}
	}

	return Response{
		Success: true,
		Message: "evolution status",
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

func (ss *SocketServer) handleEvolveHistory() Response {
	data, err := os.ReadFile(evolution.EvolutionLogFilePath)
	if err != nil {
		return Response{Success: false, Message: fmt.Sprintf("failed to read evolution log: %v", err)}
	}

	var logData evolution.EvolutionLog
	if err := json.Unmarshal(data, &logData); err != nil {
		return Response{Success: false, Message: fmt.Sprintf("failed to parse evolution log: %v", err)}
	}

	start := len(logData.Entries) - 20
	if start < 0 {
		start = 0
	}
	return Response{
		Success: true,
		Message: fmt.Sprintf("found %d evolution entries", len(logData.Entries)),
		Data:    logData.Entries[start:],
	}
}

func (ss *SocketServer) handleEvolveOnce() Response {
	if ss.server.evolution == nil {
		return Response{Success: false, Message: "evolution engine not available"}
	}

	go func() {
		if err := ss.server.evolution.ExecuteAutonomousCycle(ss.server.ctx); err != nil {
			log.Printf("Error executing evolution cycle: %v", err)
		}
	}()

	return Response{Success: true, Message: "evolution cycle triggered"}
}

func (ss *SocketServer) handleTask(args []string) Response {
	if len(args) == 0 {
		return Response{Success: false, Message: usageTask}
	}

	switch args[0] {
	case "create":
		return ss.handleTaskCreate(args[1:])
	case "list":
		return ss.handleTaskList()
	case "status":
		if len(args) < 2 {
			return Response{Success: false, Message: "usage: task status <task-id>"}
		}
		return ss.handleTaskStatus(args[1])
	default:
		return Response{Success: false, Message: "unknown task subcommand: " + args[0]}
	}
}

func (ss *SocketServer) handleTaskCreate(args []string) Response {
	if ss.server.evolution == nil {
		return Response{Success: false, Message: "evolution engine not available"}
	}
	if len(args) < 1 {
		return Response{Success: false, Message: "usage: task create <type|description> [steps...] [--async]"}
	}

	knownTypes := map[string]bool{
		"summarize": true, "consolidate": true, "recall": true, "learn": true,
		"optimize": true, "reflect": true, "idle": true, "integrate": true,
	}

	rest := []string{}
	async := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--async" {
			async = true
		} else {
			rest = append(rest, args[i])
		}
	}

	taskType := "custom"
	steps := []string{}
	if len(rest) >= 1 && knownTypes[rest[0]] {
		taskType = rest[0]
		steps = rest[1:]
	} else if len(rest) >= 1 {
		steps = []string{strings.Join(rest, " ")}
	} else {
		return Response{Success: false, Message: "usage: task create <type|description> [steps...] [--async]"}
	}

	reason := fmt.Sprintf("Task created via catacli: %s", taskType)
	if taskType == "custom" && len(steps) > 0 {
		reason = steps[0]
	}

	actionPlan := &evolution.ActionPlan{
		Action:          taskType,
		Reason:          reason,
		Steps:           steps,
		ExpectedOutcome: fmt.Sprintf("Execute %s task successfully", taskType),
		Priority:        5,
	}

	if async {
		queuedTask, err := ss.server.evolution.EnqueueTask(actionPlan, "user")
		if err != nil {
			return Response{Success: false, Message: fmt.Sprintf("failed to enqueue task: %v", err)}
		}
		return Response{
			Success: true,
			Message: fmt.Sprintf("task queued: %s", queuedTask.ID),
			Data:    queuedTask,
		}
	}

	result, err := ss.server.evolution.ExecuteTask(ss.server.ctx, actionPlan)
	if err != nil {
		return Response{Success: false, Message: fmt.Sprintf("task execution failed: %v", err)}
	}
	return Response{
		Success: true,
		Message: "task executed",
		Data: map[string]interface{}{
			"task_type": taskType,
			"output":    result.Output,
			"learning":  result.Learning,
			"success":   result.Success,
		},
	}
}

func (ss *SocketServer) handleTaskList() Response {
	if ss.server.evolution == nil {
		return Response{Success: false, Message: "evolution engine not available"}
	}
	queue := ss.server.evolution.GetTaskQueue()
	tasks := queue.ListTasks("", 50)
	return Response{
		Success: true,
		Message: fmt.Sprintf("found %d tasks", len(tasks)),
		Data:    tasks,
	}
}

func (ss *SocketServer) handleTaskStatus(taskID string) Response {
	if ss.server.evolution == nil {
		return Response{Success: false, Message: "evolution engine not available"}
	}
	queue := ss.server.evolution.GetTaskQueue()
	task := queue.GetTask(taskID)
	if task == nil {
		return Response{Success: false, Message: "task not found: " + taskID}
	}
	return Response{Success: true, Message: "task found", Data: task}
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
