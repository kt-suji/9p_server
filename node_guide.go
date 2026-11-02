package main

import (
	"fmt"
	"strings"
)

func init() {
	RegisterFeature(FeatureModule{
		Order:   10,
		Name:    "guide",
		Summary: "/hello, /help               [Read]  稼働確認メッセージ & AI/人間向け操作リファレンス",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "hello",
					ReadFn: func() ([]byte, error) {
						return []byte("Hello from Cross-Platform 9P Distributed System! (Plan 9 'Everything is a File' for Humans & AI Agents)\n"), nil
					},
				},
				{
					Path: "help",
					ReadFn: func() ([]byte, error) {
						return formatHelpGuide(), nil
					},
				},
			}
		},
	})
}

func formatHelpGuide() []byte {
	var sb strings.Builder
	sb.WriteString("=== 9P 分散リソース管理 & AI制御プレーン 操作ガイド (/help) ===\n")
	sb.WriteString("本システムでは、クラスタ内のすべての監視・ベンチマーク・仮想ターミナルを「ファイル」として操作します。\n\n")
	sb.WriteString("======================================================================\n")
	sb.WriteString("■ 1. すべての制御ファイルで共通の「3つの操作方法」\n")
	sb.WriteString("======================================================================\n")
	sb.WriteString("制御ファイル (/cluster/bench, */ctl/bench, */ctl/exec 等) はすべて同じ方法で操作できます：\n\n")
	sb.WriteString("  [方法A: GUIエディタ / メモ帳で開いて Ctrl+S 送信]\n")
	sb.WriteString("    ファイル末尾にある「--- [入力欄 (Input)] ---」の「> 」の右側にコマンドを入力し、\n")
	sb.WriteString("    Ctrl+S（または「💾 保存・実行」ボタン）を押します。\n")
	sb.WriteString("    ・ベンチマーク例 : > start all 5   または   > start server 5\n")
	sb.WriteString("    ・ターミナル例   : > ipconfig      または   > uname -a\n\n")
	sb.WriteString("  [方法B: ファイルの末尾へ追記 (>>)]\n")
	sb.WriteString("    ファイルの最終行にコマンドを追記して保存しても実行されます。\n")
	sb.WriteString("    ・例: echo start all 5 >> P:\\cluster\\bench\n")
	sb.WriteString("    ・例: echo ipconfig >> P:\\server\\ctl\\exec\n\n")
	sb.WriteString("  [方法C: AIエージェント / CLI / スクリプトからの全体上書き (>)]\n")
	sb.WriteString("    ファイル全体をコマンド文字列1行だけで上書きしても実行されます。\n")
	sb.WriteString("    ・例 (PowerShell) : Set-Content P:\\cluster\\bench \"start all 5\"\n")
	sb.WriteString("    ・例 (AI Tool)    : write_to_file(\"P:\\\\server\\\\ctl\\\\exec\", \"ipconfig\")\n\n")
	sb.WriteString("======================================================================\n")
	sb.WriteString("■ 2. 現在ロードされている機能モジュール一覧 (自動検出)\n")
	sb.WriteString("======================================================================\n")
	for _, mod := range GetRegisteredFeatures() {
		if mod.Summary != "" {
			sb.WriteString(fmt.Sprintf("  [%s]\n    %s\n", mod.Name, strings.ReplaceAll(mod.Summary, "\n", "\n    ")))
		}
	}
	return []byte(sb.String())
}
