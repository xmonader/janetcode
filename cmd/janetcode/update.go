package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/xmonader/janetcode/internal/update"
)

// selfUpdate is --update: the running binary is replaced by the latest
// release, or left alone when it is that release already.
func selfUpdate() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding this binary: %w", err)
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := update.Run(ctx, nil, update.Base(), version, exe)
	if err != nil {
		return err
	}
	if !res.Updated {
		fmt.Printf("janetcode %s is up to date (latest release %s)\n", res.From, res.To)
		return nil
	}
	fmt.Printf("updated %s: %s → %s\n", res.Path, res.From, res.To)
	// The receipt install.sh left names the bytes it wrote; it follows
	// the binary so --uninstall still recognises it.
	if rc, err := update.ReadReceipt(exe); err == nil && rc != nil {
		if err := update.WriteReceipt(exe, res.To); err != nil {
			fmt.Fprintf(os.Stderr, "janetcode: could not update %s: %v\n", update.ReceiptName(exe), err)
		}
	}
	return nil
}
