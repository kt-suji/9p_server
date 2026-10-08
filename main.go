package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"aqwari.net/net/styx"
	"github.com/shirou/gopsutil/v3/cpu"
)

func main() {
	// styx.HandlerFunc は *styx.Session を1つ受け取る関数です
	handler := styx.HandlerFunc(func(s *styx.Session) {
		// クライアントから届くリクエストをループで1つずつ処理する
		for s.Next() {
			switch req := s.Request().(type) {

			// 探索リクエスト (Walk)
			case styx.Twalk:
				req.Rwalk(nil, nil)

			// オープンリクエスト (Open)
			case styx.Topen:
				switch req.Path() {
				case "/hello":
					message := "Hello from Windows 9P Server!\n"
					req.Ropen(strings.NewReader(message), nil)

				case "/sys/cpu":
					percents, err := cpu.Percent(0, false)
					if err != nil || len(percents) == 0 {
						req.Rerror("CPU error")
						continue
					}
					cpuString := fmt.Sprintf("CPU: %.1f%%\n", percents[0])
					req.Ropen(strings.NewReader(cpuString), nil)

				default:
					req.Ropen(nil, os.ErrNotExist)
				}
			}
		}
	})

	// ネットワークの待ち受けと起動
	listener, err := net.Listen("tcp", ":5640")
	if err != nil {
		log.Fatalf("[ERROR] ポートのバインドに失敗しました: %v", err)
	}
	log.Println("[INFO] Windows 9P Serverがポート5640で待機中...")

	srv := &styx.Server{
		Handler: handler,
	}
	if err := srv.Serve(listener); err != nil {
		log.Fatalf("[ERROR] 9Pサーバーの起動に失敗しました: %v", err)
	}
}
