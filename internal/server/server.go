package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mybot/internal/evolution"
	"mybot/internal/memory"
	"mybot/internal/scheduler"
)

const (
	logServerStart = "Starting Cata server..."
	logServerReady = "Cata server started successfully!"
)

// Server 常驻进程服务器（精简版）。
// 仅保留核心流程依赖：MemoryManager + Socket 命令交互。
type Server struct {
	memMgr    *memory.MemoryManager
	socketSrv *SocketServer
	evolution *evolution.AutonomousEvolutionEngine
	registry  *scheduler.SkillRegistry
	skillsIdx *scheduler.SkillsIndexLoader
	ctx       context.Context
	cancel    context.CancelFunc
}

// NewServer 创建新的服务器实例
func NewServer() (*Server, error) {
	// 创建 MemoryManager
	memMgr, err := memory.NewMemoryManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create MemoryManager: %w", err)
	}

	registry, err := scheduler.NewSkillRegistry()
	if err != nil {
		return nil, fmt.Errorf("failed to create skill registry: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		memMgr: memMgr,
		registry: registry,
		skillsIdx: scheduler.NewSkillsIndexLoader(),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Start 启动服务器（核心链路：memory + evolution + socket）
func (s *Server) Start() error {
	log.Println(logServerStart)

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

	// 2. 接回最小可用 tool registry（不启用 scheduler，仅供 evolution 的 ReAct 工具调用）
	if err := s.loadMinimalToolRegistry(); err != nil {
		log.Printf("Warning: failed to load minimal tool registry: %v", err)
	} else {
		log.Printf("✓ Tool registry initialized (%d skills)", len(s.registry.List()))
	}
	if _, err := s.skillsIdx.Load(); err != nil {
		log.Printf("Warning: failed to load skills index: %v", err)
	}

	// 2. 启动自主演进引擎（保留核心）
	evolutionEngine, err := evolution.NewAutonomousEvolutionEngine(s.memMgr, s.registry, s.skillsIdx)
	if err != nil {
		log.Printf("Warning: failed to create evolution engine: %v", err)
	} else {
		s.evolution = evolutionEngine
		evolutionEngine.Start(s.ctx)
		log.Println("✓ Autonomous evolution engine started")
	}

	// 3. 启动 socket 服务器（用于客户端通信）
	socketSrv, err := NewSocketServer(s)
	if err != nil {
		return fmt.Errorf("failed to create socket server: %w", err)
	}
	s.socketSrv = socketSrv
	socketSrv.Start()
	log.Println("✓ Socket server started")

	// 4. 设置信号处理（优雅退出）
	s.setupSignalHandling()

	log.Println(logServerReady)
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

// GetEvolutionEngine 获取自主演进引擎
func (s *Server) GetEvolutionEngine() *evolution.AutonomousEvolutionEngine {
	return s.evolution
}

// loadMinimalToolRegistry 注册 evolution 可调用的最小技能集合。
// 仅用于 ReAct 工具能力，不启动 scheduler。
func (s *Server) loadMinimalToolRegistry() error {
	daily := scheduler.NewDailyConsolidateSkill(s.memMgr)
	if err := s.registry.Register(daily); err != nil {
		return fmt.Errorf("register daily-consolidate: %w", err)
	}

	periodic := scheduler.NewPeriodicSummarizeSkill(s.memMgr)
	if err := s.registry.Register(periodic); err != nil {
		return fmt.Errorf("register periodic-summarize: %w", err)
	}
	return nil
}

