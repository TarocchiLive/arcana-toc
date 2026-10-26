package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "server:\n  port: 9999\nlog:\n  level: debug\n  format: json\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if cfg.Addr() != "127.0.0.1:9999" {
		t.Fatalf("Addr() = %q, want 127.0.0.1:9999", cfg.Addr())
	}
	if cfg.LogLevel() != slog.LevelDebug {
		t.Fatalf("LogLevel() = %v, want debug", cfg.LogLevel())
	}
}
