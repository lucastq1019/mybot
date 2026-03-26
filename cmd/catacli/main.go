package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultSocketPath = ".cata/cata.sock"
)

type Request struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	// help 命令不需要连接服务器
	if command == "help" || command == "--help" || command == "-h" {
		printUsage()
		os.Exit(0)
	}

	// 连接到服务器
	conn, err := connectToServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to connect to server: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure the server is running with 'cata run'\n")
		os.Exit(1)
	}
	defer conn.Close()

	// 处理命令（精简版：仅保留核心记忆命令与连接检测）
	var req Request
	switch command {
	case "recall":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: catacli recall <query> [topK] [--llm]")
			os.Exit(1)
		}
		req = Request{Command: "recall", Args: os.Args[2:]}

	case "digest":
		req = Request{Command: "digest", Args: os.Args[2:]}

	case "consolidate":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: catacli consolidate <topic> <content>")
			os.Exit(1)
		}
		req = Request{
			Command: "consolidate",
			Args:    []string{os.Args[2], strings.Join(os.Args[3:], " ")},
		}

	case "ping":
		req = Request{Command: "ping", Args: []string{}}

	case "evolve":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: catacli evolve <status|history|once>")
			os.Exit(1)
		}
		req = Request{Command: "evolve", Args: os.Args[2:]}

	case "task":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: catacli task <create|list|status> [args]")
			os.Exit(1)
		}
		req = Request{Command: "task", Args: os.Args[2:]}

	default:
		fmt.Fprintf(os.Stderr, "Error: Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}

	// 发送请求
	reqData, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to marshal request: %v\n", err)
		os.Exit(1)
	}

	if _, err := conn.Write(append(reqData, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to send request: %v\n", err)
		os.Exit(1)
	}

	// 读取响应
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		fmt.Fprintf(os.Stderr, "Error: Failed to read response\n")
		os.Exit(1)
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to parse response: %v\n", err)
		os.Exit(1)
	}

	// 输出结果
	if resp.Success {
		if resp.Data != nil {
			// 格式化输出数据
			outputData(resp.Data)
		} else {
			fmt.Println(resp.Message)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Message)
		os.Exit(1)
	}
}

func connectToServer() (net.Conn, error) {
	socketPath := getSocketPath()
	return net.Dial("unix", socketPath)
}

func getSocketPath() string {
	root := findProjectRoot()
	if root != "" {
		return filepath.Join(root, ".cata", "cata.sock")
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, DefaultSocketPath)
}

// findProjectRoot 向上查找包含 go.mod 或 .git 的项目根目录（与 cata 服务端一致）
func findProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func outputData(data interface{}) {
	dataJSON, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(dataJSON))
}

func printUsage() {
	fmt.Println("Cata CLI - 精简核心命令交互客户端")
	fmt.Println()
	fmt.Println("Usage: catacli <command> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  recall <query> [topK] [--llm]  Recall memory by query")
	fmt.Println("  digest [query] [--since N]      Generate digest from memory")
	fmt.Println("  consolidate <topic> <content>   Write memory")
	fmt.Println("  evolve <status|history|once>    Evolution status and controls")
	fmt.Println("  task <create|list|status> ...   Task queue operations")
	fmt.Println("  ping                            Check server connection")
	fmt.Println("  help                            Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  catacli recall \"项目设计\" 5")
	fmt.Println("  catacli digest --week")
	fmt.Println("  catacli consolidate \"需求\" \"保留核心流程\"")
	fmt.Println("  catacli evolve status")
	fmt.Println("  catacli task create \"整理历史记忆\" --async")
	fmt.Println("  catacli ping")
	fmt.Println()
	fmt.Println("Note: Run 'cata run' first to start the server.")
}
