package main

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/mem"
)

func init() {
	RegisterFeature(FeatureModule{
		Order: 20,
		Name:  "cluster",
		Summary: "/cluster/status            [Read]  クラスタ全体の稼働状況・OS・CPU・メモリ一覧\n" +
			"/cluster/bench             [RW]    全ノード一括ベンチマーク比較表・起動制御 (書込例: 'start all 5')",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "cluster/status",
					ReadFn: func() ([]byte, error) {
						return formatClusterStatus(), nil
					},
				},
				{
					Path: "cluster/bench",
					ReadFn: func() ([]byte, error) {
						return formatClusterBench("cluster/bench"), nil
					},
					WriteFn: func(data []byte) error {
						return handleClusterBenchWrite(data)
					},
				},
			}
		},
	})
}

// formatClusterStatus は /cluster/status 用の「純粋な稼働状態・CPU・メモリ一覧」を生成します。
func formatClusterStatus() []byte {
	srvCPU := getServerCPU()
	var srvMemUsed, srvMemTotal uint64
	if v, err := mem.VirtualMemory(); err == nil {
		srvMemUsed = v.Used / 1024 / 1024
		srvMemTotal = v.Total / 1024 / 1024
	}
	srvCfg := getLiveServerConfig()
	srvOS := fmt.Sprintf("%s (%s/%s)", srvCfg.Server.System.OSInfo, runtime.GOOS, runtime.GOARCH)

	type statusRow struct {
		targetName string
		connState  string
		osStr      string
		cpuStr     string
		memStr     string
		freeMemStr string
		lastSeen   string
	}

	clientMu.RLock()
	keys := getSortedClientKeys()
	totalNodes := 1 + len(keys)
	onlineNodes := 1
	totalMemMB := srvMemTotal
	usedMemMB := srvMemUsed

	srvFreeMB := uint64(0)
	if srvMemTotal > srvMemUsed {
		srvFreeMB = srvMemTotal - srvMemUsed
	}

	rows := []statusRow{
		{
			targetName: "server",
			connState:  "ONLINE",
			osStr:      srvOS,
			cpuStr:     fmt.Sprintf("%5.1f%% (%dc)", srvCPU, runtime.NumCPU()),
			memStr:     fmt.Sprintf("%6d / %6d MB", srvMemUsed, srvMemTotal),
			freeMemStr: fmt.Sprintf("%6d MB", srvFreeMB),
			lastSeen:   "常時 (Local)",
		},
	}

	for _, h := range keys {
		cl := activeClients[h]
		online := time.Since(cl.LastSeen) <= 30*time.Second
		connStr := "ONLINE"
		if online {
			onlineNodes++
			totalMemMB += cl.MemTotalMB
			usedMemMB += cl.MemUsedMB
		} else {
			connStr = "OFFLINE"
		}

		freeMB := uint64(0)
		if cl.MemTotalMB > cl.MemUsedMB {
			freeMB = cl.MemTotalMB - cl.MemUsedMB
		}
		osInfo := cl.OS
		if osInfo == "" {
			osInfo = "-"
		}

		rows = append(rows, statusRow{
			targetName: h,
			connState:  connStr,
			osStr:      osInfo,
			cpuStr:     fmt.Sprintf("%5.1f%%", cl.CPUPercent),
			memStr:     fmt.Sprintf("%6d / %6d MB", cl.MemUsedMB, cl.MemTotalMB),
			freeMemStr: fmt.Sprintf("%6d MB", freeMB),
			lastSeen:   cl.LastSeen.Format("15:04:05"),
		})
	}
	clientMu.RUnlock()

	totalFreeMB := uint64(0)
	if totalMemMB > usedMemMB {
		totalFreeMB = totalMemMB - usedMemMB
	}

	var sb strings.Builder
	sb.WriteString("=== 9P クラスタ稼働ステータス一覧 (/cluster/status) ===\n")
	sb.WriteString(fmt.Sprintf("現在時刻:         %s\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("オンライン台数:   %d 台オンライン / 登録 %d 台 (Server 1 + Client %d)\n", onlineNodes, totalNodes, len(keys)))
	sb.WriteString(fmt.Sprintf("クラスタ総メモリ: %d MB 使用中 / %d MB 合計 (空き: %d MB)\n\n", usedMemMB, totalMemMB, totalFreeMB))

	sb.WriteString("--- [ノード別 リソース稼働状況] ---\n")
	sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-12s | %-18s | %-10s | %-10s | %s\n",
		"対象名 (Target)", "状態", "CPU使用率", "メモリ (使用/合計)", "空きメモリ", "最終確認", "OS / 環境"))
	sb.WriteString(strings.Repeat("-", 110) + "\n")
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-12s | %-18s | %-10s | %-10s | %s\n",
			r.targetName, r.connState, r.cpuStr, r.memStr, r.freeMemStr, r.lastSeen, r.osStr))
	}
	sb.WriteString(strings.Repeat("-", 110) + "\n")
	sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-12s | %6d / %6d MB | %6d MB  | %-10s | -\n\n",
		"合計 (Cluster計)", fmt.Sprintf("%d/%d台", onlineNodes, totalNodes), "-",
		usedMemMB, totalMemMB, totalFreeMB, "-"))

	sb.WriteString("💡 ベンチマーク比較表の閲覧や一括実行は /cluster/bench を参照してください。\n")
	return []byte(sb.String())
}

