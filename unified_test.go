package main

import (
	"strings"
	"testing"

	"github.com/knusbaum/go9p/proto"
)

func TestExtractUnifiedCommand(t *testing.T) {
	tmpl := "=== 9P 遷移対応 Webブラウザ & 検索エンジン (net/web) ===\n" +
		"状態 (Status):     FINISHED (完了 ✓)\n" +
		"現在地 (Target):   https://example.com\n" +
		"タイトル (Title):  Example\n\n" +
		"--- [入力欄 (Quick Input)] ---\n" +
		"↓ ここに入力して Ctrl+S ↓\n" +
		"> \n\n" +
		"--- [本文 / 検索結果 (Content)] ---\n" +
		"This is sample content.\n" +
		"Line 2\n" +
		"Line 3\n\n" +
		"--- [入力欄 (Input)] ---\n" +
		"↓ ここに入力して Ctrl+S ↓\n" +
		"> "

	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "CLI overwrite single word",
			input:    "Ryzen",
			expected: "Ryzen",
		},
		{
			name:     "CLI overwrite with prompt prefix",
			input:    "> Ryzen",
			expected: "Ryzen",
		},
		{
			name:     "GUI type at bottom prompt",
			input:    strings.Replace(tmpl, "\n> ", "\n> Ryzen", 2),
			expected: "Ryzen",
		},
		{
			name:     "GUI type at top prompt only",
			input:    strings.Replace(tmpl, "> \n\n--- [本文", "> Ryzen\n\n--- [本文", 1),
			expected: "Ryzen",
		},
		{
			name:     "GUI type on newline in bottom input section",
			input:    tmpl + "\nRyzen",
			expected: "Ryzen",
		},
		{
			name:     "UTF-8 BOM with bottom prompt",
			input:    "\ufeff" + strings.Replace(tmpl, "\n> ", "\n> Ryzen", 2),
			expected: "Ryzen",
		},
		{
			name:    "Untouched template should return error",
			input:   tmpl,
			wantErr: true,
		},
		{
			name:     "User deleted prompt and typed in input section",
			input:    strings.Replace(tmpl, "> ", "Ryzen 9950X", 1),
			expected: "Ryzen 9950X",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := extractUnifiedCommand([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got command %q", cmd)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, cmd)
			}
		})
	}
}

func TestDynamicFileChunkedWrite(t *testing.T) {
	var received string
	stat := &proto.Stat{Name: "test"}
	df := newDynamicRWFile(stat, nil, func(data []byte) error {
		received = string(data)
		return nil
	})

	fid := uint64(100)
	if err := df.Open(fid, proto.Owrite); err != nil {
		t.Fatalf("open failed: %v", err)
	}

	// 3 chunks write
	chunk1 := []byte("Hello, ")
	chunk2 := []byte("Plan 9 ")
	chunk3 := []byte("World!")

	if n, err := df.Write(fid, 0, chunk1); err != nil || n != uint32(len(chunk1)) {
		t.Fatalf("write chunk1 failed: n=%d, err=%v", n, err)
	}
	if n, err := df.Write(fid, uint64(len(chunk1)), chunk2); err != nil || n != uint32(len(chunk2)) {
		t.Fatalf("write chunk2 failed: n=%d, err=%v", n, err)
	}
	if n, err := df.Write(fid, uint64(len(chunk1)+len(chunk2)), chunk3); err != nil || n != uint32(len(chunk3)) {
		t.Fatalf("write chunk3 failed: n=%d, err=%v", n, err)
	}

	// Prior to Close, writeFunc must NOT have executed yet
	if received != "" {
		t.Fatalf("expected received to be empty before close, got %q", received)
	}

	// Close commits the write
	if err := df.Close(fid); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	expected := "Hello, Plan 9 World!"
	if received != expected {
		t.Fatalf("expected %q, got %q", expected, received)
	}
}

func TestVersionControl(t *testing.T) {
	origVer := GetRequiredClientVersion()
	defer SetRequiredClientVersion(origVer)

	// 1. Initial view contains versions
	view := string(renderServerVersionView())
	if !strings.Contains(view, "server/version") {
		t.Fatalf("expected view to contain header, got %s", view)
	}

	// 2. Bump command
	if err := handleVersionCommand("bump"); err != nil {
		t.Fatalf("handle bump failed: %v", err)
	}
	if GetRequiredClientVersion() != origVer+1 {
		t.Fatalf("expected version %d, got %d", origVer+1, GetRequiredClientVersion())
	}

	// 3. Set specific version
	if err := handleVersionCommand("2026110299"); err != nil {
		t.Fatalf("handle set version failed: %v", err)
	}
	if GetRequiredClientVersion() != 2026110299 {
		t.Fatalf("expected version 2026110299, got %d", GetRequiredClientVersion())
	}

	// 4. Invalid version format
	if err := handleVersionCommand("invalid_abc"); err == nil {
		t.Fatalf("expected error on invalid version, got nil")
	}
}
