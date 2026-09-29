package main

import (
	"context"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/voice"
)

// voiceConfig is where /voice sends a recording: the voice block's
// endpoint and key when it names them, else the active provider's when it
// speaks the OpenAI API, else the OpenAI environment variables. An
// Anthropic session has no transcription endpoint of its own.
func voiceConfig(cfg *config.Config, getenv func(string) string) voice.Config {
	var vc voice.Config
	var block config.VoiceConfig
	if cfg != nil && cfg.Voice != nil {
		block = *cfg.Voice
	}
	vc.Model, vc.Recorder = block.Model, block.Recorder
	if cfg != nil {
		if p := cfg.Active(); p.Type == "openai" {
			vc.Endpoint = p.BaseURL
			if keys := p.Keys(); len(keys) > 0 {
				vc.APIKey = keys[0]
			}
		}
	}
	if vc.Endpoint == "" {
		vc.Endpoint = getenv("OPENAI_BASE_URL")
		if vc.Endpoint == "" && getenv("OPENAI_API_KEY") != "" {
			vc.Endpoint = "https://api.openai.com/v1"
		}
		key, _, _ := strings.Cut(getenv("OPENAI_API_KEY"), ",")
		vc.APIKey = key
	}
	if block.Endpoint != "" {
		vc.Endpoint = block.Endpoint
	}
	if block.APIKeyEnv != "" {
		vc.APIKey = getenv(block.APIKeyEnv)
	}
	// The language setting doubles as the transcription hint when it is a
	// language the endpoint can be told by code.
	if cfg != nil && cfg.Language != "" {
		if c := i18n.Resolve(cfg.Language, nil); c != "en" {
			vc.Language = c
		} else if l := strings.ToLower(cfg.Language); len(l) == 2 {
			vc.Language = l
		}
	}
	return vc
}

// transcribeFile transcribes a recording the web page made.
func transcribeFile(cfg *config.Config) func(ctx context.Context, path string) (string, error) {
	return func(ctx context.Context, path string) (string, error) {
		return voice.Transcribe(ctx, voiceConfig(cfg, os.Getenv), path)
	}
}

// voiceSession records until stop and returns the transcript. The config
// is read at each use, so /config voice.model applies to the next one.
func voiceSession(cfg *config.Config) func(ctx context.Context, stop <-chan struct{}) (string, error) {
	return func(ctx context.Context, stop <-chan struct{}) (string, error) {
		return voice.Session(ctx, voiceConfig(cfg, os.Getenv), stop)
	}
}
