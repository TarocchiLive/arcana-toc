//go:build !linux

package player

import (
	"context"
	"errors"
)

func openDeviceOutput(context.Context, string) (deviceOutput, error) {
	return nil, errors.New("player.output_device is supported only on Linux with PulseAudio/PipeWire; leave it empty on this platform")
}
