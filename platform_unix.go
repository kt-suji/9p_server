//go:build !windows

package main

import (
	"context"
	"os/exec"
)

// buildPlatformShellCommand は Linux / Unix 向けに /bin/sh -c でコマンドを実行する Cmd を返します。
func buildPlatformShellCommand(ctx context.Context, cmdStr string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
}

// decodeConsoleOutput は Linux / Unix では標準で UTF-8 のためそのまま文字列化します。
func decodeConsoleOutput(raw []byte) string {
	return string(raw)
}
