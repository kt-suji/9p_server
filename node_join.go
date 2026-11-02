package main

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QueuedCmd はクライアント宛の待機中コマンド（シーケンス番号付き）です。
type QueuedCmd struct {
	Seq uint64
	Cmd string // 例: "bench 5", "exec <base64>"
}

// ClientInfo はクラスタに参加している各クライアントの状態を保持します。
type ClientInfo struct {
	Hostname       string
	OS             string
	ClientVersion  int64
	CPUPercent     float64
	MemUsedMB      uint64
	MemTotalMB     uint64
	LastSeen       time.Time
	PendingCmds    []QueuedCmd
	BenchStatus    string // "idle", "running", "finished"
	BenchMeasured  bool   // 未計測と0を明確に区別するフラグ
	BenchResult    string
	BenchMetrics   BenchMetrics
	BenchDuration  int
	BenchStartTime time.Time

	ExecStatus    string // "idle", "running", "finished", "error"
	ExecLastCmd   string
	ExecExitCode  int
	ExecUpdatedAt string
	ExecOutput    string
}

var (
	clientMu        sync.RWMutex
	activeClients   = make(map[string]*ClientInfo)
	registeredHosts = make(map[string]bool)
)

func init() {
	RegisterFeature(FeatureModule{
		Order:   70,
		Name:    "join",
		Summary: "/join                      [RW]    参加クライアント一覧 (Read) / クライアント心拍報告窓口 (Write)",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "join",
					ReadFn: func() ([]byte, error) {
						clientMu.RLock()
						defer clientMu.RUnlock()
						reqVer := GetRequiredClientVersion()
						keys := getSortedClientKeys()
						var out []string
						out = append(out, fmt.Sprintf("min_client_version: %d", reqVer))
						out = append(out, fmt.Sprintf("Active Clients (%d):", len(keys)))
						for _, h := range keys {
							c := activeClients[h]
							status := "Online"
							if time.Since(c.LastSeen) > 30*time.Second {
								status = "Offline"
							}
							out = append(out, fmt.Sprintf(" - %s [%s] v%d OS:%s CPU:%.1f%%, MEM:%dMB/%dMB (Last: %s)",
								h, status, c.ClientVersion, c.OS, c.CPUPercent, c.MemUsedMB, c.MemTotalMB, c.LastSeen.Format("15:04:05")))
							for _, q := range c.PendingCmds {
								out = append(out, fmt.Sprintf("CMD_%s: %d %s", h, q.Seq, q.Cmd))
							}
						}
						return []byte(strings.Join(out, "\n") + "\n"), nil
					},
					WriteFn: func(data []byte) error {
						return handleClientReport(data)
					},
				},
			}
		},
	})
}

