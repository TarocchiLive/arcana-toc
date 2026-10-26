package main

import (
	"errors"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/player"
)

func newPlayer(cfg config.Player) (player.Player, error) {
	if cfg.Backend != "beep" {
		return nil, errors.New("unsupported player backend")
	}
	return player.NewBeep(), nil
}
