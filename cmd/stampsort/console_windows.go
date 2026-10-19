package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachConsole：exe 是 GUI 子系统（双击不弹黑窗），在 cmd/PowerShell 里用命令行时
// 挂到父进程的控制台上输出。输出已被重定向（> log.txt）时保持不变。
func attachConsole() {
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != windows.InvalidHandle {
		return
	}
	const attachParentProcess = ^uintptr(0)
	if r, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole").Call(attachParentProcess); r == 0 {
		return // 不是从控制台启动的
	}
	if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = f
	}
}
