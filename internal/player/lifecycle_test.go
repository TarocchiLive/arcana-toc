package player

import (
	"context"
	"github.com/LingLambda/arcana-toc/internal/platform"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
		_, err := p.Play(ctx, platform.ResolvedSource{Kind: platform.SourceURL, URL: srv.URL})
		result <- err
	}()
	<-entered
	if _, err := p.Play(ctx, platform.ResolvedSource{Kind: platform.SourceURL, URL: srv.URL}); err == nil {
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
