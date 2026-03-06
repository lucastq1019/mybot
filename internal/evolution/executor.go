package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"mybot/internal/brain"
	"mybot/internal/llm"
	"mybot/internal/memory"
)

// TaskExecutor 任务执行器
type TaskExecutor struct {
	memMgr   *memory.MemoryManager
	llmClient *llm.Client
}

// NewTaskExecutor 创建任务执行器
func NewTaskExecutor(memMgr *memory.MemoryManager) (*TaskExecutor, error) {
	// 按“自主演进/任务执行”角色创建 LLM 客户端
	llmClient, err := llm.NewClientForRole(llm.RoleEvolution)
	if err != nil {
		log.Printf("Warning: failed to create LLM client for evolution role in TaskExecutor: %v", err)
	}

	if llmClient == nil {
		panic("TaskExecutor: LLM client is required but not available. Please configure LLM in config or environment.")
	}

	return &TaskExecutor{
		memMgr:   memMgr,
		llmClient: llmClient,
	}, nil
}

// Execute 执行任务
func (te *TaskExecutor) Execute(ctx context.Context, task *Task) (*TaskResult, error) {
	task.Status = "running"
	task.StartedAt = task.CreatedAt // 简化处理

	log.Printf("Executing task: %s (type: %s)", task.ID, task.Type)

	var result *TaskResult
	var err error

	switch task.Type {
	case "summarize":
		result, err = te.executeSummarize(ctx, task)
	case "consolidate":
		result, err = te.executeConsolidate(ctx, task)
	case "recall":
		result, err = te.executeRecall(ctx, task)
	case "learn":
		result, err = te.executeLearn(ctx, task)
	case "optimize":
		result, err = te.executeOptimize(ctx, task)
	case "reflect":
		result, err = te.executeReflect(ctx, task)
	case "idle":
		result, err = te.executeIdle(ctx, task)
	case "integrate":
		result, err = te.executeIntegrate(ctx, task)
	case "custom":
		result, err = te.executeCustom(ctx, task)
	default:
		return nil, fmt.Errorf("unknown task type: %s", task.Type)
	}

	if err != nil {
		task.Status = "failed"
		task.Result = &TaskResult{
			Success: false,
			Error:   err.Error(),
		}
		return task.Result, err
	}

	task.Status = "completed"
	task.CompletedAt = result.Metrics["completed_at"].(string)
	task.Result = result

	log.Printf("Task completed: %s (success: %v)", task.ID, result.Success)
	return result, nil
}

// executeSummarize 执行压缩任务
func (te *TaskExecutor) executeSummarize(ctx context.Context, task *Task) (*TaskResult, error) {
	if err := te.memMgr.SummarizeAndRotate(); err != nil {
		return &TaskResult{
			Success: false,
			Error:   err.Error(),
			Learning: "SummarizeAndRotate 失败，需要检查 LLM 配置和 archive 文件状态",
		}, err
	}

	return &TaskResult{
		Success: true,
		Output:  "Archive files summarized successfully",
		Metrics: map[string]interface{}{
			"completed_at": task.CreatedAt,
		},
		Learning: "成功执行 SummarizeAndRotate，archive 文件已压缩并移动到 backup 目录",
	}, nil
}

// executeConsolidate 执行固化任务
func (te *TaskExecutor) executeConsolidate(ctx context.Context, task *Task) (*TaskResult, error) {
	// 从 ActionPlan.steps 提取 topic 和 content
	if len(task.ActionPlan.Steps) < 2 {
		return &TaskResult{
			Success: false,
			Error:   "Consolidate task requires topic and content in steps",
		}, fmt.Errorf("invalid consolidate task: missing topic or content")
	}

	topic := task.ActionPlan.Steps[0]
	content := task.ActionPlan.Steps[1]

	if err := te.memMgr.Consolidate(topic, content); err != nil {
		return &TaskResult{
			Success: false,
			Error:   err.Error(),
			Learning: "Consolidate 失败，需要检查文件写入权限和目录结构",
		}, err
	}

	return &TaskResult{
		Success: true,
		Output:  fmt.Sprintf("Content consolidated: topic=%s", topic),
		Metrics: map[string]interface{}{
			"topic":        topic,
			"completed_at": task.CreatedAt,
		},
		Learning: fmt.Sprintf("成功固化记忆：%s", topic),
	}, nil
}

