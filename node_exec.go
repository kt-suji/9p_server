package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const maxOutputLen = 32768

var (
	execMu        sync.Mutex
	execStatus    = "idle"
	execLastCmd   = "(未実行)"
	execExitCode  = 0
	execUpdatedAt = "-"
	execOutput    = "(まだコマンドは実行されていません)"
)

func init() {
	RegisterFeature(FeatureModule{
		Order:   50,
		Name:    "exec",
		Summary: "/server/ctl/exec, /client/<host>/ctl/exec   [RW] 安全仮想ターミナル (書込例: 'ipconfig', 'uname -a')",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "server/ctl/exec",
					ReadFn: func() ([]byte, error) {
						execMu.Lock()
						st := execStatus
						cmd := execLastCmd
						code := execExitCode
						upd := execUpdatedAt
						out := execOutput
						execMu.Unlock()
						return formatExecView("server/ctl/exec", "server", st, cmd, code, upd, out), nil
					},
					WriteFn: func(data []byte) error {
						cmdStr, err := extractUnifiedCommand(data)
						if err != nil {
							return err
						}
						return runServerExecCommand(cmdStr)
					},
				},
			}
		},
		ClientNodes: func(ci *ClientInfo, tb *TreeBuilder) {
			hostname := ci.Hostname
			base := fmt.Sprintf("client/%s", hostname)

			_ = tb.RegisterRW(base+"/ctl/exec", func() ([]byte, error) {
				clientMu.RLock()
				c, ok := activeClients[hostname]
				if !ok {
					clientMu.RUnlock()
					return []byte("Client: offline\n"), nil
				}
				status := c.ExecStatus
				lastCmd := c.ExecLastCmd
				exitCode := c.ExecExitCode
				updated := c.ExecUpdatedAt
				output := c.ExecOutput
				clientMu.RUnlock()
				return formatExecView(base+"/ctl/exec", "client/"+hostname, status, lastCmd, exitCode, updated, output), nil
			}, func(data []byte) error {
				cmdStr, err := extractUnifiedCommand(data)
				if err != nil {
					return err
				}
				if blocked, reason := isDangerousCommand(cmdStr); blocked {
					clientMu.Lock()
					if c, ok := activeClients[hostname]; ok {
						c.ExecStatus = "error"
						c.ExecLastCmd = cmdStr
						c.ExecExitCode = -1
						c.ExecUpdatedAt = time.Now().Format("2006-01-02 15:04:05")
						c.ExecOutput = "[SECURITY BLOCK] " + reason
					}
					clientMu.Unlock()
					addSystemLog(fmt.Sprintf("クライアント %s への危険コマンドをブロック (%q)", hostname, cmdStr))
					return nil
				}

				clientMu.Lock()
				c, ok := activeClients[hostname]
				if !ok || time.Since(c.LastSeen) > 30*time.Second {
					clientMu.Unlock()
					return fmt.Errorf("client %s is offline", hostname)
				}
				seq := nextCmdSeq()
				b64Cmd := base64.StdEncoding.EncodeToString([]byte(cmdStr))
				c.ExecStatus = "running"
				c.ExecLastCmd = cmdStr
				c.ExecUpdatedAt = time.Now().Format("2006-01-02 15:04:05")
				c.ExecOutput = "リモート端末へコマンドを送信中..."
				c.PendingCmds = append(c.PendingCmds, QueuedCmd{
					Seq: seq,
					Cmd: fmt.Sprintf("exec %s", b64Cmd),
				})
				clientMu.Unlock()
				addSystemLog(fmt.Sprintf("クライアント %s にリモートコマンド送信 (seq=%d): %s", hostname, seq, cmdStr))
				return nil
			})
		},
	})
}

// isDangerousCommand はシステム保護のための危険コマンドフィルタ（ブラクラ・OS破壊防止）です。
func isDangerousCommand(cmd string) (bool, string) {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	if strings.Contains(lower, "taskkill") || strings.Contains(lower, "stop-process") || strings.Contains(lower, "tskill") || strings.Contains(lower, "killall") || strings.Contains(lower, "pkill") {
		criticalProcs := []string{"explorer", "svchost", "csrss", "wininit", "winlogon", "lsass", "dwm", "smss", "systemd", "init", "p9_server", "9p_client"}
		for _, proc := range criticalProcs {
			if strings.Contains(lower, proc) {
				return true, fmt.Sprintf("重要プロセス (%s) の停止はシステム保護のため禁止されています", proc)
			}
		}
	}
	forbidden := []string{
		"shutdown", "restart-computer", "stop-computer", "logoff", "reboot", "poweroff", "init 0", "init 6",
		"format ", "diskpart", "bcdedit", "rd /s", "rmdir /s", "del /f /s", "rm -rf", "mkfs",
		"start explorer", ":(){", "fork",
	}
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			return true, fmt.Sprintf("危険なコマンドパターン (%s) はブロックされました", strings.TrimSpace(f))
		}
	}
	return false, ""
}

