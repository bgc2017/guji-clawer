//go:build windows

package main

import "syscall"

var procSetConsoleOutputCP = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP")

func setConsoleUTF8() {
	_, _, _ = procSetConsoleOutputCP.Call(65001)
}
