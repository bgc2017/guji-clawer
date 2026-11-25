//go:build windows

package main

import "syscall"

// initConsole 把控制台输入输出切到 UTF-8（65001）。
// Windows 的 cmd/PowerShell 默认用 GBK 代码页，直接打印 UTF-8 中文会变乱码。
func initConsole() {
	k := syscall.NewLazyDLL("kernel32.dll")
	out := k.NewProc("SetConsoleOutputCP")
	in := k.NewProc("SetConsoleCP")
	_, _, _ = out.Call(65001)
	_, _, _ = in.Call(65001)
}

// platformNL 在 Windows 上用 CRLF，保证记事本等老工具打开 txt 不会挤成一行。
func platformNL() string { return "\r\n" }
