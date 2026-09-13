//go:build unix

package main

import "syscall"

func bgProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
