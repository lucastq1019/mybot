package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mybot/internal/brain"
	"mybot/internal/config"
	"mybot/internal/evolution"
	"mybot/internal/memory"
	"mybot/internal/scheduler"
)

// Server 常驻进程服务器。路径与技能索引与 brain/core.md 一致（见 internal/brain/paths.go）。
// 可执行技能为 .so 插件与内置技能；MD 技能通过 socket 的 skill_get 提供 SKILL.md 内容，由 Agent 按 core 规则执行，避免 brain 与代码分歧导致无效演进。
type Server struct {
	memMgr      *memory.MemoryManager
	sched       *scheduler.Scheduler
	registry    *scheduler.SkillRegistry
	skillsIndex *scheduler.SkillsIndexLoader
	socketSrv   *SocketServer
	evolution   *evolution.AutonomousEvolutionEngine
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewServer 创建新的服务器实例
func NewServer() (*Server, error) {
	// 创建 MemoryManager
	memMgr, err := memory.NewMemoryManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create MemoryManager: %w", err)
	}

	// 创建技能注册表
	registry, err := scheduler.NewSkillRegistry()
	if err != nil {
		return nil, fmt.Errorf("failed to create skill registry: %w", err)
	}

	// 创建调度器
	sched := scheduler.NewScheduler(registry)

	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		memMgr:      memMgr,
		sched:       sched,
		registry:    registry,
		skillsIndex: scheduler.NewSkillsIndexLoader(),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// Start 启动服务器（启动流程完整链路）
func (s *Server) Start() error {
	log.Println("Starting Cata server...")

	if err := s.startCorePipeline(); err != nil {
		s.shutdownPartialStart()
		return err
	}

	// 6. 设置信号处理（优雅退出）
	s.setupSignalHandling()

	log.Println("Cata server started successfully!")
	return nil
}

// startCorePipeline 运行服务启动的核心流程。
// 该流程按「记忆 -> 技能 -> 调度 -> 通信 -> 演进」顺序启动，任何关键阶段失败都会中断启动。
func (s *Server) startCorePipeline() error {

	// 1. MemoryManager 已在 NewServer 中创建并加载索引
	log.Println("✓ MemoryManager initialized")

	// 2. 加载 Skills（路径与 brain/core.md 技能目录、技能索引一致）
	skillsDir := brain.SkillsDir()
	loader := scheduler.NewSkillLoader(skillsDir, s.registry)
	if err := loader.LoadSkills(); err != nil {
		log.Printf("Warning: failed to load skills: %v", err)
	}

	// 加载 skills-index.json，与 core.md「从 skills-index 解析」对齐
	if _, err := s.skillsIndex.Load(); err != nil {
		log.Printf("Warning: failed to load skills index: %v", err)
	}

	// 注册内置技能（对应 workflow 定时/阈值任务：consolidate、summarize）
	if err := s.loadBuiltinSkills(); err != nil {
		log.Printf("Warning: failed to load builtin skills: %v", err)
	}

	skillCount := len(s.registry.List())
	log.Printf("✓ Skills loaded (%d skills)", skillCount)

	// 3. 注册定时任务
	if err := s.sched.Start(); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	log.Println("✓ Scheduler started")

	// 4. 启动 socket 服务器（用于客户端通信）
	socketSrv, err := NewSocketServer(s)
	if err != nil {
		return fmt.Errorf("failed to create socket server: %w", err)
	}
	s.socketSrv = socketSrv
	socketSrv.Start()
	log.Println("✓ Socket server started")

	// 5. 初始化并启动自主演进引擎（可通过配置开关禁用）
	if config.Config != nil && !config.Config.Evolution.Enabled {
		log.Println("- Evolution engine disabled by configuration")
		return nil
	}

	evolutionEngine, err := evolution.NewAutonomousEvolutionEngine(s.memMgr, s.registry, s.skillsIndex)
	if err != nil {
		log.Printf("Warning: failed to create evolution engine: %v", err)
	} else {
		s.evolution = evolutionEngine
		evolutionEngine.Start(s.ctx)
		log.Println("✓ Autonomous evolution engine started")
	}

	return nil
}

// shutdownPartialStart 在启动流程中途失败时清理已启动的组件，避免遗留后台任务。
func (s *Server) shutdownPartialStart() {
	if s.evolution != nil {
		s.evolution.SetEnabled(false)
		s.evolution = nil
	}

	if s.socketSrv != nil {
		s.socketSrv.Stop()
		s.socketSrv = nil
	}

	if s.sched != nil {
		s.sched.Stop()
	}
}

// setupSignalHandling 设置信号处理（SIGTERM, SIGINT）
func (s *Server) setupSignalHandling() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal: %v, initiating graceful shutdown...", sig)
		s.Stop()
	}()
}

// Stop 优雅停止服务器
func (s *Server) Stop() {
	log.Println("Initiating graceful shutdown...")

	s.cancel()

	if s.evolution != nil {
		s.evolution.SetEnabled(false)
		log.Println("Evolution engine stopped")
	}

	if s.socketSrv != nil {
		s.socketSrv.Stop()
	}

	s.sched.Stop()

	time.Sleep(100 * time.Millisecond)

	log.Println("Server stopped gracefully")
	os.Exit(0)
}

// Wait 等待服务器停止（阻塞）
func (s *Server) Wait() {
	<-s.ctx.Done()
}

// GetMemoryManager 获取 MemoryManager（供 Skills 使用）
func (s *Server) GetMemoryManager() *memory.MemoryManager {
	return s.memMgr
}

// GetRegistry 获取技能注册表
func (s *Server) GetRegistry() *scheduler.SkillRegistry {
	return s.registry
}

// GetScheduler 获取调度器
func (s *Server) GetScheduler() *scheduler.Scheduler {
	return s.sched
}

// GetEvolutionEngine 获取自主演进引擎
func (s *Server) GetEvolutionEngine() *evolution.AutonomousEvolutionEngine {
	return s.evolution
}

// GetSkillsIndexLoader 获取技能索引加载器（与 brain/core.md skills-index 一致）
func (s *Server) GetSkillsIndexLoader() *scheduler.SkillsIndexLoader {
	return s.skillsIndex
}

// loadBuiltinSkills 加载内置技能
func (s *Server) loadBuiltinSkills() error {
	// 注册今日固化技能
	dailySkill := scheduler.NewDailyConsolidateSkill(s.memMgr)
	if err := s.registry.Register(dailySkill); err != nil {
		return fmt.Errorf("failed to register daily consolidate skill: %w", err)
	}

	// 注册周期摘要技能
	summarizeSkill := scheduler.NewPeriodicSummarizeSkill(s.memMgr)
	if err := s.registry.Register(summarizeSkill); err != nil {
		return fmt.Errorf("failed to register periodic summarize skill: %w", err)
	}

	return nil
}
