//go:build windows

package main

import "syscall"

// createNoWindow is CREATE_NO_WINDOW, which package syscall does not name.
const createNoWindow = 0x08000000

// bgProcAttr detaches the --bg child from the terminal it was started in,
// as Setsid does on Unix. Without flags it shared the parent's console, so
// closing that window, or ctrl+c typed in it once the parent had returned,
// ended the background run. Its own hidden console also gives the commands
// it runs somewhere to attach without a window popping up.
func bgProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow}
}
