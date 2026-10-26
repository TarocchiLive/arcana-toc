// Package config loads service configuration from an optional YAML file
// layered over sane defaults. Unknown fields are rejected so that typos in
// config files fail loudly instead of silently falling back.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const defaultConfigYAML = `# Arcana ToC stores runtime state and credentials below data_dir.
server:
  address: 127.0.0.1
  port: 8974
data_dir: ./data
log:
  level: info
  format: text
player:
  backend: beep
`

// Config is the full service configuration.
type Config struct {
	Server  Server `yaml:"server"`
	DataDir string `yaml:"data_dir"`
	Log     Log    `yaml:"log"`
	Player  Player `yaml:"player"`
}

// Server is the control HTTP listener.
type Server struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

// Player selects the in-process audio backend.
type Player struct {
	Backend string `yaml:"backend"`
}

// Log configures the structured logger.
type Log struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // text|json
}

// Addr renders the listen address.
func (c Config) Addr() string { return net.JoinHostPort(c.Server.Address, fmt.Sprint(c.Server.Port)) }

// LogLevel maps the configured level onto slog levels.
func (c Config) LogLevel() slog.Level {
	switch c.Log.Level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Default returns the baseline configuration values.
func Default() Config {
	return Config{
		Server:  Server{Address: "127.0.0.1", Port: 8974},
		DataDir: "./data",
		Log:     Log{Level: "info", Format: "text"},
		Player:  Player{Backend: "beep"},
	}
}

// DefaultConfigPath is the configuration created and used when -config is omitted.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("resolve home directory: empty path")
	}
	return filepath.Join(home, "arcana", "toc", "config.yaml"), nil
}

func ensureDefaultConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create default config directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect default config directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe default config directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict default config directory permissions: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect default config: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("default config must be a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("restrict default config permissions: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("create default config: %w", err)
	}

	_, writeErr := io.WriteString(f, defaultConfigYAML)
	closeErr := errors.Join(f.Chmod(0o600), f.Sync(), f.Close())
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write default config: %w", err)
	}
	return nil
}

// Load reads a YAML configuration, creating the per-user default when path is empty.
func Load(path string) (Config, error) {
	if path == "" {
		var err error
		path, err = DefaultConfigPath()
		if err != nil {
			return Config{}, err
		}
		if err := ensureDefaultConfig(path); err != nil {
			return Config{}, err
		}
	}

	cfg := Default()
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("configuration must contain exactly one YAML document")
	}
	if cfg.DataDir == "" {
		return Config{}, errors.New("data_dir: must not be empty")
	}
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(filepath.Dir(path), cfg.DataDir)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	resolved, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, err
	}
	cfg.DataDir = resolved
	return cfg, nil
}

func (c Config) validate() error {
	if ip := net.ParseIP(c.Server.Address); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("server.address: only literal loopback addresses are supported until external API authentication is configured")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port: %d out of range 1-65535", c.Server.Port)
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir: must not be empty")
	}
	if c.Player.Backend != "beep" {
		return fmt.Errorf("player.backend: %q is unsupported; only beep is available", c.Player.Backend)
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: %q must be debug, info, warn or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format: %q must be text or json", c.Log.Format)
	}
	return nil
}
