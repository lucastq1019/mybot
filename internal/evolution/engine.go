package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"mybot/internal/llm"
	"mybot/internal/memory"
	"mybot/internal/scheduler"
)

// AutonomousEvolutionEngine 自主演进引擎
type AutonomousEvolutionEngine struct {
	llmClient     *llm.Client
	memMgr        *memory.MemoryManager
	stateAnalyzer *StateAnalyzer
	executor      *TaskExecutor
	taskQueue     *TaskQueue
	registry      *scheduler.SkillRegistry
	skillsIndex   *scheduler.SkillsIndexLoader
	// decisionHistory 保存最近若干次自主演进决策的对话历史（仅 user/assistant），用于后续请求带上上下文。
	decisionHistory []llm.Message
	enabled       bool
	cycleInterval time.Duration
}

// NewAutonomousEvolutionEngine 创建自主演进引擎
// registry / skillsIndex 用于将真正可执行的技能以 tools 形式暴露给 LLM。
func NewAutonomousEvolutionEngine(memMgr *memory.MemoryManager, registry *scheduler.SkillRegistry, skillsIndex *scheduler.SkillsIndexLoader) (*AutonomousEvolutionEngine, error) {
	// 按“自主演进”角色创建 LLM 客户端
	llmClient, err := llm.NewClientForRole(llm.RoleEvolution)
	if err != nil {
		log.Printf("Warning: failed to create LLM client for evolution role: %v", err)
	}

	// 在自主演进引擎中，LLM 是硬依赖，缺失时直接 panic，避免静默退化。
	if llmClient == nil {
		panic("AutonomousEvolutionEngine: LLM client is required but not available. Please configure LLM in config or environment.")
	}

	stateAnalyzer := NewStateAnalyzer(memMgr)
	executor, err := NewTaskExecutor(memMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to create task executor: %w", err)
	}

	taskQueue := NewTaskQueue()

	return &AutonomousEvolutionEngine{
		llmClient:     llmClient,
		memMgr:        memMgr,
		stateAnalyzer: stateAnalyzer,
		executor:      executor,
		taskQueue:     taskQueue,
		registry:      registry,
		skillsIndex:   skillsIndex,
		enabled:       true,
		cycleInterval: 1 * time.Hour, // 默认每小时执行一次
	}, nil
}

// Start 启动自主演进循环（后台 goroutine）
func (e *AutonomousEvolutionEngine) Start(ctx context.Context) {
	if !e.enabled {
		log.Println("Autonomous evolution is disabled")
		return
	}

	log.Println("Starting autonomous evolution engine...")

	go func() {
		ticker := time.NewTicker(e.cycleInterval)
		defer ticker.Stop()

		// 立即执行一次
		if err := e.ExecuteAutonomousCycle(ctx); err != nil {
			log.Printf("Error in initial evolution cycle: %v", err)
		}

		// 启动任务队列处理循环（每 30 秒检查一次）
		taskTicker := time.NewTicker(30 * time.Second)
		defer taskTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Println("Autonomous evolution engine stopped")
				return
			case <-ticker.C:
				if err := e.ExecuteAutonomousCycle(ctx); err != nil {
					log.Printf("Error in evolution cycle: %v", err)
				}
			case <-taskTicker.C:
				// 处理队列中的任务
				e.processTaskQueue(ctx)
			}
		}
	}()
}

// ExecuteAutonomousCycle 执行一个自主演进循环
func (e *AutonomousEvolutionEngine) ExecuteAutonomousCycle(ctx context.Context) error {
	log.Println("=== Starting autonomous evolution cycle ===")

	// 1. 分析当前状态
	state, err := e.stateAnalyzer.Analyze()
	if err != nil {
		return fmt.Errorf("failed to analyze state: %w", err)
	}
	log.Printf("State analyzed: archive=%d files, index=%d entries", 
		state.MemoryState.ArchiveFileCount, state.MemoryState.IndexEntryCount)

	// 2. LLM 决策下一步行动
	plan, err := e.DecideNextAction(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to decide next action: %w", err)
	}
	log.Printf("Decision made: action=%s, reason=%s", plan.Action, plan.Reason)

	// 3. 记录决策到记忆
	if err := e.recordDecision(plan); err != nil {
		log.Printf("Warning: failed to record decision: %v", err)
	}

	// 4. 生成任务
	task := NewTask(plan)
	log.Printf("Task created: id=%s, type=%s", task.ID, task.Type)

	// 5. 执行任务
	result, err := e.executor.Execute(ctx, task)
	if err != nil {
		log.Printf("Task execution failed: %v", err)
		e.recordFailure(task, err)
		return err
	}

	log.Printf("Task executed successfully: %s", result.Output)

	// 6. 学习改进
	if err := e.learnFromResult(task, result); err != nil {
		log.Printf("Warning: failed to learn from result: %v", err)
	}

	log.Println("=== Evolution cycle completed ===")
	return nil
}

