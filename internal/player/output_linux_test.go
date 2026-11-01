//go:build linux

package player

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutputDefaultDoesNotConnect(t *testing.T) {
	t.Setenv("PULSE_SERVER", "unix:/nonexistent/arcana-test")
	p, err := NewBeepWithOutput("")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBeepWithOutput("missing-sink"); err == nil {
		t.Fatal("explicit output silently fell back")
	}
}

func TestOutputRejectsAliasesAndRemoteServers(t *testing.T) {
	for _, device := range []string{"@DEFAULT_SINK@", " sink", "sink\n", "sink\x00"} {
		if _, err := NewBeepWithOutput(device); err == nil {
			t.Fatalf("accepted %q", device)
		}
	}
	t.Setenv("PULSE_SERVER", "tcp:127.0.0.1:4713")
	if _, err := NewBeepWithOutput("sink"); err == nil || !strings.Contains(err.Error(), "local PulseAudio Unix socket") {
		t.Fatalf("remote server: %v", err)
	}
}

func TestOutputCanceledHandshakeReleasesSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pulse.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("PULSE_SERVER", "unix:"+path)
	accepted := make(chan struct{})
	released := make(chan struct{})
	go func() {
		defer close(released)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.Write([]byte{0}); err != nil {
			return
		}
		close(accepted)
		_, _ = io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := openDeviceOutput(ctx, "exact-sink"); result <- err }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("did not connect")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled handshake leaked")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("socket remained open")
	}
}

func TestPulseWireCloseInterruptsBlockedWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	o := &pulseOutput{conn: local, failure: make(chan error, 1)}
	result := make(chan error, 1)
	go func() { _, err := (pulseWire{o}).Write(make([]byte, 1024)); result <- err }()
	_ = local.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked writer leaked")
	}
	select {
	case err := <-o.failure:
		if err == nil {
			t.Fatal("write failure was empty")
		}
	default:
		t.Fatal("write error was lost")
	}
}

func TestOutputTruncatedReplyDoesNotBlockClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pulse.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("PULSE_SERVER", "unix:"+path)
	peerDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer func() { _ = conn.Close() }()
		var header [20]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			peerDone <- err
			return
		}
		if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint32(header[:4]))); err != nil {
			peerDone <- err
			return
		}
		// 回复帧头只有一个字节，协议读取必定以 UnexpectedEOF 结束。
		_, err = conn.Write([]byte{0})
		peerDone <- err
	}()
	result := make(chan error, 1)
	go func() { _, err := openDeviceOutput(context.Background(), "exact-sink"); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("truncated handshake succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("truncated reply blocked resource cleanup")
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