// formatClusterBench は /cluster/bench 用の「ベンチマーク比較表 & 一括起動制御」を生成します。
func formatClusterBench(filePath string) []byte {
	benchMu.Lock()
	srvStatus := benchStatus
	srvMeasured := benchMeasured
	srvDur := benchDurationSec
	srvStart := benchStartTime
	srvPt := benchScorePt
	srvMops := benchScoreMops
	srvElapsed := benchElapsedSec
	srvCores := runtime.NumCPU()
	benchMu.Unlock()

	clientMu.RLock()
	keys := getSortedClientKeys()
	totalNodes := 1 + len(keys)
	onlineNodes := 1

	measuredNodes := 0
	var totalPt uint64
	var totalMops float64
	anyRunning := (srvStatus == "running")

	if srvMeasured {
		measuredNodes++
		totalPt += uint64(srvPt)
		totalMops += srvMops
	}

	type benchRow struct {
		targetName  string
		connState   string
		benchState  string
		scoreStr    string
		rateStr     string
		timeCoreStr string
	}

	var rows []benchRow

	// 1. サーバー行
	srvRow := benchRow{
		targetName: "server",
		connState:  "ONLINE",
	}
	if srvStatus == "running" {
		el := time.Since(srvStart).Seconds()
		if el > float64(srvDur) {
			el = float64(srvDur)
		}
		srvRow.benchState = fmt.Sprintf("計測中🔥(%.1f/%ds)", el, srvDur)
		if srvMeasured {
			srvRow.scoreStr = fmt.Sprintf("%d pt (前回)", srvPt)
			srvRow.rateStr = fmt.Sprintf("%.1f Mops/s", srvMops)
		} else {
			srvRow.scoreStr = "(計測中...)"
			srvRow.rateStr = "(計測中...)"
		}
		srvRow.timeCoreStr = fmt.Sprintf("%dc / 実行中", srvCores)
	} else if !srvMeasured {
		srvRow.benchState = "未計測"
		srvRow.scoreStr = "未計測"
		srvRow.rateStr = "未計測"
		srvRow.timeCoreStr = fmt.Sprintf("%dc / -", srvCores)
	} else {
		srvRow.benchState = "完了 🎉"
		srvRow.scoreStr = fmt.Sprintf("%d pt", srvPt)
		srvRow.rateStr = fmt.Sprintf("%.1f Mops/s", srvMops)
		srvRow.timeCoreStr = fmt.Sprintf("%dc / %.2fs", srvCores, srvElapsed)
	}
	rows = append(rows, srvRow)

	// 2. 各クライアント行
	for _, h := range keys {
		cl := activeClients[h]
		online := time.Since(cl.LastSeen) <= 30*time.Second
		connStr := "ONLINE"
		if online {
			onlineNodes++
		} else {
			connStr = "OFFLINE"
		}

		if cl.BenchStatus == "running" {
			anyRunning = true
		}
		if cl.BenchMeasured {
			measuredNodes++
			totalPt += uint64(cl.BenchMetrics.ScorePt)
			totalMops += cl.BenchMetrics.Mops
		}

		r := benchRow{
			targetName: h,
			connState:  connStr,
		}

		if cl.BenchStatus == "running" {
			el := time.Since(cl.BenchStartTime).Seconds()
			if el > float64(cl.BenchDuration) {
				el = float64(cl.BenchDuration)
			}
			r.benchState = fmt.Sprintf("計測中🔥(%.1f/%ds)", el, cl.BenchDuration)
			if cl.BenchMeasured {
				r.scoreStr = fmt.Sprintf("%d pt (前回)", cl.BenchMetrics.ScorePt)
				r.rateStr = fmt.Sprintf("%.1f Mops/s", cl.BenchMetrics.Mops)
			} else {
				r.scoreStr = "(計測中...)"
				r.rateStr = "(計測中...)"
			}
			r.timeCoreStr = "実行中"
		} else if !cl.BenchMeasured {
			r.benchState = "未計測"
			r.scoreStr = "未計測"
			r.rateStr = "未計測"
			r.timeCoreStr = "-"
		} else {
			r.benchState = "完了 🎉"
			r.scoreStr = fmt.Sprintf("%d pt", cl.BenchMetrics.ScorePt)
			r.rateStr = fmt.Sprintf("%.1f Mops/s", cl.BenchMetrics.Mops)
			r.timeCoreStr = fmt.Sprintf("%dc / %.2fs", cl.BenchMetrics.Cores, cl.BenchMetrics.ElapsedSec)
		}
		rows = append(rows, r)
	}
	clientMu.RUnlock()

	var combinedSummary string
	var totalScoreCell, totalRateCell, totalBenchStateCell string
	if measuredNodes == 0 {
		if anyRunning {
			combinedSummary = fmt.Sprintf("現在計測中 🔥 (計測完了ノード: 0 / %d 台)", totalNodes)
			totalBenchStateCell = "計測中 🔥"
			totalScoreCell = "(計測中...)"
			totalRateCell = "(計測中...)"
		} else {
			combinedSummary = fmt.Sprintf("未計測 (計測済みノード: 0 / %d 台)", totalNodes)
			totalBenchStateCell = fmt.Sprintf("未計測 (0/%d台)", totalNodes)
			totalScoreCell = "未計測"
			totalRateCell = "未計測"
		}
	} else if measuredNodes < totalNodes {
		combinedSummary = fmt.Sprintf("%d pt | 合計レート: %.1f Mops/s (計測済み: %d / %d 台 ※未計測ノードを除く暫定合計)",
			totalPt, totalMops, measuredNodes, totalNodes)
		totalBenchStateCell = fmt.Sprintf("一部完了 (%d/%d台)", measuredNodes, totalNodes)
		totalScoreCell = fmt.Sprintf("%d pt (暫定)", totalPt)
		totalRateCell = fmt.Sprintf("%.1f Mops/s", totalMops)
	} else {
		combinedSummary = fmt.Sprintf("%d pt | 合計レート: %.1f Mops/s (全 %d / %d 台 計測完了 🎉)",
			totalPt, totalMops, measuredNodes, totalNodes)
		totalBenchStateCell = fmt.Sprintf("全完了 (%d/%d台)", measuredNodes, totalNodes)
		totalScoreCell = fmt.Sprintf("%d pt", totalPt)
		totalRateCell = fmt.Sprintf("%.1f Mops/s", totalMops)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== 9P クラスタ一括ベンチマーク比較 & 起動制御 (/%s) ===\n", filePath))
	sb.WriteString(fmt.Sprintf("現在時刻:           %s\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("稼働ノード数:       %d 台オンライン / 登録 %d 台 (Server 1 + Client %d)\n", onlineNodes, totalNodes, len(keys)))
	sb.WriteString(fmt.Sprintf("クラスタ合算ベンチ: %s\n\n", combinedSummary))

	sb.WriteString("--- [ノード別 ベンチマーク結果一覧] ---\n")
	sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-16s | %-14s | %-16s | %s\n",
		"対象名 (Target)", "接続", "ベンチ状態", "スコア (Score)", "演算速度 (Rate)", "コア/計測時間"))
	sb.WriteString(strings.Repeat("-", 92) + "\n")
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-16s | %-14s | %-16s | %s\n",
			r.targetName, r.connState, r.benchState, r.scoreStr, r.rateStr, r.timeCoreStr))
	}
	sb.WriteString(strings.Repeat("-", 92) + "\n")
	sb.WriteString(fmt.Sprintf("%-18s | %-7s | %-16s | %-14s | %-16s | -\n\n",
		"合計 (Cluster計)", fmt.Sprintf("%d/%d台", onlineNodes, totalNodes),
		totalBenchStateCell, totalScoreCell, totalRateCell))

	winPath := "P:\\" + strings.ReplaceAll(filePath, "/", "\\")
	sb.WriteString("--- [操作方法 (対象・秒数を選んでベンチマーク起動)] ---\n")
	sb.WriteString("  1. GUIエディタ (Ctrl+S送信) : 下の [入力欄] の '> ' の右側に書いて Ctrl+S で保存\n")
	sb.WriteString(fmt.Sprintf("  2. ファイル末尾への追記     : 最終行にコマンドを追記 (例: echo start all 5 >> %s)\n", winPath))
	sb.WriteString(fmt.Sprintf("  3. ファイル全体の上書き     : コマンド1行で上書き   (例: Set-Content %s \"start all 5\")\n", winPath))
	sb.WriteString("  【コマンド書式と入力例】\n")
	sb.WriteString("    ・全ノード一斉計測 : start all 5             (または start 5)\n")
	sb.WriteString("    ・サーバー単体計測 : start server 5\n")
	if len(keys) > 0 {
		sb.WriteString(fmt.Sprintf("    ・クライアント単体 : start %s 5\n\n", keys[0]))
	} else {
		sb.WriteString("    ・クライアント単体 : start <クライアント名> 5\n\n")
	}

	sb.WriteString("--- [入力欄 (Input)] ---\n")
	sb.WriteString("↓ ここ（'> ' の右側）に 'start <対象名またはall> [秒数]' を入力して Ctrl+S で送信してください ↓\n")
	sb.WriteString("> \n")
	return []byte(sb.String())
}

func handleClusterBenchWrite(data []byte) error {
	cmdStr, err := extractUnifiedCommand(data)
	if err != nil {
		return err
	}
	target, sec, err := parseBenchCommand(cmdStr, "all")
	if err != nil {
		return err
	}
	return triggerBenchmarkByTarget(target, sec)
}
