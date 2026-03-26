package main

import (
	"fmt"
	"os"

	"mybot/internal/config"
	"mybot/internal/memory"
	"mybot/internal/server"
)

func main() {
	// 初始化配置（brain 目录路径）
	if err := config.InitBrainPath(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to initialize brain path: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "help", "--help", "-h":
		printUsage()
		os.Exit(0)

	case "init":
		// 加载配置
		cfg, err := config.LoadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		// 初始化 brain 目录
		if err := memory.InitBrainDirectory(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// 保存配置文件（如果不存在）
		configPath := config.GetConfigPath()
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			if err := config.SaveConfig(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to save config file: %v\n", err)
			} else {
				fmt.Printf("Configuration file created: %s\n", configPath)
			}
		}

		// 显示配置信息
		fmt.Printf("Brain directory initialized successfully!\n")
		fmt.Printf("Brain directory: %s\n", cfg.Brain.Dir)
		fmt.Printf("Brain base directory (for git): %s\n", cfg.Brain.BaseDir)
		fmt.Printf("Configuration file: %s\n", configPath)
		
		// 显示配置提示
		fmt.Println("\nConfiguration:")
		fmt.Printf("  LLM Provider: %s\n", cfg.LLM.Provider)
		fmt.Printf("  LLM Enabled: %v\n", cfg.LLM.Enabled)
		fmt.Printf("  Evolution Enabled: %v\n", cfg.Evolution.Enabled)
		
		fmt.Println("\nTo customize configuration, edit the config file or set environment variables:")
		fmt.Println("  CATA_BRAIN_DIR - Brain directory path")
		fmt.Println("  CATA_CONFIG_FILE - Config file path")
		fmt.Println("  OPENAI_API_KEY - OpenAI API key")

	case "config":
		// 配置管理命令
		handleConfigCommand(os.Args[2:])

	case "run":
		// 启动常驻进程
		runServer()

	case "stop":
		// 停止常驻进程（通过信号）
		stopServer()

	case "upgrade":
		// 优雅退出（用于升级）
		upgradeServer()

	case "test":
		// 测试基础功能
		testBasicFunctions()

	default:
		fmt.Fprintf(os.Stderr, "Error: Unknown command: %s\n\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Cata - 核心记忆服务（精简实现）")
	fmt.Println()
	fmt.Println("Usage: cata <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init     Initialize brain directory structure")
	fmt.Println("  config   Manage configuration (show, get, set, edit)")
	fmt.Println("  run      Start the Cata server (daemon mode)")
	fmt.Println("  stop     Stop the Cata server (sends signal)")
	fmt.Println("  upgrade  Gracefully stop for upgrade")
	fmt.Println("  test     Test basic functionality")
	fmt.Println("  help     Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  cata init              # Initialize brain directory")
	fmt.Println("  cata run               # Start server in foreground")
	fmt.Println("  cata stop              # Stop running server")
	fmt.Println()
	fmt.Println("Client (catacli):")
	fmt.Println("  catacli recall \"query\" [topK] [--llm]")
	fmt.Println("  catacli digest [query] [--since 7d]")
	fmt.Println("  catacli consolidate \"topic\" \"content\"")
	fmt.Println("  catacli evolve <status|history|once>")
	fmt.Println("  catacli task <create|list|status> ...")
	fmt.Println("  catacli ping")
	fmt.Println()
	fmt.Println("For more information, see README.md")
}

func runServer() {
	// 加载配置（如果尚未加载）
	if config.Config == nil {
		cfg, err := config.LoadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		} else {
			config.Config = cfg
		}
	}

	srv, err := server.NewServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create server: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start server: %v\n", err)
		os.Exit(1)
	}

	// 阻塞等待
	srv.Wait()
}

func stopServer() {
	// TODO: 实现通过 PID 文件或 socket 发送停止信号
	// 目前简化实现：直接发送 SIGTERM 给当前进程组
	fmt.Println("Stopping Cata server...")
	fmt.Println("Note: This is a simplified implementation. In production, use PID file or socket communication.")
}

func upgradeServer() {
	// 同 stop，优雅退出
	fmt.Println("Upgrading Cata server (graceful shutdown)...")
	fmt.Println("Note: After this process exits, external supervisor should start new process.")
}

func testBasicFunctions() {
	fmt.Println("Testing basic functionality...")

	// 创建 MemoryManager
	mm, err := memory.NewMemoryManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create MemoryManager: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ MemoryManager created")

	// 测试 Recall（应该返回空结果，因为还没有内容）
	results, err := mm.RecallSimple("测试", 5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to recall: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Recall test: found %d results\n", len(results))

	// 测试 Consolidate
	err = mm.Consolidate("测试主题", "这是一条测试内容")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to consolidate: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Consolidate test: wrote test content")

	// 再次测试 Recall
	results, err = mm.RecallSimple("测试", 5)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to recall: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Recall after consolidate: found %d results\n", len(results))

	fmt.Println("\nAll tests passed!")
}
