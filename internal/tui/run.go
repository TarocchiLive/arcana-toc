package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// Run connects to the already-running service without opening its database or lock.
// All requests stop when the TUI exits.
func Run(cfg config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTUIClient(cfg)
	client.ctx = ctx
	healthCtx, healthCancel := context.WithTimeout(ctx, 3*time.Second)
	err := client.health(healthCtx)
	healthCancel()
	if err != nil {
		return fmt.Errorf("cannot start TUI: service at %s is unavailable: %w", cfg.Addr(), err)
	}
	_, err = tea.NewProgram(newTUIModel(client), tea.WithAltScreen()).Run()
	return err
}
