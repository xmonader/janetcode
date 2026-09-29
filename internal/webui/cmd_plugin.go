package webui

import (
	"context"

	"github.com/xmonader/janetcode/internal/plugin/plugincmd"
)

// pluginCommand answers /plugin; a fetch runs through runBusy like /mcp's.
func (s *server) pluginCommand(args []string) {
	if s.opts.ConfigDir == "" {
		s.errf("plugins need a config directory, and this session has none")
		return
	}
	dir := s.opts.ConfigDir
	if !plugincmd.Slow(args) {
		s.showResult(plugincmd.Run(s.life, dir, args))
		return
	}
	s.runBusy("fetching the plugin", func(ctx context.Context) {
		s.showResult(plugincmd.Run(ctx, dir, args))
	})
}
