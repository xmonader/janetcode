//go:build windows

package main

import "syscall"

func bgProcAttr() *syscall.SysProcAttr {
	return nil
}
