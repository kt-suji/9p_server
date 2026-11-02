package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ServerVersion         int64 = 2026110201
	RequiredClientVersion int64 = 2026110201
	versionMu             sync.RWMutex
)

// GetServerVersion はサーバー自身のビルドバージョンを返します。
func GetServerVersion() int64 {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return ServerVersion
}

// GetRequiredClientVersion は接続中クライアントに要求する最低バージョンを返します。
func GetRequiredClientVersion() int64 {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return RequiredClientVersion
}

// SetRequiredClientVersion は要求クライアントバージョンを更新し、全クライアントに自動更新を要求します。
func SetRequiredClientVersion(v int64) {
	versionMu.Lock()
	RequiredClientVersion = v
	versionMu.Unlock()
	addSystemLog(fmt.Sprintf("★ 要求クライアントバージョンが v%d に更新されました (全クライアントの自動再起動が要求されます)", v))
}

func init() {
	RegisterFeature(FeatureModule{
		Order:   25,
		Name:    "version",
		Summary: "/server/version            [RW]    クラスタ/クライアント バージョン管理 & 全自動更新トリガー (書込例: 'bump' または '2026110202')",
		ServerNodes: func(cfg *Config) []NodeDefinition {
			return []NodeDefinition{
				{
					Path: "server/version",
					ReadFn: func() ([]byte, error) {
						return renderServerVersionView(), nil
					},
					WriteFn: func(data []byte) error {
						cmdStr, err := extractUnifiedCommand(data)
						if err != nil {
							return err
						}
						return handleVersionCommand(cmdStr)
					},
				},
			}
		},
	})
}

func renderServerVersionView() []byte {
	srvVer := GetServerVersion()
	reqVer := GetRequiredClientVersion()

	var sb strings.Builder
	sb.WriteString("=== 9P クラスタ バージョン管理 & 自動更新 (server/version) ===\n")
	sb.WriteString(fmt.Sprintf("サーバー内部バージョン:         v%d\n", srvVer))
	sb.WriteString(fmt.Sprintf("要求クライアントバージョン:     v%d\n", reqVer))
	sb.WriteString("共有バイナリ同期先:             G:\\マイドライブ\\zemi_9p\\bin\\9p_client_gui.exe\n")
	sb.WriteString(fmt.Sprintf("確認日時:                       %s\n\n", time.Now().Format("2006-01-02 15:04:05")))

	clientMu.RLock()
	keys := getSortedClientKeys()
	sb.WriteString(fmt.Sprintf("--- [接続クライアント一覧 (全 %d 台)] ---\n", len(keys)))
	if len(keys) == 0 {
		sb.WriteString("  (接続中のクライアントはありません)\n")
	} else {
		for _, h := range keys {
			c := activeClients[h]
			status := "最新 ✓"
			if c.ClientVersion == 0 {
				status = "未報告 (旧バージョン) ⚠️"
			} else if c.ClientVersion < reqVer {
				status = fmt.Sprintf("要更新 (現在: v%d < 要求: v%d) ⏳", c.ClientVersion, reqVer)
			}
			online := "ONLINE"
			if time.Since(c.LastSeen) > 30*time.Second {
				online = "OFFLINE"
			}
			sb.WriteString(fmt.Sprintf("  - %-18s [%s] v%-10d : %s (最終確認: %s)\n",
				h, online, c.ClientVersion, status, c.LastSeen.Format("15:04:05")))
		}
	}
	clientMu.RUnlock()

	sb.WriteString("\n--- [操作方法 (管理者向け: 全クライアントの一斉自動更新)] ---\n")
	sb.WriteString("  1. G:\\マイドライブ\\zemi_9p\\bin\\ に新しい 9p_client_gui.exe をビルド・配置します\n")
	sb.WriteString("  2. 下の [入力欄] に新しいバージョン番号 (例: 2026110202 または bump) を入力して Ctrl+S 保存します\n")
	sb.WriteString("  3. 接続中の全クライアントPCがGoogle Drive同期を検知し、安全にアンマウントして自動再起動します\n\n")

	sb.WriteString("--- [入力欄 (Input)] ---\n")
	sb.WriteString("↓ ここ（'> ' の右側）に新しいバージョン番号 (または bump) を入力して Ctrl+S ↓\n")
	sb.WriteString("> \n")

	return []byte(sb.String())
}

func handleVersionCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return fmt.Errorf("コマンドが空です")
	}

	lower := strings.ToLower(cmd)
	if lower == "bump" || lower == "+1" {
		cur := GetRequiredClientVersion()
		SetRequiredClientVersion(cur + 1)
		return nil
	}

	newVer, err := strconv.ParseInt(cmd, 10, 64)
	if err != nil {
		return fmt.Errorf("無効なバージョン形式です (数字または 'bump' を入力してください): %s", cmd)
	}

	if newVer <= 0 {
		return fmt.Errorf("バージョン番号は正の整数である必要があります")
	}

	SetRequiredClientVersion(newVer)
	return nil
}
