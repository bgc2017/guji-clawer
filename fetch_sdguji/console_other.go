//go:build !windows

package main

// initConsole 在非 Windows 平台不需要处理代码页。
func initConsole() {}

func platformNL() string { return "\n" }