// DecideNextAction 由 LLM 决定下一步行动
func (e *AutonomousEvolutionEngine) DecideNextAction(ctx context.Context, state *SystemState) (*ActionPlan, error) {
	if e.llmClient == nil {
		panic("DecideNextAction: LLM client is nil")
	}

	// 构建决策 Prompt
	prompt := e.buildDecisionPrompt(state)

	// 为决策阶段构造可执行技能对应的 tools，让 LLM 知道有哪些 server-side 能力可调度。
	tools := e.buildDecisionTools()

	// 构造带上下文的消息：先带上历史对话（若有），再加本轮决策的 system+user。
	messages := make([]llm.Message, 0, len(e.decisionHistory)+2)
	messages = append(messages, e.decisionHistory...)
	currentUser := llm.Message{Role: "user", Content: prompt}
	messages = append(messages,
		llm.Message{Role: "system", Content: "你是一个自主演进的记忆系统。请分析当前状态，决定下一步应该执行什么行动。返回 JSON 格式的 ActionPlan。"},
		currentUser,
	)

	response, toolCalls, err := e.llmClient.ChatWithTools(messages, tools, "auto", 0, 0)
	if err != nil {
		log.Printf("LLM decision failed: %v, using fallback", err)
		return e.fallbackDecision(state), nil
	}

	// 如果 LLM 在决策阶段触发了工具调用，这里立即执行对应的技能。
	if len(toolCalls) > 0 {
		log.Printf("LLM suggested %d tool calls in decision stage", len(toolCalls))
		e.executeToolCalls(ctx, toolCalls)
	}

	// 解析 JSON 响应
	plan := &ActionPlan{}
	if err := e.parseActionPlan(response, plan); err != nil {
		log.Printf("Failed to parse LLM response: %v, using fallback", err)
		return e.fallbackDecision(state), nil
	}

	// 将本轮 user/assistant 对话追加进历史，供下一次决策时带上上下文。
	e.appendDecisionHistory(currentUser, llm.Message{Role: "assistant", Content: response})

	return plan, nil
}

// appendDecisionHistory 维护一个有限长度的决策对话历史，避免无限增长。
func (e *AutonomousEvolutionEngine) appendDecisionHistory(user, assistant llm.Message) {
	const maxMessages = 20
	e.decisionHistory = append(e.decisionHistory, user, assistant)
	if len(e.decisionHistory) > maxMessages {
		e.decisionHistory = e.decisionHistory[len(e.decisionHistory)-maxMessages:]
	}
}

// buildDecisionPrompt 构建决策 Prompt
func (e *AutonomousEvolutionEngine) buildDecisionPrompt(state *SystemState) string {
	var sb strings.Builder

	sb.WriteString("当前状态：\n")
	sb.WriteString(fmt.Sprintf("- 记忆状态：archive %d 个文件，总大小 %d 字节，索引 %d 条条目\n", 
		state.MemoryState.ArchiveFileCount, state.MemoryState.ArchiveTotalSize, state.MemoryState.IndexEntryCount))
	
	if state.MemoryState.NeedsSummarize {
		sb.WriteString(fmt.Sprintf("- 需要压缩：%s\n", state.MemoryState.SummarizeReason))
	}

	sb.WriteString(fmt.Sprintf("- 任务状态：成功率 %.2f%%，最近任务时间 %s\n", 
		state.TaskState.SuccessRate*100, state.TaskState.LastTaskTime))
	sb.WriteString(fmt.Sprintf("- 演进状态：已掌握 %d 项能力，最后演进时间 %s\n", 
		len(state.EvolutionState.Capabilities), state.EvolutionState.LastEvolution))

	sb.WriteString("\n可用行动：\n")
	sb.WriteString("1. summarize - 压缩 archive，释放空间\n")
	sb.WriteString("2. consolidate - 固化新记忆\n")
	sb.WriteString("3. recall - 检索相关记忆\n")
	sb.WriteString("4. learn - 学习新能力\n")
	sb.WriteString("5. optimize - 优化索引或检索策略\n")
	sb.WriteString("6. reflect - 反思和改进\n")
	sb.WriteString("7. idle - 暂不行动\n")

	sb.WriteString("\n请以 JSON 格式返回决策：\n")
	sb.WriteString(`{
  "action": "行动类型",
  "reason": "为什么选择这个行动",
  "steps": ["步骤1", "步骤2", ...],
  "expected_outcome": "预期结果",
  "priority": 优先级(1-10)
}`)

	return sb.String()
}

