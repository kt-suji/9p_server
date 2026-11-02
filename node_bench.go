package main

import (
	"fmt"
	"math"
	"math/bits"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BenchMetrics はベンチマーク結果文字列から抽出した構造化メトリクスです。
type BenchMetrics struct {
	Measured   bool
	ScorePt    uint32
	Cores      int
	ElapsedSec float64
	Mops       float64
}

var (
	benchMu          sync.Mutex
	benchStatus      = "idle" // "idle", "running", "finished"
	benchMeasured    = false  // 未計測と0を明確に区別するフラグ
	benchDurationSec = 5
	benchStartTime   time.Time
	benchThreads     = 0
	benchScorePt     uint32
	benchScoreMops   float64
	benchElapsedSec  float64
	benchResultText  = "未計測"
)

func init() {
	RegisterFeature(FeatureModule{
		Order:   40,
		Name:    "bench",
		Summary: "/server/ctl/bench, /client/<host>/ctl/bench [RW] 単体ノードCPUベンチマーク (書込例: 'start 5')",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "server/ctl/bench",
					ReadFn: func() ([]byte, error) {
						return getServerBenchContent(), nil
					},
					WriteFn: func(data []byte) error {
						return handleServerBenchWrite(data)
					},
				},
			}
		},
		ClientNodes: func(ci *ClientInfo, tb *TreeBuilder) {
			hostname := ci.Hostname
			base := fmt.Sprintf("client/%s", hostname)

			_ = tb.RegisterRW(base+"/ctl/bench", func() ([]byte, error) {
				clientMu.RLock()
				c, ok := activeClients[hostname]
				if !ok {
					clientMu.RUnlock()
					return []byte("Client: offline\n"), nil
				}
				st := c.BenchStatus
				meas := c.BenchMeasured
				dur := c.BenchDuration
				startT := c.BenchStartTime
				cpuPct := c.CPUPercent
				res := c.BenchResult
				clientMu.RUnlock()
				return formatSingleBenchView(base+"/ctl/bench", hostname, st, meas, dur, startT, cpuPct, res), nil
			}, func(data []byte) error {
				cmdStr, err := extractUnifiedCommand(data)
				if err != nil {
					return err
				}
				target, sec, err := parseBenchCommand(cmdStr, hostname)
				if err != nil {
					return err
				}
				return triggerBenchmarkByTarget(target, sec)
			})
		},
	})
}

// parseBenchCommand はベンチマークコマンド文字列から対象ノードと秒数を解析します。
// 対応書式:
//   - "start"                     -> defaultTarget, 5秒
//   - "start 10"                  -> defaultTarget, 10秒
//   - "start all 10"              -> "all", 10秒
//   - "start server 5"            -> "server", 5秒
//   - "start desktop-snc5oe2 5"   -> "desktop-snc5oe2", 5秒
func parseBenchCommand(cmdStr string, defaultTarget string) (target string, sec int, err error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(cmdStr)))
	if len(fields) == 0 {
		return "", 0, fmt.Errorf("empty benchmark command")
	}

	sec = 5
	target = defaultTarget

	rest := fields
	if rest[0] == "start" || rest[0] == "run" || rest[0] == "bench" {
		rest = rest[1:]
		if len(rest) == 0 {
			return target, sec, nil
		}
	}

	if len(rest) == 1 {
		if s, convErr := strconv.Atoi(rest[0]); convErr == nil && s > 0 {
			sec = s
		} else {
			target = strings.TrimPrefix(rest[0], "client/")
		}
	} else if len(rest) >= 2 {
		if s, convErr := strconv.Atoi(rest[1]); convErr == nil && s > 0 {
			target = strings.TrimPrefix(rest[0], "client/")
			sec = s
		} else if s0, convErr0 := strconv.Atoi(rest[0]); convErr0 == nil && s0 > 0 {
			sec = s0
			target = strings.TrimPrefix(rest[1], "client/")
		} else {
			return "", 0, fmt.Errorf("ベンチマーク指定が不正です (例: 'start all 5', 'start server 5', 'start <host> 5')")
		}
	}

	if sec <= 0 {
		sec = 5
	}
	if sec > 60 {
		sec = 60
	}
	return target, sec, nil
}

// parseBenchMetrics は "Score: 21288 pt | Cores: 16 | Time: 2.19s | Rate: 21287.6 Mops/s" から数値を構造化抽出します。
func parseBenchMetrics(res string) BenchMetrics {
	var m BenchMetrics
	if res == "" || res == "未計測" || strings.Contains(res, "No benchmark") {
		return m
	}
	parts := strings.Split(res, "|")
	for _, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), ":", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := strings.TrimSpace(kv[1])
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		switch k {
		case "score":
			if val, err := strconv.ParseUint(fields[0], 10, 32); err == nil {
				m.ScorePt = uint32(val)
				m.Measured = true
			}
		case "cores":
			if val, err := strconv.Atoi(fields[0]); err == nil {
				m.Cores = val
			}
		case "time":
			numStr := strings.TrimSuffix(fields[0], "s")
			if val, err := strconv.ParseFloat(numStr, 64); err == nil {
				m.ElapsedSec = val
			}
		case "rate":
			if val, err := strconv.ParseFloat(fields[0], 64); err == nil {
				m.Mops = val
				m.Measured = true
			}
		}
	}
	return m
}

