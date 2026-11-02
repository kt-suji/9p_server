package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NodeDefinition は 9P 仮想ファイルツリー上の1つのファイルノード定義を表します。
type NodeDefinition struct {
	Path    string
	ReadFn  DynamicReader
	WriteFn DynamicWriter
}

// FeatureModule は 9P ファイルシステム上の1つの機能単位（モジュール）を表します。
// 新しい機能を追加したい場合は、新しい node_xxx.go ファイルを作成して init() で RegisterFeature を呼ぶだけで、
// 自動的にサーバーツリー・クライアント動的ツリー・/help ガイドに統合されます。
type FeatureModule struct {
	Order       int                                     // 登録・ヘルプ表示順
	Name        string                                  // 機能識別名 (例: "cluster", "bench", "exec")
	Summary     string                                  // /help に自動出力される機能概要
	ServerNodes func(cfg *Config) []NodeDefinition      // サーバー起動時に登録する仮想ファイル群
	ClientNodes func(ci *ClientInfo, tb *TreeBuilder)   // クライアント参加時に client/<host>/ 下へ動的生成する仮想ファイル群
}

var (
	featureMu       sync.RWMutex
	featureModules  []FeatureModule
	globalTreeBuilder *TreeBuilder

	// サーバー再起動を跨いでも単調増加するコマンドシーケンス番号
	globalCmdSeq = uint64(time.Now().UnixMilli())
)

// RegisterFeature は機能モジュールを登録します。各 node_*.go の init() から呼び出されます。
func RegisterFeature(mod FeatureModule) {
	featureMu.Lock()
	defer featureMu.Unlock()
	featureModules = append(featureModules, mod)
	sort.SliceStable(featureModules, func(i, j int) bool {
		return featureModules[i].Order < featureModules[j].Order
	})
}

// GetRegisteredFeatures は登録済みモジュールのスライスを返します。
func GetRegisteredFeatures() []FeatureModule {
	featureMu.RLock()
	defer featureMu.RUnlock()
	out := make([]FeatureModule, len(featureModules))
	copy(out, featureModules)
	return out
}

func nextCmdSeq() uint64 {
	return atomic.AddUint64(&globalCmdSeq, 1)
}

// SetupAllNodes は登録されているすべての FeatureModule からサーバーノードを収集して 9P ツリーを構築します。
func SetupAllNodes(tb *TreeBuilder, cfg *Config) {
	globalTreeBuilder = tb
	addSystemLog("9Pサーバー分散リソースツリー初期化完了 (モジュラーアーキテクチャ)")

	for _, mod := range GetRegisteredFeatures() {
		if mod.ServerNodes == nil {
			continue
		}
		for _, node := range mod.ServerNodes(cfg) {
			if node.WriteFn != nil {
				_ = tb.RegisterRW(node.Path, node.ReadFn, node.WriteFn)
			} else {
				_ = tb.RegisterRead(node.Path, node.ReadFn)
			}
		}
	}
}

// registerClientNodes は新規クライアント参加時に、全 FeatureModule の ClientNodes フックを実行します。
func registerClientNodes(ci *ClientInfo) {
	if globalTreeBuilder == nil {
		return
	}
	for _, mod := range GetRegisteredFeatures() {
		if mod.ClientNodes != nil {
			mod.ClientNodes(ci, globalTreeBuilder)
		}
	}
}

// extractUnifiedCommand はすべての制御ファイル (/cluster/status, /cluster/bench, ctl/bench, ctl/exec 等) で
// 共通の「3つの操作方法」からコマンド文字列を抽出する統一パーサーです。
//  1. GUIエディタ等で「--- [入力欄 (Input)] ---」の「> 」の右側に入力して Ctrl+S 保存
//  2. ファイルの末尾に行を追記して保存 (echo ... >> file)
//  3. ファイル全体をコマンド文字列だけで上書き (Set-Content / write_to_file / echo ... > file)
func extractUnifiedCommand(data []byte) (string, error) {
	// 1. UTF-8 BOM を安全に除去
	rawStr := strings.TrimPrefix(string(data), "\ufeff")
	raw := strings.ReplaceAll(rawStr, "\r\n", "\n")
	trimmedAll := strings.TrimSpace(raw)
	if trimmedAll == "" {
		return "", fmt.Errorf("コマンドが空です")
	}

	lines := strings.Split(raw, "\n")
	firstLine := strings.TrimSpace(lines[0])

	// ケースA: テンプレートヘッダ ("===") が無い場合 → 上書きモード (CLI / スクリプト / Set-Content)
	if !strings.HasPrefix(firstLine, "===") {
		for i := len(lines) - 1; i >= 0; i-- {
			t := strings.TrimSpace(lines[i])
			if t != "" {
				if strings.HasPrefix(t, ">") {
					t = strings.TrimSpace(strings.TrimPrefix(t, ">"))
				}
				if t != "" && !strings.HasPrefix(t, "↓") && !strings.HasPrefix(t, "※") {
					return t, nil
				}
			}
		}
		return "", fmt.Errorf("コマンドが空です")
	}

	// ケースB: テンプレートヘッダ ("===") がある場合 → GUIでの Ctrl+S 保存 または 末尾追記
	inInputSection := false
	var promptCandidates []string
	var inputSectionCandidates []string
	var appendedCandidates []string

	for _, l := range lines {
		t := strings.TrimSpace(l)

		// 入力セクションの開始判定 ("--- [入力欄" や "--- [クイック入力" など)
		if strings.HasPrefix(t, "--- [入力欄") || strings.HasPrefix(t, "--- [クイック入力") || strings.HasPrefix(t, "--- [操作・入力") {
			inInputSection = true
			continue
		}
		// 別のセクション開始で入力セクション終了
		if strings.HasPrefix(t, "--- [") {
			inInputSection = false
			continue
		}

		// "> " プロンプト行の解析
		if strings.HasPrefix(t, ">") {
			cmd := strings.TrimSpace(strings.TrimPrefix(t, ">"))
			if cmd != "" {
				promptCandidates = append(promptCandidates, cmd)
			}
			continue
		}

		// 入力セクション内での直接記述 (案内行 "↓" "※" は除外)
		if inInputSection {
			if t != "" && !strings.HasPrefix(t, "↓") && !strings.HasPrefix(t, "※") {
				inputSectionCandidates = append(inputSectionCandidates, t)
			}
		}
	}

	// 1. プロンプト行 ("> ...") に入力があれば、最新（最下部）のものを採用
	if len(promptCandidates) > 0 {
		return promptCandidates[len(promptCandidates)-1], nil
	}

	// 2. 入力セクション内に直接書かれた行があれば採用
	if len(inputSectionCandidates) > 0 {
		return inputSectionCandidates[len(inputSectionCandidates)-1], nil
	}

	// 3. ファイル末尾への追記行 (echo ... >> file) のチェック
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, ">") {
			cmd := strings.TrimSpace(strings.TrimPrefix(t, ">"))
			if cmd != "" {
				return cmd, nil
			}
			break
		}
		if !strings.HasPrefix(t, "↓") && !strings.HasPrefix(t, "※") && !strings.HasPrefix(t, "---") {
			appendedCandidates = append(appendedCandidates, t)
			break
		}
	}

	if len(appendedCandidates) > 0 {
		return appendedCandidates[0], nil
	}

	return "", fmt.Errorf("入力欄 ('> ' の右側) が空です。コマンドを入力してから保存 (Ctrl+S) してください")
}