// parseActionPlan 解析 ActionPlan（从 LLM 响应中提取 JSON）
func (e *AutonomousEvolutionEngine) parseActionPlan(response string, plan *ActionPlan) error {
	// 尝试提取 JSON（可能包含 markdown 代码块）
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		response = response[jsonStart : jsonEnd+1]
	}

	return json.Unmarshal([]byte(response), plan)
}

// fallbackDecision 回退决策（LLM 不可用时）
func (e *AutonomousEvolutionEngine) fallbackDecision(state *SystemState) *ActionPlan {
	if state.MemoryState.NeedsSummarize {
		return &ActionPlan{
			Action:         "summarize",
			Reason:         state.MemoryState.SummarizeReason,
			Steps:          []string{"选择需要压缩的文件", "生成摘要", "移动到 backup"},
			ExpectedOutcome: "archive 文件数减少",
			Priority:       8,
		}
	}

	// 默认：idle
	return &ActionPlan{
		Action:         "idle",
		Reason:         "当前状态良好，无需立即行动",
		Steps:          []string{},
		ExpectedOutcome: "保持当前状态",
		Priority:       1,
	}
}

// recordDecision 记录决策到记忆
func (e *AutonomousEvolutionEngine) recordDecision(plan *ActionPlan) error {
	logFile := EvolutionLogFilePath

	// 读取现有日志
	log := EvolutionLog{Entries: []EvolutionLogEntry{}}
	if data, err := os.ReadFile(logFile); err == nil {
		json.Unmarshal(data, &log)
	}

	// 添加新条目
	entry := EvolutionLogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		TaskID:    "", // 将在任务创建后更新
		Action:    plan.Action,
		Decision:  plan.Reason,
		Status:    "pending",
		NextSteps: plan.Steps,
	}

	log.Entries = append(log.Entries, entry)

	// 保存日志
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(logFile, data, 0644)
}

// recordFailure 记录失败
func (e *AutonomousEvolutionEngine) recordFailure(task *Task, err error) {
	logFile := EvolutionLogFilePath

	log := EvolutionLog{Entries: []EvolutionLogEntry{}}
	if data, readErr := os.ReadFile(logFile); readErr == nil {
		json.Unmarshal(data, &log)
	}

	// 更新最后一条记录
	if len(log.Entries) > 0 {
		lastIdx := len(log.Entries) - 1
		log.Entries[lastIdx].TaskID = task.ID
		log.Entries[lastIdx].Status = "failed"
		log.Entries[lastIdx].Result = err.Error()
		log.Entries[lastIdx].CompletedAt = time.Now().Format(time.RFC3339)
	}

	data, _ := json.MarshalIndent(log, "", "  ")
	os.WriteFile(logFile, data, 0644)
}

// learnFromResult 从结果中学习
func (e *AutonomousEvolutionEngine) learnFromResult(task *Task, result *TaskResult) error {
	logFile := EvolutionLogFilePath

	log := EvolutionLog{Entries: []EvolutionLogEntry{}}
	if data, err := os.ReadFile(logFile); err == nil {
		json.Unmarshal(data, &log)
	}

	// 更新最后一条记录
	if len(log.Entries) > 0 {
		lastIdx := len(log.Entries) - 1
		log.Entries[lastIdx].TaskID = task.ID
		log.Entries[lastIdx].Status = "completed"
		log.Entries[lastIdx].Result = result.Output
		log.Entries[lastIdx].Learning = result.Learning
		log.Entries[lastIdx].CompletedAt = time.Now().Format(time.RFC3339)
	}

	// 保存日志
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(logFile, data, 0644)
}

// SetEnabled 设置是否启用自主演进
func (e *AutonomousEvolutionEngine) SetEnabled(enabled bool) {
	e.enabled = enabled
}

// SetCycleInterval 设置循环间隔
func (e *AutonomousEvolutionEngine) SetCycleInterval(interval time.Duration) {
	e.cycleInterval = interval
}

// ExecuteTask 立即执行一个任务（供外部调用，同步执行）
func (e *AutonomousEvolutionEngine) ExecuteTask(ctx context.Context, actionPlan *ActionPlan) (*TaskResult, error) {
	// 生成任务
	task := NewTask(actionPlan)
	log.Printf("Manual task created: id=%s, type=%s", task.ID, task.Type)

	// 执行任务
	result, err := e.executor.Execute(ctx, task)
	if err != nil {
		log.Printf("Manual task execution failed: %v", err)
		e.recordFailure(task, err)
		return nil, err
	}

	log.Printf("Manual task executed successfully: %s", result.Output)

	// 学习改进
	if err := e.learnFromResult(task, result); err != nil {
		log.Printf("Warning: failed to learn from manual task result: %v", err)
	}

	return result, nil
}

// EnqueueTask 将任务加入队列（供外部调用，异步执行）
func (e *AutonomousEvolutionEngine) EnqueueTask(actionPlan *ActionPlan, createdBy string) (*QueuedTask, error) {
	return e.taskQueue.Enqueue(actionPlan, createdBy)
}

