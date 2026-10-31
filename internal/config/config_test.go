package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPlaybackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name         string
		yaml         string
		allowPreview bool
		skipOnError  bool
		timeout      time.Duration
	}{
		{name: "omitted fields", yaml: "player:\n  backend: beep\n", allowPreview: true, timeout: 15 * time.Second},
		{name: "explicit false", yaml: "player:\n  allow_preview: false\n  skip_on_error: false\n", timeout: 15 * time.Second},
		{name: "explicit true", yaml: "player:\n  allow_preview: true\n  skip_on_error: true\n", allowPreview: true, skipOnError: true, timeout: 15 * time.Second},
		{name: "milliseconds", yaml: "player:\n  source_timeout: 250ms\n", allowPreview: true, timeout: 250 * time.Millisecond},
		{name: "seconds", yaml: "player:\n  source_timeout: 45s\n", allowPreview: true, timeout: 45 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Player.AllowPreview != tc.allowPreview || cfg.Player.SkipOnError != tc.skipOnError || cfg.Player.SourceTimeout != tc.timeout {
				t.Fatalf("Player = %+v, want allow_preview=%v skip_on_error=%v source_timeout=%v", cfg.Player, tc.allowPreview, tc.skipOnError, tc.timeout)
			}
		})
	}
}

func TestLoadRejectsInvalidSourceTimeout(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{value: "0s", want: "player.source_timeout: must be positive"},
		{value: "-1s", want: "player.source_timeout: must be positive"},
		{value: "15", want: "parse config"},
		{value: "0", want: "parse config"},
		{value: "invalid", want: "parse config"},
		{value: "\"15\"", want: "parse config"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("player:\n  source_timeout: "+tc.value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, want error containing %q", err, tc.want)
			}
		})
	}
}
