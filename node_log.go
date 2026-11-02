package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	benchLogFile  = "bench.log"
	systemLogFile = "system.log"
)

var (
	sysLogMu  sync.Mutex
	sysLogs   []string
	benchLogs []string
)

func init() {
	// 過去のログファイルをディスクから自動復元（履歴参照用）
	if data, err := os.ReadFile(benchLogFile); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				benchLogs = append(benchLogs, l)
			}
		}
	}
	if data, err := os.ReadFile(systemLogFile); err == nil {
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				sysLogs = append(sysLogs, l)
			}
		}
	}

	RegisterFeature(FeatureModule{
		Order:   60,
		Name:    "log",
		Summary: "/server/log/{system,bench}  [Read]  システムイベントログ・ベンチマーク計測履歴",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "server/log/system",
					ReadFn: func() ([]byte, error) {
						sysLogMu.Lock()
						defer sysLogMu.Unlock()
						if len(sysLogs) == 0 {
							return []byte("(no system logs yet)\n"), nil
						}
						return []byte(strings.Join(sysLogs, "\n") + "\n"), nil
					},
				},
				{
					Path: "server/log/bench",
					ReadFn: func() ([]byte, error) {
						benchMu.Lock()
						defer benchMu.Unlock()
						if len(benchLogs) == 0 {
							return []byte("(no benchmark runs recorded yet)\n"), nil
						}
						return []byte(strings.Join(benchLogs, "\n") + "\n"), nil
					},
				},
			}
		},
	})
}

func addSystemLog(msg string) {
	sysLogMu.Lock()
	defer sysLogMu.Unlock()
	entry := fmt.Sprintf("[%s] %s", time.Now().Format("2006-01-02 15:04:05"), msg)
	sysLogs = append(sysLogs, entry)
	if len(sysLogs) > 200 {
		sysLogs = sysLogs[len(sysLogs)-200:]
	}
	if f, err := os.OpenFile(systemLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		_, _ = f.WriteString(entry + "\n")
		_ = f.Close()
	}
}

func addBenchLog(msg string) {
	benchMu.Lock()
	defer benchMu.Unlock()
	entry := fmt.Sprintf("[%s] %s", time.Now().Format("2006-01-02 15:04:05"), msg)
	benchLogs = append(benchLogs, entry)
	if len(benchLogs) > 200 {
		benchLogs = benchLogs[len(benchLogs)-200:]
	}
	if f, err := os.OpenFile(benchLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		_, _ = f.WriteString(entry + "\n")
		_ = f.Close()
	}
}
