package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
)

func healthzServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := New(Deps{Config: config.Default(), Logger: slog.New(slog.DiscardHandler), Version: "test-v1"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestHealthz(t *testing.T) {
	ts := healthzServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" || body["version"] != "test-v1" {
		t.Fatalf("body = %v, want status=ok version=test-v1", body)
	}
}

func TestHealthzRejectsNonGET(t *testing.T) {
	ts := healthzServer(t)

	resp, err := http.Post(ts.URL+"/healthz", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestIdleVolumePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Deps{Config: config.Default(), Store: db})
	for _, tc := range []struct {
		body string
		code int
		want int
	}{
		{`{"value":0}`, http.StatusOK, 0},
		{`{"value":-1}`, http.StatusBadRequest, 0},
		{`{"value":100}`, http.StatusOK, 100},
		{`{"value":101}`, http.StatusBadRequest, 100},
		{`{"value":37}`, http.StatusOK, 37},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playback/volume", strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("volume %s: status=%d body=%s", tc.body, w.Code, w.Body.String())
		}
		if tc.code == http.StatusOK {
			var snap playbackSnapshot
			if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
				t.Fatal(err)
			}
			if snap.Volume != tc.want || snap.State != "idle" {
				t.Fatalf("volume response=%+v", snap)
			}
		}
		var saved playbackSnapshot
		if _, err := db.GetState("playback", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Volume != tc.want || s.currentState().Volume != tc.want {
			t.Fatalf("volume %s: saved=%d current=%d want=%d", tc.body, saved.Volume, s.currentState().Volume, tc.want)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playback/seek", strings.NewReader(`{"value":0}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("idle seek status=%d", w.Code)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s = New(Deps{Config: config.Default(), Store: db})
	defer func() { _ = s.Close() }()
	if got := s.currentState().Volume; got != 37 {
		t.Fatalf("restored volume=%d want=37", got)
	}
}

type volumeHandle struct {
	player.Handle
	err error
}

func (h *volumeHandle) SetVolume(int) error { return h.err }
func (h *volumeHandle) Status() (player.Status, error) {
	return player.Status{Volume: 100}, nil
}

func TestVolumeRejectsBackendFailureAndIgnoresStaleStatus(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := New(Deps{Config: config.Default(), Store: db})
	defer s.cancel()
	h := &volumeHandle{}
	s.pbHandle = h
	for _, tc := range []struct {
		fail bool
		body string
		code int
		want int
	}{
		{false, `{"value":37}`, http.StatusOK, 37},
		{true, `{"value":80}`, http.StatusBadRequest, 37},
	} {
		if tc.fail {
			h.err = errors.New("backend unavailable")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playback/volume", strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatalf("volume status=%d body=%s", w.Code, w.Body.String())
		}
		var saved playbackSnapshot
		if _, err := db.GetState("playback", &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Volume != tc.want || s.currentState().Volume != tc.want {
			t.Fatalf("saved=%d current=%d want=%d", saved.Volume, s.currentState().Volume, tc.want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	h.err = nil
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playback/volume", strings.NewReader(`{"value":80}`)))
	if w.Code != http.StatusInternalServerError || s.currentState().Volume != 37 {
		t.Fatalf("failed persistence: status=%d volume=%d", w.Code, s.currentState().Volume)
	}
}
