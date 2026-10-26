package tui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpaceControlsPlaybackLifecycle(t *testing.T) {
	state := "idle"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/playback/start":
			if state != "idle" {
				t.Errorf("start while %s", state)
				w.WriteHeader(409)
				return
			}
			state = "playing"
		case "/api/playback/pause":
			if state != "playing" {
				t.Errorf("pause while %s", state)
				w.WriteHeader(409)
				return
			}
			state = "paused"
		case "/api/playback/resume":
			if state != "paused" {
				t.Errorf("resume while %s", state)
				w.WriteHeader(409)
				return
			}
			state = "playing"
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	model := newTUIModel(&tuiClient{baseURL: server.URL, http: server.Client()})
	for _, want := range []string{"playing", "paused", "playing"} {
		model = updateModel(model, tuiSnapshotMsg{kind: syncStatus, status: tuiStatus{Playback: tuiPlayback{State: state}}})
		_, cmd := model.handlePlaybackKey(" ")
		if cmd == nil {
			t.Fatal("space produced no action")
		}
		result := cmd().(tuiActionMsg)
		if result.err != nil || state != want {
			t.Fatalf("state=%s want=%s err=%v", state, want, result.err)
		}
	}
}

func TestNextAndStopKeysControlPlayback(t *testing.T) {
	state := "first"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/playback/next":
			state = "second"
		case "/api/playback/stop":
			state = "stopped"
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	m := newTUIModel(&tuiClient{baseURL: srv.URL, http: srv.Client()})
	m = updateModel(m, tuiSnapshotMsg{kind: syncStatus, status: tuiStatus{Playback: tuiPlayback{State: "playing"}}})
	for _, step := range []struct{ key, want string }{{"n", "second"}, {"x", "stopped"}} {
		_, cmd := m.handlePlaybackKey(step.key)
		if cmd == nil {
			t.Fatalf("key %s ignored", step.key)
		}
		result := cmd().(tuiActionMsg)
		if result.err != nil || state != step.want {
			t.Fatalf("key=%s state=%s err=%v", step.key, state, result.err)
		}
	}
}
