// The openclaw harness: it runs against a profile and a workspace of its
// own rather than in the task directory, so seeding, harvesting and
// cleaning up around one run are its own business.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// invoke runs one prompt on a harness in dir. With a session, the first
// prompt creates the harness's session under the given id and later ones
// resume it, so the conversation carries over the way it does for a user.
// openclaw scaffolds these into any workspace it uses; they are its own
// files, not the task's, and must not be copied back as results.
var openclawScaffold = map[string]bool{
	"AGENTS.md": true, "BOOTSTRAP.md": true, "IDENTITY.md": true,
	"SOUL.md": true, "USER.md": true,
}

// openclawWorkspaceDir is one directory reused for every task in a run.
//
// Pointing openclaw at each task directory in turn does not work: it
// attests a workspace on first use and refuses to start when that
// directory later disappears — which is exactly what happens when the
// previous task's directory is cleaned up. So the workspace is stable and
// the task moves through it: seeded in before the run, results copied back
// out after, openclaw's own scaffolding left behind.
func openclawWorkspaceDir(task string) (string, error) {
	dir := filepath.Join(os.TempDir(), "openclaw-bench", evalSanitize(task))
	return dir, os.MkdirAll(dir, 0o755)
}

// openclawTaskProfile is a config namespace of its own for each task.
// openclaw keeps exactly one workspace setting per profile, and this
// benchmark runs tasks concurrently: with a shared profile the tasks
// overwrite each other's workspace between the patch and the run, and each
// one then works in some other task's directory. That is what made every
// task fail while the same command by hand succeeded.
func openclawTaskProfile(task string) string {
	return openclawProfile + "-" + evalSanitize(task)
}

// openclawSeed empties the shared workspace of the last task's files and
// copies this one's in.
func openclawSeed(ws, taskDir string) error {
	entries, err := os.ReadDir(ws)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if openclawScaffold[e.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(ws, e.Name())); err != nil {
			return err
		}
	}
	return copyTree(taskDir, ws, nil)
}

// openclawHarvest copies what the run produced back to the task directory,
// where the check will look for it.
func openclawHarvest(ws, taskDir string) error {
	return copyTree(ws, taskDir, openclawScaffold)
}

// copyTree copies a directory's contents, skipping top-level names in skip.
func copyTree(from, to string, skip map[string]bool) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(from, path)
		if rerr != nil || rel == "." {
			return rerr
		}
		if skip != nil {
			if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; skip[top] {
				return nil
			}
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		info, _ := d.Info()
		mode := fs.FileMode(0o644)
		if info != nil {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(dest, data, mode)
	})
}

// openclawCleanup removes what one task's run left behind: its workspace
// and its profile under ~/.openclaw-<profile>. Every task gets a profile of
// its own with a fresh suffix per run, so nothing reclaims them otherwise;
// three runs of twenty tasks had left 116 of them in the home directory.
// Only profiles this benchmark named are touched.
func openclawCleanup(profile, ws string) {
	if os.Getenv("HARNESSBENCH_KEEP") != "" {
		return
	}
	if ws != "" {
		_ = os.RemoveAll(ws)
	}
	if !strings.HasPrefix(profile, openclawProfile+"-") {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(home, ".openclaw-"+profile))
}

// openclawConfigure writes everything one task's profile needs: the
// endpoint under test, and the workspace it must work in.
func openclawConfigure(ctx context.Context, profile string, cfg runConfig, ws string) error {
	patch := map[string]any{
		"agents": map[string]any{"defaults": map[string]any{"workspace": ws}},
		"models": map[string]any{
			"mode": "merge",
			"providers": map[string]any{
				"bench": map[string]any{
					"baseUrl": cfg.BaseURL,
					"apiKey":  cfg.Key,
					"api":     "openai-completions",
					"models": []any{map[string]any{
						"id": cfg.Model, "name": cfg.Model,
						"contextWindow": 128000, "maxTokens": 8192,
					}},
				},
			},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "openclaw-ws-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	cmd := exec.CommandContext(ctx, "openclaw", "--profile", profile, "config", "patch", "--replace-path", "models.providers.bench.models", "--file", f.Name())
	cmd.Env = openclawEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}
