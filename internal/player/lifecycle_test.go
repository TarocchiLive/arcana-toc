package player

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

func TestCloseCancelsPendingSource(t *testing.T) {
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewBeep()
	result := make(chan error, 1)
	go func() {
		_, err := p.Play(ctx, ctx, platform.ResolvedSource{Kind: platform.SourceURL, URL: srv.URL})
		result <- err
	}()
	<-entered
	if _, err := p.Play(ctx, ctx, platform.ResolvedSource{Kind: platform.SourceURL, URL: srv.URL}); err == nil {
		t.Fatal("concurrent source preparation accepted")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("closed playback succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Close left source request running")
	}
}

func TestContextsCancelPendingDownload(t *testing.T) {
	for _, which := range []string{"prepare", "playback"} {
		t.Run(which, func(t *testing.T) {
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			entered := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("partial"))
				w.(http.Flusher).Flush()
				close(entered)
				<-r.Context().Done()
			}))
			defer srv.Close()
			prepareCtx, cancelPrepare := context.WithCancel(context.Background())
			defer cancelPrepare()
			playbackCtx, cancelPlayback := context.WithCancel(context.Background())
			defer cancelPlayback()
			p := NewBeep()
			defer p.Close()
			result := make(chan error, 1)
			go func() {
				_, err := p.Play(prepareCtx, playbackCtx, platform.ResolvedSource{Kind: platform.SourceURL, URL: srv.URL})
				result <- err
			}()
			<-entered
			if which == "prepare" {
				cancelPrepare()
			} else {
				cancelPlayback()
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("download did not stop")
			}
			p.mu.Lock()
			busy := p.preparing != nil || p.active != nil
			p.mu.Unlock()
			if busy {
				t.Fatal("canceled preparation retained player resources")
			}
			assertNoTemporaryAudio(t, temp)
		})
	}
}

func assertNoTemporaryAudio(t *testing.T, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "arcana-toc-audio-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary source leak: %v, %v", files, err)
	}
}
