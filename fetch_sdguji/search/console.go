package main

import "runtime"

// Windows 下 cmd 默认不是 UTF-8 代码页，先切到 65001，避免中文输出乱码。
func init() {
	if runtime.GOOS == "windows" {
		setConsoleUTF8()
	}
}

func nl() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}