func getSortedClientKeys() []string {
	keys := make([]string, 0, len(activeClients))
	for k := range activeClients {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// handleClientReport はクライアントからのテレメトリ報告を受信し、新規クライアントなら動的にツリーノードを生やします。
func handleClientReport(data []byte) error {
	lines := strings.Split(string(data), "\n")
	now := time.Now()

	var hostname, osStr string
	var clientVersionReport int64
	var cpuPercent float64
	var memUsedMB, memTotalMB uint64
	var benchStatusReport, benchResultReport string
	var execStatusReport, execCmdB64, execOutB64 string
	var execExitReport int
	var hasExecExit bool
	var cmdAckSeq uint64
	var hasCmdAck bool

	for _, line := range lines {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])

		switch key {
		case "hostname", "id":
			hostname = strings.ToLower(val)
		case "os":
			osStr = val
		case "version", "client_version":
			if v, err := strconv.ParseInt(val, 10, 64); err == nil {
				clientVersionReport = v
			}
		case "cpu":
			if v, err := strconv.ParseFloat(val, 64); err == nil {
				cpuPercent = v
			}
		case "mem_used_mb":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				memUsedMB = v
			}
		case "mem_total_mb":
			if v, err := strconv.ParseUint(val, 10, 64); err == nil {
				memTotalMB = v
			}
		case "bench_status":
			benchStatusReport = strings.ToLower(val)
		case "bench_result":
			benchResultReport = val
		case "exec_status":
			execStatusReport = strings.ToLower(val)
		case "exec_cmd_b64":
			execCmdB64 = val
		case "exec_exit":
			if v, err := strconv.Atoi(val); err == nil {
				execExitReport = v
				hasExecExit = true
			}
		case "exec_out_b64":
			execOutB64 = val
		case "cmd_ack":
			if ackSeq, err := strconv.ParseUint(val, 10, 64); err == nil {
				cmdAckSeq = ackSeq
				hasCmdAck = true
			}
		}
	}

	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" {
		hostname = "client"
	}

	clientMu.Lock()
	c, exists := activeClients[hostname]
	isNewHost := false
	if !exists {
		c = &ClientInfo{
			Hostname:      hostname,
			OS:            osStr,
			ClientVersion: clientVersionReport,
			CPUPercent:    cpuPercent,
			MemUsedMB:     memUsedMB,
			MemTotalMB:    memTotalMB,
			LastSeen:      now,
			BenchStatus:   "idle",
			BenchMeasured: false,
			BenchResult:   "未計測",
			ExecStatus:    "idle",
			ExecLastCmd:   "(未実行)",
			ExecUpdatedAt: "-",
			ExecOutput:    "(まだコマンドは実行されていません)",
		}
		activeClients[hostname] = c
		isNewHost = !registeredHosts[hostname]
		if isNewHost {
			registeredHosts[hostname] = true
		}
		addSystemLog(fmt.Sprintf("新規クライアント参加: %s (%s)", hostname, osStr))
	} else {
		c.LastSeen = now
		c.CPUPercent = cpuPercent
		c.MemUsedMB = memUsedMB
		c.MemTotalMB = memTotalMB
		if clientVersionReport > 0 {
			c.ClientVersion = clientVersionReport
		}
		if osStr != "" {
			c.OS = osStr
		}
	}

	if hasCmdAck && len(c.PendingCmds) > 0 {
		remaining := make([]QueuedCmd, 0, len(c.PendingCmds))
		for _, q := range c.PendingCmds {
			if q.Seq > cmdAckSeq {
				remaining = append(remaining, q)
			}
		}
		c.PendingCmds = remaining
	}

	if benchStatusReport != "" {
		if c.BenchStatus == "running" && benchStatusReport == "idle" && time.Since(c.BenchStartTime) < 4*time.Second {
			// 命令送信直後のハートビートによる上書きを抑止
		} else {
			c.BenchStatus = benchStatusReport
		}
	}
	if benchResultReport != "" && benchResultReport != c.BenchResult {
		c.BenchResult = benchResultReport
		metrics := parseBenchMetrics(benchResultReport)
		c.BenchMetrics = metrics
		if metrics.Measured {
			c.BenchMeasured = true
		}
		addBenchLog(fmt.Sprintf("[%s] 計測完了: %s", hostname, benchResultReport))
	}

	var decodedCmd, decodedOut string
	if execCmdB64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(execCmdB64); err == nil {
			decodedCmd = string(raw)
		}
	}
	if execOutB64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(execOutB64); err == nil {
			decodedOut = string(raw)
		}
	}
	if execStatusReport != "" && execStatusReport != "idle" {
		if c.ExecStatus == "running" && decodedCmd != "" && decodedCmd != c.ExecLastCmd {
			// 新しいコマンド送信直後の古いレポートは無視
		} else {
			prevStatus := c.ExecStatus
			c.ExecStatus = execStatusReport
			if decodedCmd != "" {
				c.ExecLastCmd = decodedCmd
			}
			if hasExecExit {
				c.ExecExitCode = execExitReport
			}
			if decodedOut != "" {
				c.ExecOutput = decodedOut
			}
			if prevStatus != execStatusReport || execStatusReport == "finished" || execStatusReport == "error" {
				c.ExecUpdatedAt = now.Format("2006-01-02 15:04:05")
			}
		}
	}
	clientMu.Unlock()

	// 動的に client/<hostname>/ のフォルダと各機能のファイルを生成
	if globalTreeBuilder != nil && isNewHost {
		registerClientNodes(c)
	}

	return nil
}
