//go:build linux

package player

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep/speaker"
	"github.com/jfreymuth/pulse/proto"
)

const pulseTimeout = 2 * time.Second

// pulseOutput 仅负责一个不可移动的立体声流，不修改服务端设备或默认路由。
type pulseOutput struct {
	conn      net.Conn
	client    proto.Client
	stream    proto.CreatePlaybackStreamReply
	requests  chan uint32
	failure   chan error
	pumpDone  chan struct{}
	closeOnce sync.Once
	startOnce sync.Once
}

// pulseWire 将底层错误显式反馈；协议库的 Send 不返回 socket 写错误。
type pulseWire struct{ output *pulseOutput }

func (w pulseWire) Read(p []byte) (int, error) {
	n, err := w.output.conn.Read(p)
	if err != nil {
		w.output.fail(fmt.Errorf("PulseAudio connection lost: %w", err))
		return n, err
	}
	return n, nil
}

func (w pulseWire) Write(p []byte) (int, error) {
	_ = w.output.conn.SetWriteDeadline(time.Now().Add(pulseTimeout))
	n, err := w.output.conn.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.output.fail(fmt.Errorf("PulseAudio write failed: %w", err))
	}
	return n, err
}

func openDeviceOutput(ctx context.Context, device string) (deviceOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, pulseTimeout)
	defer cancel()
	if strings.TrimSpace(device) != device || device == "" || strings.ContainsAny(device, "\x00\r\n") || strings.HasPrefix(device, "@") {
		return nil, errors.New("player.output_device must be an exact PulseAudio sink name, not an alias")
	}
	address := os.Getenv("PULSE_SERVER")
	if address == "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
		}
		address = filepath.Join(runtimeDir, "pulse/native")
	}
	address = strings.TrimPrefix(address, "unix:")
	if !filepath.IsAbs(address) || strings.ContainsAny(address, "\x00\r\n") {
		return nil, errors.New("explicit output requires a local PulseAudio Unix socket (PULSE_SERVER=unix:/path)")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
	if err != nil {
		return nil, fmt.Errorf("open output %q: %w", device, err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	o := &pulseOutput{conn: conn, requests: make(chan uint32, 32), failure: make(chan error, 1), pumpDone: make(chan struct{})}
	o.client.SetTimeout(pulseTimeout)
	o.client.Callback = func(event interface{}) {
		switch event := event.(type) {
		case *proto.Request:
			select {
			case o.requests <- event.Length:
			default:
				o.fail(errors.New("PulseAudio request queue overflow"))
			}
		case *proto.PlaybackStreamKilled:
			o.fail(errors.New("configured PulseAudio sink disappeared or killed playback"))
		case *proto.PlaybackStreamMoved:
			o.fail(errors.New("PulseAudio attempted to move the explicitly bound stream"))
		case *proto.PlaybackStreamSuspended:
			if event.Suspended {
				o.fail(errors.New("configured PulseAudio sink suspended playback"))
			}
		}
	}
	o.client.Open(pulseWire{o})
	fail := func(err error) (deviceOutput, error) {
		o.close()
		return nil, fmt.Errorf("open output %q: %w", device, err)
	}
	cookiePath := os.Getenv("PULSE_COOKIE")
	if cookiePath == "" {
		cookiePath = filepath.Join(os.Getenv("HOME"), ".config/pulse/cookie")
	}
	cookie, err := os.ReadFile(cookiePath)
	if os.IsNotExist(err) {
		cookie = make([]byte, 256)
	} else if err != nil {
		return fail(err)
	}
	if len(cookie) != 256 {
		return fail(errors.New("invalid PulseAudio cookie length"))
	}
	var auth proto.AuthReply
	if err := o.client.Request(&proto.Auth{Version: o.client.Version(), Cookie: cookie}, &auth); err != nil {
		return fail(err)
	}
	o.client.SetVersion(auth.Version)
	if o.client.Version() < 15 {
		return fail(errors.New("PulseAudio protocol version must be at least 15"))
	}
	var named proto.SetClientNameReply
	if err := o.client.Request(&proto.SetClientName{Props: proto.PropList{"application.name": proto.PropListString("arcana-toc")}}, &named); err != nil {
		return fail(err)
	}
	var sink proto.GetSinkInfoReply
	if err := o.client.Request(&proto.GetSinkInfo{SinkIndex: proto.Undefined, SinkName: device}, &sink); err != nil {
		return fail(err)
	}
	if sink.SinkName != device {
		return fail(errors.New("PulseAudio resolved a different sink"))
	}
	request := explicitPulseStream(sink.SinkIndex)
	if err := o.client.Request(&request, &o.stream); err != nil {
		return fail(err)
	}
	if o.stream.SinkIndex != sink.SinkIndex || o.stream.SinkName != device || o.stream.SinkSuspended {
		return fail(errors.New("PulseAudio did not bind the requested available sink"))
	}
	if o.stream.SampleSpec != request.SampleSpec {
		return fail(errors.New("PulseAudio changed the PCM format"))
	}
	return o, nil
}

func explicitPulseStream(index uint32) proto.CreatePlaybackStream {
	return proto.CreatePlaybackStream{
		SampleSpec:      proto.SampleSpec{Format: proto.FormatFloat32LE, Channels: 2, Rate: uint32(beepOutputRate)},
		ChannelMap:      proto.ChannelMap{proto.ChannelFrontLeft, proto.ChannelFrontRight},
		SinkIndex:       index,
		BufferMaxLength: 35280, BufferTargetLength: 17640, BufferPrebufferLength: 0, BufferMinimumRequest: 3528,
		Corked: true, NoMove: true, FailOnSuspend: true,
		ChannelVolumes: proto.ChannelVolumes{0x10000, 0x10000}, VolumeSet: true,
		Properties: proto.PropList{"media.name": proto.PropListString("arcana-toc music")},
	}
}

func (o *pulseOutput) fail(err error) {
	select {
	case o.failure <- err:
	default:
	}
	_ = o.conn.Close()
}

func (o *pulseOutput) close() {
	o.closeOnce.Do(func() {
		// proto 只在完整 EOF 时通知 ConnectionClosed；半帧断开也必须及时释放。
		// 关闭 socket 中断协议库读写，自有 PCM 泵由 wait/outputWG 等待退出。
		_ = o.conn.Close()
	})
}

func (o *pulseOutput) wait() { <-o.pumpDone }

func (o *pulseOutput) start(h *beepHandle) {
	o.startOnce.Do(func() { go o.run(h) })
}

func (o *pulseOutput) run(h *beepHandle) {
	defer h.player.outputWG.Done()
	defer close(o.pumpDone)
	finish := func(err error) {
		speaker.Lock()
		defer speaker.Unlock()
		if err != nil {
			h.finish(Result{Reason: EndError, Err: err})
		} else {
			h.finish(Result{Reason: EndEOF})
		}
	}
	if err := o.client.Request(&proto.CorkPlaybackStream{StreamIndex: o.stream.StreamIndex, Corked: false}, nil); err != nil {
		finish(err)
		return
	}
	remaining := o.stream.Missing
	var samples [441][2]float64
	var pcm [441 * 8]byte
	for {
		select {
		case err := <-o.failure:
			finish(err)
			return
		default:
		}
		if remaining == 0 {
			select {
			case err := <-o.failure:
				finish(err)
				return
			case remaining = <-o.requests:
			}
		}
		if remaining%8 != 0 {
			finish(errors.New("invalid PulseAudio PCM request"))
			return
		}
		count := int(remaining / 8)
		if count > len(samples) {
			count = len(samples)
		}
		speaker.Lock()
		h.mu.Lock()
		finished := h.finished
		seekVersion := h.seekVersion
		h.mu.Unlock()
		if finished {
			speaker.Unlock()
			return
		}
		n, ok := h.volume.Stream(samples[:count])
		decodeErr := h.raw.Err()
		speaker.Unlock()
		for i := range n {
			for channel := range 2 {
				value := math.Max(-1, math.Min(1, samples[i][channel]))
				binary.LittleEndian.PutUint32(pcm[i*8+channel*4:], math.Float32bits(float32(value)))
			}
		}
		if n > 0 {
			if err := o.client.Send(o.stream.StreamIndex, pcm[:n*8]); err != nil {
				finish(err)
				return
			}
			remaining -= uint32(n * 8)
		}
		if !ok {
			if decodeErr == nil {
				decodeErr = o.client.Request(&proto.DrainPlaybackStream{StreamIndex: o.stream.StreamIndex}, nil)
			}
			if decodeErr != nil {
				finish(decodeErr)
				return
			}
			// Drain 等待时允许定位或暂停；已确认的控制操作不能被旧 EOF 覆盖。
			speaker.Lock()
			h.mu.Lock()
			resumed := !h.finished && (h.seekVersion != seekVersion || h.paused)
			h.mu.Unlock()
			if resumed {
				speaker.Unlock()
				continue
			}
			h.finish(Result{Reason: EndEOF})
			speaker.Unlock()
			return
		}
	}
}