// formatSingleBenchView は単体ノード用ベンチマーク制御ファイル (/server/ctl/bench, /client/<host>/ctl/bench) の表示を生成します。
func formatSingleBenchView(filePath, targetName, status string, measured bool, dur int, startTime time.Time, cpuPct float64, resultText string) []byte {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== 9P 単体ノード CPUベンチマーク (%s) ===\n", filePath))
	sb.WriteString(fmt.Sprintf("対象ノード: %s\n", targetName))

	switch status {
	case "running":
		elapsed := time.Since(startTime).Seconds()
		if elapsed > float64(dur) {
			elapsed = float64(dur)
		}
		sb.WriteString("現在の状態: RUNNING (計測中 🔥)\n")
		sb.WriteString(fmt.Sprintf("進捗状況:   %.1fs / %ds (現在のCPU負荷: %.1f%%)\n", elapsed, dur, cpuPct))
		if measured && resultText != "" && resultText != "未計測" {
			sb.WriteString(fmt.Sprintf("前回結果:   %s\n", resultText))
		} else {
			sb.WriteString("前回結果:   未計測\n")
		}
	case "finished":
		sb.WriteString("現在の状態: FINISHED (計測完了 🎉)\n")
		sb.WriteString(fmt.Sprintf("計測結果:   %s\n", resultText))
	default:
		sb.WriteString("現在の状態: IDLE (未計測 / 待機中)\n")
		sb.WriteString("計測結果:   未計測 (まだ一度もベンチマークが実行されていません)\n")
	}

	winPath := "P:\\" + strings.ReplaceAll(filePath, "/", "\\")
	sb.WriteString("\n--- [操作方法 (How to Operate)] ---\n")
	sb.WriteString("  ※ 複数ノードの一括起動や横並び比較は /cluster/bench から行えます。\n")
	sb.WriteString("  1. GUIエディタ (Ctrl+S送信) : 下の [入力欄] の '> ' の右側に書いて Ctrl+S で保存\n")
	sb.WriteString(fmt.Sprintf("  2. ファイル末尾への追記     : 最終行にコマンドを追記 (例: echo start 5 >> %s)\n", winPath))
	sb.WriteString(fmt.Sprintf("  3. ファイル全体の上書き     : コマンド1行で上書き   (例: Set-Content %s \"start 5\")\n", winPath))
	sb.WriteString("  【入力例】 start 5  (5秒間計測)   /   start 10  (10秒間計測)   /   start  (省略時は5秒)\n\n")

	sb.WriteString("--- [入力欄 (Input)] ---\n")
	sb.WriteString("↓ ここ（'> ' の右側）に 'start [秒数]' を入力して Ctrl+S で送信してください ↓\n")
	sb.WriteString("> \n")
	return []byte(sb.String())
}

// triggerBenchmarkByTarget は対象 ("all", "server", またはクライアントホスト名) に応じてベンチマークを起動します。
func triggerBenchmarkByTarget(target string, sec int) error {
	target = strings.ToLower(strings.TrimSpace(target))
	if sec <= 0 {
		sec = 5
	}
	if sec > 60 {
		sec = 60
	}

	switch target {
	case "all", "cluster", "*":
		_ = startBenchmarkTask(sec)

		clientMu.Lock()
		triggeredClients := 0
		for _, c := range activeClients {
			if time.Since(c.LastSeen) <= 30*time.Second {
				if c.BenchStatus != "running" || time.Since(c.BenchStartTime) >= time.Duration(c.BenchDuration+10)*time.Second {
					seq := nextCmdSeq()
					c.BenchStatus = "running"
					c.BenchDuration = sec
					c.BenchStartTime = time.Now()
					c.PendingCmds = append(c.PendingCmds, QueuedCmd{
						Seq: seq,
						Cmd: fmt.Sprintf("bench %d", sec),
					})
					triggeredClients++
				}
			}
		}
		clientMu.Unlock()
		addSystemLog(fmt.Sprintf("クラスタ一斉ベンチマーク開始 (%d秒間: サーバー + クライアント%d台)", sec, triggeredClients))
		return nil

	case "server":
		return startBenchmarkTask(sec)

	default:
		clientMu.Lock()
		c, ok := activeClients[target]
		if !ok {
			clientMu.Unlock()
			return fmt.Errorf("対象ノード %q が見つかりません ('all', 'server', または参加済みホスト名を指定してください)", target)
		}
		if time.Since(c.LastSeen) > 30*time.Second {
			clientMu.Unlock()
			return fmt.Errorf("クライアント %s は現在オフラインです", target)
		}
		if c.BenchStatus == "running" && time.Since(c.BenchStartTime) < time.Duration(c.BenchDuration+10)*time.Second {
			clientMu.Unlock()
			return fmt.Errorf("クライアント %s は現在ベンチマーク実行中です", target)
		}
		seq := nextCmdSeq()
		c.BenchStatus = "running"
		c.BenchDuration = sec
		c.BenchStartTime = time.Now()
		c.PendingCmds = append(c.PendingCmds, QueuedCmd{
			Seq: seq,
			Cmd: fmt.Sprintf("bench %d", sec),
		})
		clientMu.Unlock()
		addSystemLog(fmt.Sprintf("クライアント %s にベンチマーク開始命令を送信 (%d秒間, seq=%d)", target, sec, seq))
		return nil
	}
}