// processTaskQueue 处理任务队列中的任务
func (e *AutonomousEvolutionEngine) processTaskQueue(ctx context.Context) {
	// 取出一个待执行的任务
	queuedTask := e.taskQueue.Dequeue()
	if queuedTask == nil {
		return // 没有待执行的任务
	}

	log.Printf("Processing queued task: id=%s, type=%s", queuedTask.ID, queuedTask.Type)

	// 转换为 Task 并执行
	task := &Task{
		ID:         queuedTask.ID,
		Type:       queuedTask.Type,
		ActionPlan: queuedTask.ActionPlan,
		Params:     queuedTask.Params,
		Priority:   queuedTask.Priority,
		Status:     queuedTask.Status,
		CreatedAt:  queuedTask.CreatedAt,
	}

	result, err := e.executor.Execute(ctx, task)
	if err != nil {
		log.Printf("Queued task execution failed: %v", err)
		e.taskQueue.UpdateTask(queuedTask.ID, "failed", &TaskResult{
			Success: false,
			Error:   err.Error(),
		})
		e.recordFailure(task, err)
		return
	}

	log.Printf("Queued task executed successfully: %s", result.Output)

	// 更新任务状态
	e.taskQueue.UpdateTask(queuedTask.ID, "completed", result)

	// 学习改进
	if err := e.learnFromResult(task, result); err != nil {
		log.Printf("Warning: failed to learn from queued task result: %v", err)
	}
}

// GetTaskQueue 获取任务队列（供外部查询）
func (e *AutonomousEvolutionEngine) GetTaskQueue() *TaskQueue {
	return e.taskQueue
}

// buildDecisionTools 根据已注册的可执行技能构造 LLM tools 列表。
// 这里只暴露「server 侧真正可执行」的技能：即已经在 SkillRegistry 中注册的技能，
// 并尝试从 skills-index.json 中补充描述信息。
func (e *AutonomousEvolutionEngine) buildDecisionTools() []llm.Tool {
	if e.registry == nil {
		return nil
	}

	skills := e.registry.List()
	if len(skills) == 0 {
		return nil
	}

	var tools []llm.Tool

	for _, s := range skills {
		name := s.Name()
		if name == "" {
			continue
		}

		// 从 skills-index.json 中读取 description（如有）
		var desc string
		if e.skillsIndex != nil {
			if meta, err := e.skillsIndex.SkillByName(name); err == nil && meta != nil {
				desc = meta.Description
			}
		}
		if desc == "" {
			desc = fmt.Sprintf("Server-side skill %s, executable by scheduler", name)
		}

		// 先暴露一个「无参数」的函数 schema；后续如需参数可扩展。
		schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				// 避免与其它 function 命名冲突，加一个前缀
				Name:        "skill_" + name,
				Description: desc,
				Parameters:  schema,
			},
		})
	}

	return tools
}

// executeToolCalls 根据 LLM 返回的 toolCalls 实际执行对应的 server 侧技能。
// 当前约定：由 buildDecisionTools 暴露的函数名形如 "skill_<技能名>"，
// 这里会解析出技能名，并通过 SkillRegistry 找到并调用对应的 Skill.Run。
// arguments JSON 中如包含 "args": ["--flag", "value"]，则作为 Run 的参数传入。
func (e *AutonomousEvolutionEngine) executeToolCalls(ctx context.Context, toolCalls []llm.ToolCall) {
	if e.registry == nil || len(toolCalls) == 0 {
		return
	}

	for _, tc := range toolCalls {
		fn := tc.Function
		name := fn.Name
		if !strings.HasPrefix(name, "skill_") {
			log.Printf("Skipping tool call %s: unsupported function name=%s", tc.ID, name)
			continue
		}

		skillName := strings.TrimPrefix(name, "skill_")
		skill, ok := e.registry.Get(skillName)
		if !ok {
			log.Printf("Skill %s not found for tool call %s", skillName, tc.ID)
			continue
		}

		var args []string
		if fn.Arguments != "" && fn.Arguments != "null" {
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(fn.Arguments), &raw); err != nil {
				log.Printf("Failed to parse arguments for tool call %s (skill=%s): %v", tc.ID, skillName, err)
			} else if v, ok := raw["args"]; ok {
				if arr, ok := v.([]interface{}); ok {
					for _, item := range arr {
						if s, ok := item.(string); ok {
							args = append(args, s)
						}
					}
				}
			}
		}

		log.Printf("Executing skill tool call: id=%s, skill=%s, args=%v", tc.ID, skillName, args)
		if err := skill.Run(ctx, args); err != nil {
			log.Printf("Skill %s execution failed for tool call %s: %v", skillName, tc.ID, err)
		}
	}
}