// executeRecall 执行检索任务
func (te *TaskExecutor) executeRecall(ctx context.Context, task *Task) (*TaskResult, error) {
	if len(task.ActionPlan.Steps) < 1 {
		return &TaskResult{
			Success: false,
			Error:   "Recall task requires query in steps",
		}, fmt.Errorf("invalid recall task: missing query")
	}

	query := task.ActionPlan.Steps[0]
	topK := 5
	if len(task.ActionPlan.Steps) > 1 {
		// 可以解析 topK
	}

	results, err := te.memMgr.RecallWithPreprocess(query, topK, true)
	if err != nil {
		return &TaskResult{
			Success: false,
			Error:   err.Error(),
		}, err
	}

	return &TaskResult{
		Success: true,
		Output:  fmt.Sprintf("Found %d results for query: %s", len(results), query),
		Metrics: map[string]interface{}{
			"query":        query,
			"result_count": len(results),
			"completed_at": task.CreatedAt,
		},
		Learning: fmt.Sprintf("检索到 %d 条相关记忆", len(results)),
	}, nil
}

// executeLearn 执行学习任务
func (te *TaskExecutor) executeLearn(ctx context.Context, task *Task) (*TaskResult, error) {
	if te.llmClient == nil {
		panic("executeLearn: LLM client is nil")
	}

	// 1. 确定本次学习的需求描述（优先使用 steps[0]，否则用 reason）
	requirement := ""
	if len(task.ActionPlan.Steps) > 0 {
		requirement = strings.TrimSpace(strings.Join(task.ActionPlan.Steps, "；"))
	}
	if requirement == "" {
		requirement = strings.TrimSpace(task.ActionPlan.Reason)
	}
	if requirement == "" {
		requirement = "根据当前 brain 与记忆，对 hot / short / long memory 做一次自我学习和整理。"
	}

	// 2. 构建集成上下文：复用 integrate 的能力，生成 integrated system prompt 文件并读取内容
	integratedPath, _, err := memory.BuildIntegratedSystemPrompt()
	if err != nil {
		return &TaskResult{
			Success:  false,
			Error:    fmt.Sprintf("failed to build integrated system prompt for learn: %v", err),
			Learning: "学习失败：无法构建集成上下文，需检查 brain 目录与权限",
		}, err
	}

	integratedContentBytes, err := os.ReadFile(integratedPath)
	if err != nil {
		return &TaskResult{
			Success:  false,
			Error:    fmt.Sprintf("failed to read integrated system prompt: %v", err),
			Learning: "学习失败：无法读取集成上下文文件",
		}, err
	}
	integratedContent := string(integratedContentBytes)

	// 为避免 prompt 过大，必要时截断
	const maxContextRunes = 8000
	if len([]rune(integratedContent)) > maxContextRunes {
		runes := []rune(integratedContent)
		integratedContent = string(runes[:maxContextRunes]) + "\n\n...[内容截断，仅保留前 8000 字符]..."
	}

	// 3. 让 LLM 基于集成上下文与需求，产出一份可执行的记忆更新计划（JSON）
	type learnUpdate struct {
		Topic   string `json:"topic"`
		Content string `json:"content"`
	}
	type learnPlan struct {
		HotUpdates   []learnUpdate `json:"hot_updates"`
		ShortUpdates []string      `json:"short_updates"`
		LongUpdates  []learnUpdate `json:"long_updates"`
	}

	systemPrompt := "你是一套自主演进的记忆系统，负责根据 brain 与各类记忆（hot / short / long）生成具体的更新计划。只输出 JSON，不要任何解释。"
	userPrompt := fmt.Sprintf(`学习需求：
%s

下面是当前 brain 与记忆的集成视图（节选）：
----------------
%s
----------------

请根据以上信息，输出一个 JSON 对象，字段说明：
- hot_updates: 数组，每项包含 { "topic": "写入 hot 的主题，如『更新 hot memory：开发偏好』", "content": "要写入的 Markdown 内容" }
- short_updates: 数组，每项是要追加到短期记忆 current_session.md 的 Markdown 段落
- long_updates: 数组，每项包含 { "topic": "写入 long-term 的主题，如『项目知识：Cata 演进』", "content": "要写入的 Markdown 内容" }

返回格式示例（不要换行注释，不要多余字段）：
{
  "hot_updates": [
    {"topic": "更新 hot memory：身份与偏好", "content": "具体内容..."}
  ],
  "short_updates": [
    "本次学习的关键决策与收获..."
  ],
  "long_updates": [
    {"topic": "项目知识：自主演进路径", "content": "具体内容..."}
  ]
}`, requirement, integratedContent)

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	resp, err := te.llmClient.Chat(messages)
	if err != nil {
		return &TaskResult{
			Success: false,
			Error:   fmt.Sprintf("learn LLM call failed: %v", err),
		}, err
	}

	// 提取 JSON
	jsonStr := resp
	if i := strings.Index(resp, "{"); i >= 0 {
		if j := strings.LastIndex(resp, "}"); j > i {
			jsonStr = resp[i : j+1]
		}
	}

	var plan learnPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return &TaskResult{
			Success: false,
			Error:   fmt.Sprintf("learn plan parse failed: %v", err),
		}, err
	}

	// 4. 根据计划实际更新 hot / short / long memory
	appliedHot := 0
	appliedShort := 0
	appliedLong := 0

	// 4.1 hot 与 long 使用 MemoryManager.Consolidate（由 topic 决定写入目标）
	for _, u := range plan.HotUpdates {
		if strings.TrimSpace(u.Topic) == "" || strings.TrimSpace(u.Content) == "" {
			continue
		}
		if err := te.memMgr.Consolidate(u.Topic, u.Content); err != nil {
			log.Printf("Learn task: failed to consolidate hot update (%s): %v", u.Topic, err)
			continue
		}
		appliedHot++
	}
	for _, u := range plan.LongUpdates {
		if strings.TrimSpace(u.Topic) == "" || strings.TrimSpace(u.Content) == "" {
			continue
		}
		if err := te.memMgr.Consolidate(u.Topic, u.Content); err != nil {
			log.Printf("Learn task: failed to consolidate long update (%s): %v", u.Topic, err)
			continue
		}
		appliedLong++
	}

	// 4.2 short 直接追加到 current_session.md
	if len(plan.ShortUpdates) > 0 {
		shortPath := brain.ShortTermCurrentPath()
		existing, _ := os.ReadFile(shortPath)
		var builder strings.Builder
		builder.Write(existing)
		for _, s := range plan.ShortUpdates {
			text := strings.TrimSpace(s)
			if text == "" {
				continue
			}
			appliedShort++
			builder.WriteString("\n\n## Learn 更新（")
			builder.WriteString(time.Now().Format("2006-01-02 15:04:05"))
			builder.WriteString(")\n\n")
			builder.WriteString(text)
			builder.WriteString("\n")
		}
		if appliedShort > 0 {
			if err := os.WriteFile(shortPath, []byte(builder.String()), 0644); err != nil {
				log.Printf("Learn task: failed to write short-term memory: %v", err)
			}
		}
	}

	// 5. 汇总结果
	output := fmt.Sprintf("Learn task completed. Applied hot_updates=%d, short_updates=%d, long_updates=%d", appliedHot, appliedShort, appliedLong)
	learning := fmt.Sprintf("根据需求「%s」完成一次基于 brain 集成视图的记忆学习与更新。", requirement)

	return &TaskResult{
		Success: true,
		Output:  output,
		Metrics: map[string]interface{}{
			"completed_at":   time.Now().Format(time.RFC3339),
			"hot_updates":    appliedHot,
			"short_updates":  appliedShort,
			"long_updates":   appliedLong,
			"integratedPath": integratedPath,
		},
		Learning: learning,
	}, nil
}

