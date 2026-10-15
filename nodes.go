package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

var (
	benchMu     sync.Mutex
	benchStatus = "idle"
	benchResult = "none"
)

type NodeDefinition struct {
	Path    string
	ReadFn  DynamicReader
	WriteFn DynamicWriter
}

var nodeRegistry = []NodeDefinition{
	// 静的ファイル
	{
		Path: "hello",
		ReadFn: func() ([]byte, error) {
			return []byte("Hello from Windows 9P!\n"), nil
		},
	},

	// システム情報（Read Only）
	{
		Path: "sys/cpu",
		ReadFn: func() ([]byte, error) {
			p, err := cpu.Percent(0, false)
			if err != nil || len(p) == 0 {
				return nil, err
			}
			return []byte(fmt.Sprintf("CPU: %.1f%%\n", p[0])), nil
		},
	},
	{
		Path: "sys/mem",
		ReadFn: func() ([]byte, error) {
			v, err := mem.VirtualMemory()
			if err != nil {
				return nil, err
			}
			return []byte(fmt.Sprintf("Mem: %.1f%%\n", v.UsedPercent)), nil
		},
	},

	// ベンチマーク制御（Readで状態・使い方、Writeで実行）
	{
		Path: "ctl/bench",
		ReadFn: func() ([]byte, error) {
			benchMu.Lock()
			st := benchStatus
			res := benchResult
			benchMu.Unlock()

			msg := fmt.Sprintf("Status: %s\nLast Result: %s\nUsage: write 'start' to run CPU benchmark\n", st, res)
			return []byte(msg), nil
		},
		WriteFn: func(data []byte) error {
			cmd := strings.TrimSpace(string(data))
			log.Printf("[CTL] bench 書き込み受信: %q", cmd)

			if cmd == "start" {
				benchMu.Lock()
				if benchStatus == "running" {
					benchMu.Unlock()
					return fmt.Errorf("benchmark already running")
				}
				benchStatus = "running"
				benchMu.Unlock()

				// 非同期でベンチマーク処理を実行（5秒間の計算タスク）
				go func() {
					log.Println("--> [BENCH] 計測開始...")
					start := time.Now()

					// CPU負荷の模擬計算
					count := 0
					for i := 0; i < 500000000; i++ {
						count += i % 3
					}
					elapsed := time.Since(start)

					benchMu.Lock()
					benchStatus = "finished"
					benchResult = fmt.Sprintf("elapsed: %v (calc: %d)", elapsed, count)
					benchMu.Unlock()

					log.Printf("--> [BENCH] 完了! 所要時間: %v", elapsed)
				}()
			}
			return nil
		},
	},
}

func SetupAllNodes(tb *TreeBuilder) {
	for _, node := range nodeRegistry {
		if node.WriteFn != nil {
			tb.RegisterRW(node.Path, node.ReadFn, node.WriteFn)
		} else {
			tb.RegisterRead(node.Path, node.ReadFn)
		}
	}
}
