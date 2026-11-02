//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32Server          = windows.NewLazySystemDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32Server.NewProc("MultiByteToWideChar")
)

// buildPlatformShellCommand は Windows 向けに非表示コンソールで cmd.exe を起動する Cmd を返します。
func buildPlatformShellCommand(ctx context.Context, cmdStr string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", cmdStr)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}

// decodeConsoleOutput は Windows CP932 (Shift-JIS) 出力を UTF-8 に自動変換します。
func decodeConsoleOutput(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if utf8.Valid(raw) {
		return string(raw)
	}
	const cp932 = 932
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(cp932),
		0,
		uintptr(unsafe.Pointer(&raw[0])),
		uintptr(len(raw)),
		0,
		0,
	)
	if n == 0 {
		return string(raw)
	}
	wbuf := make([]uint16, n)
	procMultiByteToWideChar.Call(
		uintptr(cp932),
		0,
		uintptr(unsafe.Pointer(&raw[0])),
		uintptr(len(raw)),
		uintptr(unsafe.Pointer(&wbuf[0])),
		n,
	)
	return windows.UTF16ToString(wbuf)
}