// executeOptimize 执行优化任务
func (te *TaskExecutor) executeOptimize(ctx context.Context, task *Task) (*TaskResult, error) {
	// TODO: 实现优化索引或检索策略的具体逻辑

	return &TaskResult{
		Success: true,
		Output:  "Optimize task executed (placeholder)",
		Metrics: map[string]interface{}{
			"completed_at": task.CreatedAt,
		},
		Learning: "优化任务执行完成（待实现具体逻辑）",
	}, nil
}

// executeReflect 执行反思任务
func (te *TaskExecutor) executeReflect(ctx context.Context, task *Task) (*TaskResult, error) {
	// TODO: 实现反思和改进的具体逻辑
	// 1. 分析能力边界
	// 2. 识别改进点
	// 3. 生成改进方案
	// 4. 记录到记忆

	return &TaskResult{
		Success: true,
		Output:  "Reflect task executed (placeholder)",
		Metrics: map[string]interface{}{
			"completed_at": task.CreatedAt,
		},
		Learning: "反思任务执行完成（待实现具体逻辑）",
	}, nil
}

// executeIdle 执行空闲任务（当前状态良好，无需行动）
func (te *TaskExecutor) executeIdle(ctx context.Context, task *Task) (*TaskResult, error) {
	// idle 任务不需要执行任何操作，只是记录当前状态良好
	return &TaskResult{
		Success: true,
		Output:  "System state is healthy, no action needed",
		Metrics: map[string]interface{}{
			"completed_at": task.CreatedAt,
		},
		Learning: task.ActionPlan.Reason,
	}, nil
}

