package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/update"
	"github.com/xmonader/janetcode/internal/xdg"
)

// uninstallMode is --uninstall: the binary goes when janetcode installed
// it, and the config and data directories go only with --purge. Every
// path removed is printed, and so is every one left behind.
func uninstallMode(w io.Writer, purge bool) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	cfg, err := xdg.ConfigDir(os.Getenv)
	if err != nil {
		return err
	}
	data, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return err
	}
	return uninstall(w, exe, version, []string{cfg, data}, purge)
}

func uninstall(w io.Writer, exe, version string, dirs []string, purge bool) error {
	plan, err := update.PlanUninstall(exe, version, dirs, purge)
	if err != nil {
		return err
	}
	done, err := update.Uninstall(plan)
	for _, line := range done {
		fmt.Fprintln(w, line)
	}
	if err != nil {
		return err
	}
	for _, d := range plan.Kept {
		fmt.Fprintf(w, "kept %s (--uninstall --purge removes it; --backup <file> saves a copy first)\n", d)
	}
	return nil
}