// computeCompositeWorkload は SpareLink 互換の複合 CPU マイクロベンチマークを実行します。
func computeCompositeWorkload(iterations int) uint64 {
	const bufferSize = 4096 // 4096 * 8 bytes = 32 KB (L1/L2 キャッシュ作業枠)
	var buffer [bufferSize]uint64

	s0 := uint64(0x123456789abcdef0)
	s1 := uint64(0x0fedcba987654321)
	s2 := uint64(0x5555555555555555)
	s3 := uint64(0xaaaaaaaaaaaaaaaa)

	f0 := 1.0001
	f1 := 0.9999

	outerLoops := iterations / bufferSize
	if outerLoops < 1 {
		outerLoops = 1
	}

	for loopIdx := 0; loopIdx < outerLoops; loopIdx++ {
		offset := (loopIdx * 17) & (bufferSize - 1)

		for i := 0; i < bufferSize; i++ {
			s0 ^= s0 << 23
			s1 ^= s1 >> 17
			s2 = s2*6364136223846793005 + 1
			s3 = bits.RotateLeft64(s3, 19) ^ s0

			mixed := (s0 + s1) ^ (s2 + s3)

			idx := (i + offset) & (bufferSize - 1)
			buffer[idx] += mixed

			f0 = f0*1.0000001 + 0.0000002
			f1 = f1*0.9999999 + 0.0000001
		}
	}

	return buffer[0] ^ buffer[bufferSize-1] ^ uint64(f0) ^ uint64(f1)
}

func startBenchmarkTask(sec int) error {
	benchMu.Lock()
	if benchStatus == "running" {
		benchMu.Unlock()
		return fmt.Errorf("サーバーベンチマークは既に実行中です")
	}
	if sec <= 0 {
		sec = 5
	}
	if sec > 60 {
		sec = 60
	}
	threads := runtime.NumCPU()
	if threads < 1 {
		threads = 1
	}
	benchStatus = "running"
	benchDurationSec = sec
	benchStartTime = time.Now()
	benchThreads = threads
	benchMu.Unlock()

	addBenchLog(fmt.Sprintf("ベンチマーク開始要求: %d秒間 / %d並列スレッド (SpareLink 複合ワークロード)", sec, threads))
	addSystemLog(fmt.Sprintf("CPUベンチマーク開始 (%d秒間, %dスレッド)", sec, threads))

	go func() {
		stop := make(chan struct{})
		time.AfterFunc(time.Duration(sec)*time.Second, func() {
			close(stop)
		})

		var totalOps uint64
		var wg sync.WaitGroup

		const chunkSize = 4096 * 8
		const opsPerIteration = 7

		for t := 0; t < threads; t++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var localOps uint64
				for {
					select {
					case <-stop:
						atomic.AddUint64(&totalOps, localOps)
						return
					default:
						_ = computeCompositeWorkload(chunkSize)
						localOps += uint64(chunkSize * opsPerIteration)
					}
				}
			}()
		}

		wg.Wait()
		elapsed := time.Since(benchStartTime)
		elapsedSecs := elapsed.Seconds()
		if elapsedSecs < 0.001 {
			elapsedSecs = 0.001
		}

		opsPerSec := float64(totalOps) / elapsedSecs
		mops := opsPerSec / 1_000_000.0
		scorePt := uint32(math.Round(mops))
		if scorePt < 10 {
			scorePt = 10
		}

		benchMu.Lock()
		benchStatus = "finished"
		benchMeasured = true
		benchScorePt = scorePt
		benchScoreMops = mops
		benchElapsedSec = elapsedSecs
		res := fmt.Sprintf("Score: %d pt | Cores: %d | Time: %.2fs | Rate: %.1f Mops/s | Ops: %d",
			scorePt, threads, elapsedSecs, mops, totalOps)
		benchResultText = res
		benchMu.Unlock()

		addBenchLog(fmt.Sprintf("計測完了: %s", res))
		addSystemLog(fmt.Sprintf("ベンチマーク完了 (%s)", res))
	}()
	return nil
}

func getServerBenchContent() []byte {
	benchMu.Lock()
	st := benchStatus
	meas := benchMeasured
	dur := benchDurationSec
	startT := benchStartTime
	res := benchResultText
	benchMu.Unlock()

	return formatSingleBenchView("server/ctl/bench", "server", st, meas, dur, startT, getServerCPU(), res)
}

func handleServerBenchWrite(data []byte) error {
	cmdStr, err := extractUnifiedCommand(data)
	if err != nil {
		return err
	}
	target, sec, err := parseBenchCommand(cmdStr, "server")
	if err != nil {
		return err
	}
	return triggerBenchmarkByTarget(target, sec)
}