// executeIntegrate 执行整合任务：整个 brain 作为 system prompt，将 archive、long、short、graph_memory 整合为一份文档
func (te *TaskExecutor) executeIntegrate(ctx context.Context, task *Task) (*TaskResult, error) {
	outPath, size, err := memory.BuildIntegratedSystemPrompt()
	if err != nil {
		return &TaskResult{
			Success:  false,
			Error:    err.Error(),
			Learning: "整合失败，检查 brain 目录与权限",
		}, err
	}
	return &TaskResult{
		Success: true,
		Output:  fmt.Sprintf("Integrated system prompt written to %s (%d bytes)", outPath, size),
		Metrics: map[string]interface{}{
			"completed_at": time.Now().Format(time.RFC3339),
			"output_path":  outPath,
			"size_bytes":   size,
		},
		Learning: "已将 core、workflow、hot、long-term、short-term、graph_memory、archive 整合为 brain/context/integrated_system_prompt.md",
	}, nil
}

// customActionResponse LLM 返回的 custom 解析结果
type customActionResponse struct {
	Action string   `json:"action"`
	Steps  []string `json:"steps"`
}

// executeCustom 根据用户需求（长串字符串）由 LLM 解析为具体 action+steps 后执行
func (te *TaskExecutor) executeCustom(ctx context.Context, task *Task) (*TaskResult, error) {
	requirement := ""
	if len(task.ActionPlan.Steps) > 0 {
		requirement = strings.TrimSpace(task.ActionPlan.Steps[0])
	}
	if requirement == "" {
		requirement = task.ActionPlan.Reason
	}
	if requirement == "" {
		return &TaskResult{
			Success: false,
			Error:   "custom task requires user requirement in steps or reason",
		}, fmt.Errorf("custom task: missing requirement")
	}

	if te.llmClient == nil {
		panic("executeCustom: LLM client is nil")
	}

	prompt := fmt.Sprintf(`用户通过 catacli 提交了以下需求，请判断应执行哪种任务并只返回一个 JSON 对象，不要其他文字。

用户需求：
%s

可选任务类型：integrate（整合 brain+记忆 为 system prompt）, summarize（压缩 archive）, consolidate（固化记忆）, recall（检索）, learn, optimize, reflect, idle。

返回格式（仅一行 JSON）：
{"action":"类型", "steps":["步骤1","步骤2"]}

若需求涉及「整个 brain、system prompt、整合、archive、long、short、graph_memory、记忆整合」等，选 integrate，steps 可为 []。`, requirement)

	messages := []llm.Message{
		{Role: "system", Content: "你只输出一个 JSON 对象，包含 action 和 steps 两个字段。"},
		{Role: "user", Content: prompt},
	}
	resp, err := te.llmClient.Chat(messages)
	if err != nil {
		return &TaskResult{
			Success: false,
			Error:   fmt.Sprintf("LLM parse failed: %v", err),
		}, err
	}

	// 提取 JSON
	jsonStr := resp
	if i := strings.Index(resp, "{"); i >= 0 {
		if j := strings.LastIndex(resp, "}"); j > i {
			jsonStr = resp[i : j+1]
		}
	}
	var parsed customActionResponse
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		// 回退：需求里含整合相关词则执行 integrate
		if strings.Contains(strings.ToLower(requirement), "整合") || strings.Contains(strings.ToLower(requirement), "system prompt") {
			parsed.Action = "integrate"
			parsed.Steps = []string{}
		} else {
			return &TaskResult{
				Success: false,
				Error:   fmt.Sprintf("LLM response parse failed: %v", err),
			}, err
		}
	}

	action := strings.TrimSpace(strings.ToLower(parsed.Action))
	if action == "" {
		action = "idle"
	}
	innerPlan := &ActionPlan{
		Action: action,
		Reason: requirement,
		Steps:  parsed.Steps,
	}
	innerTask := &Task{
		Type:       action,
		ActionPlan: innerPlan,
		CreatedAt:  task.CreatedAt,
	}

	switch action {
	case "integrate":
		return te.executeIntegrate(ctx, innerTask)
	case "summarize":
		return te.executeSummarize(ctx, innerTask)
	case "consolidate":
		return te.executeConsolidate(ctx, innerTask)
	case "recall":
		return te.executeRecall(ctx, innerTask)
	case "learn":
		return te.executeLearn(ctx, innerTask)
	case "optimize":
		return te.executeOptimize(ctx, innerTask)
	case "reflect":
		return te.executeReflect(ctx, innerTask)
	default:
		return te.executeIdle(ctx, innerTask)
	}
}
