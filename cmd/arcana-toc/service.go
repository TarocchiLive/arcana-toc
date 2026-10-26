package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/platform/netease"
	"github.com/LingLambda/arcana-toc/internal/platform/qqmusic"
	"github.com/LingLambda/arcana-toc/internal/server"
	"github.com/LingLambda/arcana-toc/internal/store"
	"github.com/LingLambda/arcana-toc/internal/version"
)

func runService(cfg config.Config, importCookies string) error {
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	logger.Info("starting arcana-toc", "version", version.String(), "data_dir", cfg.DataDir)

	lock, err := ownDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Warn("storage close incomplete", "err", err)
		}
	}()
	logger.Info("storage ready", "path", cfg.DataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	adapter, err := netease.New(filepath.Join(cfg.DataDir, "credentials", "netease.json"))
	if err != nil {
		return err
	}
	if importCookies != "" {
		if err := adapter.ImportCookies(importCookies); err != nil {
			return err
		}
		logger.Info("netease session imported")
		return nil
	}
	qq, err := qqmusic.New(filepath.Join(cfg.DataDir, "credentials", "qqmusic.json"))
	if err != nil {
		return err
	}
	defer func() {
		if err := qq.Close(); err != nil {
			logger.Error("QQ Music shutdown failed")
		}
	}()
	backend, err := newPlayer(cfg.Player)
	if err != nil {
		return err
	}
	logger.Info("player configured", "backend", cfg.Player.Backend)
	srv := server.New(server.Deps{Config: cfg, Logger: logger, Version: version.String(), Store: st, Adapters: map[platform.Platform]platform.Adapter{platform.Netease: adapter, platform.QQMusic: qq}, Player: backend, Music: server.Music{Adapter: adapter, QQMusic: qq}})
	defer func() {
		if err := srv.Close(); err != nil {
			logger.Error("playback shutdown failed")
		}
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Warn("http shutdown incomplete", "err", err)
	}
	if err := <-serveErr; err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("bye")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel()}
	if cfg.Log.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
