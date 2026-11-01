//go:build !linux

package player

import (
	"strings"
	"testing"
)

func TestOutputDeviceRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := NewBeepWithOutput("specific-sink"); err == nil || !strings.Contains(err.Error(), "only on Linux") {
		t.Fatalf("expected platform restriction, got %v", err)
	}
	p, err := NewBeepWithOutput("")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}