func formatExecView(filePath, targetName, status, lastCmd string, exitCode int, updatedAt, output string) []byte {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== 9P 安全仮想ターミナル (%s) ===\n", filePath))
	switch status {
	case "running":
		sb.WriteString("状態 (Status):     RUNNING (コマンド実行中 ⏳)\n")
	case "error":
		sb.WriteString("状態 (Status):     ERROR (エラーまたはシステム保護ブロック ⚠️)\n")
	case "finished":
		sb.WriteString("状態 (Status):     FINISHED (実行完了 ✓)\n")
	default:
		sb.WriteString("状態 (Status):     IDLE (未実行 / 待機中)\n")
	}
	sb.WriteString(fmt.Sprintf("対象PC (Target):   %s\n", targetName))
	sb.WriteString(fmt.Sprintf("前回コマンド:      %s\n", lastCmd))
	sb.WriteString(fmt.Sprintf("終了コード (Exit): %d\n", exitCode))
	sb.WriteString(fmt.Sprintf("最終更新 (Time):   %s\n\n", updatedAt))

	sb.WriteString("--- [実行出力 (Output)] ---\n")
	if strings.TrimSpace(output) == "" {
		sb.WriteString("(出力なし)\n")
	} else {
		sb.WriteString(strings.TrimRight(output, "\r\n") + "\n")
	}

	winPath := "P:\\" + strings.ReplaceAll(filePath, "/", "\\")
	sb.WriteString("\n--- [操作方法 (How to Operate)] ---\n")
	sb.WriteString("  1. GUIエディタ (Ctrl+S送信) : 下の [入力欄] の '> ' の右側にコマンドを書いて Ctrl+S で保存\n")
	sb.WriteString(fmt.Sprintf("  2. ファイル末尾への追記     : 最終行にコマンドを追記 (例: echo ipconfig >> %s)\n", winPath))
	sb.WriteString(fmt.Sprintf("  3. ファイル全体の上書き     : コマンド1行で上書き   (例: Set-Content %s \"ipconfig\")\n", winPath))
	sb.WriteString("  【入力例 (Windows)】 ipconfig   /   whoami & hostname   /   dir C:\\   /   tasklist\n")
	sb.WriteString("  【入力例 (Linux)】   uname -a   /   whoami && hostname  /   ls -la     /   ps aux\n\n")

	sb.WriteString("--- [入力欄 (Input)] ---\n")
	sb.WriteString("↓ ここ（'> ' の右側）に実行したいコマンドを入力して Ctrl+S で送信してください ↓\n")
	sb.WriteString("> \n")
	return []byte(sb.String())
}

func runServerExecCommand(cmdStr string) error {
	if blocked, reason := isDangerousCommand(cmdStr); blocked {
		execMu.Lock()
		execStatus = "error"
		execLastCmd = cmdStr
		execExitCode = -1
		execUpdatedAt = time.Now().Format("2006-01-02 15:04:05")
		execOutput = "[SECURITY BLOCK] " + reason
		execMu.Unlock()
		addSystemLog(fmt.Sprintf("サーバー仮想ターミナル: 危険コマンドをブロック (%q)", cmdStr))
		return nil
	}

	execMu.Lock()
	if execStatus == "running" {
		execMu.Unlock()
		return fmt.Errorf("別のコマンドが現在実行中です")
	}
	execStatus = "running"
	execLastCmd = cmdStr
	execUpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	execOutput = "実行中..."
	execMu.Unlock()

	addSystemLog(fmt.Sprintf("サーバー仮想ターミナル実行: %s", cmdStr))

	go func() {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		cmd := buildPlatformShellCommand(ctx, cmdStr)
		rawOut, err := cmd.CombinedOutput()
		decoded := decodeConsoleOutput(rawOut)
		if len(decoded) > maxOutputLen {
			decoded = decoded[:maxOutputLen] + "\n... [Output truncated at 32KB]"
		}

		exitCode := 0
		status := "finished"
		if ctx.Err() == context.DeadlineExceeded {
			status = "error"
			exitCode = 124
			decoded += "\n[TIMEOUT] コマンドが15秒の制限時間を超過したため強制終了しました。"
		} else if err != nil {
			status = "error"
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
			if strings.TrimSpace(decoded) == "" {
				decoded = err.Error()
			}
		}

		elapsed := time.Since(start).Seconds()
		execMu.Lock()
		execStatus = status
		execExitCode = exitCode
		execUpdatedAt = fmt.Sprintf("%s (%.2fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed)
		execOutput = decoded
		execMu.Unlock()
	}()

	return nil
}
