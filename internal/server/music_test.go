package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/config"
)

func TestMusicAllowsLocalRequestsAndRejectsBrowserOrigin(t *testing.T) {
	s := New(Deps{Config: config.Default(), Logger: slog.New(slog.DiscardHandler), Version: "test"})
	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{"local", "", http.StatusOK},
		{"cross-origin", "https://untrusted.example", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q want=no-store", got)
			}
		})
	}
}
