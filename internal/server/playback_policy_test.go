package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/player"
	"github.com/LingLambda/arcana-toc/internal/store"
)

type policyCatalog struct {
	*playlistCatalog
	resolve func(context.Context, platform.TrackRef) (platform.ResolvedSource, error)
}

func (a *policyCatalog) ResolveSource(ctx context.Context, ref platform.TrackRef, _ string) (platform.ResolvedSource, error) {
	return a.resolve(ctx, ref)
}

type policyPlayer struct {
	player.Player
	play func(context.Context, context.Context, platform.ResolvedSource) (player.Handle, error)
}

func (p *policyPlayer) Play(prepare, playback context.Context, source platform.ResolvedSource) (player.Handle, error) {
	return p.play(prepare, playback, source)
}
func (*policyPlayer) Close() error { return nil }

func TestPolicyRetrySchedule(t *testing.T) {
	for _, failure := range []error{platform.ErrTemporary, platform.ErrSourceExpired} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var attempts []time.Duration
				a := &policyCatalog{resolve: func(context.Context, platform.TrackRef) (platform.ResolvedSource, error) {
					attempts = append(attempts, time.Since(start))
					return platform.ResolvedSource{}, failure
				}}
				s := &Server{ctx: context.Background(), sourceTimeout: time.Minute}
				h, _, err := s.prepareEntry(context.Background(), a, platform.TrackRef{})
				want := []time.Duration{0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 23 * time.Second}
				if h != nil || !errors.Is(err, errPlaybackRetryExhausted) || time.Since(start) != 30*time.Second || !reflect.DeepEqual(attempts, want) {
					t.Fatalf("handle=%v err=%v elapsed=%s attempts=%v", h, err, time.Since(start), attempts)
				}
			})
		})
	}
}

func TestPolicyAttemptDeadlineAndSuccessfulHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var attempts []time.Duration
		var prepareCtx, playbackCtx context.Context
		a := &policyCatalog{resolve: func(ctx context.Context, ref platform.TrackRef) (platform.ResolvedSource, error) {
			attempts = append(attempts, time.Since(start))
			time.Sleep(500 * time.Millisecond)
			return platform.ResolvedSource{Kind: platform.SourceClient, URI: ref.String()}, nil
		}}
		calls := 0
		wantHandle := &playlistHandle{done: make(chan player.Result, 1)}
		lifetime, stop := context.WithCancel(context.Background())
		defer stop()
		s := &Server{ctx: lifetime, sourceTimeout: time.Second, player: &policyPlayer{play: func(prep, life context.Context, _ platform.ResolvedSource) (player.Handle, error) {
			calls++
			if calls == 1 {
				<-prep.Done()
				if time.Since(start) != time.Second {
					t.Fatalf("resolve and Play did not share timeout: %s", time.Since(start))
				}
				return nil, prep.Err()
			}
			prepareCtx, playbackCtx = prep, life
			return wantHandle, nil
		}}}
		h, _, err := s.prepareEntry(context.Background(), a, platform.TrackRef{Platform: platform.Netease, TrackID: "one"})
		if err != nil || h != wantHandle || calls != 2 || !reflect.DeepEqual(attempts, []time.Duration{0, 2 * time.Second}) {
			t.Fatalf("handle=%v err=%v calls=%d attempts=%v", h, err, calls, attempts)
		}
		if prepareCtx.Err() == nil || playbackCtx.Err() != nil || playbackCtx != lifetime {
			t.Fatal("preparation cleanup canceled playback or failed to release attempt context")
		}
		time.Sleep(time.Minute)
		if playbackCtx.Err() != nil {
			t.Fatal("preparation budget leaked into playback lifetime")
		}
	})
}

func policyAdd(t *testing.T, db *store.Store, ids ...string) []int64 {
	t.Helper()
	var result []int64
	for _, id := range ids {
		key, err := db.AddQueueEntry(store.QueueEntry{Platform: "netease", TrackID: id})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, key)
	}
	return result
}

func policyRequest(s *Server, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func policyAwait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("playback transition did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPolicySkipPrioritiesAcrossEntrypoints(t *testing.T) {
	for _, route := range []string{"start", "next", "playlist", "automatic", "delete"} {
		for _, unavailable := range []bool{false, true} {
			for _, ordinary := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/unavailable=%t/error=%t", route, unavailable, ordinary), func(t *testing.T) {
					s, catalog, db := playlistServer(t)
					s.allowPreview, s.skipUnavailable, s.skipOnError = false, unavailable, ordinary
					catalog.hits = []platform.SearchHit{
						{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "preview"}},
						{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "broken"}},
						{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: "good"}},
					}
					s.adapters[platform.Netease] = &policyCatalog{playlistCatalog: catalog, resolve: func(_ context.Context, ref platform.TrackRef) (platform.ResolvedSource, error) {
						if ref.TrackID == "broken" {
							return platform.ResolvedSource{}, errors.New("bad format")
						}
						return platform.ResolvedSource{Kind: platform.SourceClient, URI: ref.String(), Preview: ref.TrackID == "preview"}, nil
					}}
					var oldID int64
					var oldHandle *playlistHandle
					if route == "next" || route == "automatic" || route == "delete" {
						oldID = policyAdd(t, db, "old")[0]
						if w := policyRequest(s, http.MethodPost, "/api/playback/start"); w.Code != http.StatusAccepted {
							t.Fatal(w.Body.String())
						}
						s.pbMu.Lock()
						oldHandle = s.pbHandle.(*playlistHandle)
						s.pbMu.Unlock()
					}
					if route != "playlist" {
						policyAdd(t, db, "preview", "broken", "good")
					}
					var w *httptest.ResponseRecorder
					switch route {
					case "playlist":
						w = playlistRequest(s)
					case "automatic":
						oldHandle.done <- player.Result{Reason: player.EndEOF}
						policyAwait(t, func() bool { s.pbMu.Lock(); defer s.pbMu.Unlock(); return s.pbHandle != oldHandle })
					case "delete":
						w = policyRequest(s, http.MethodDelete, fmt.Sprintf("/api/queue/%d", oldID))
					default:
						w = policyRequest(s, http.MethodPost, "/api/playback/"+route)
					}
					if w != nil {
						if route == "delete" && w.Code != http.StatusNoContent {
							t.Fatalf("delete=%d: %s", w.Code, w.Body.String())
						}
						if route != "delete" && ((unavailable && ordinary && w.Code != http.StatusAccepted) || (!(unavailable && ordinary) && w.Code < 400)) {
							t.Fatalf("status=%d: %s", w.Code, w.Body.String())
						}
					}
					snap := s.currentState()
					if unavailable && ordinary {
						if snap.Entry == nil || snap.Entry.TrackID != "good" {
							t.Fatalf("state=%+v", snap)
						}
					} else if snap.Entry != nil || snap.State != "idle" {
						t.Fatalf("state=%+v", snap)
					}
					history, err := db.HistoryEntries()
					if err != nil {
						t.Fatal(err)
					}
					seen := map[int64]bool{}
					outcomes := map[string]string{}
					for _, h := range history {
						if seen[h.QueueID] {
							t.Fatalf("duplicate archive: %+v", history)
						}
						seen[h.QueueID] = true
						outcomes[h.TrackID] = h.Outcome
					}
					automatic := route == "automatic" || route == "delete"
					wantPreview := ""
					if unavailable {
						wantPreview = "skipped"
					} else if automatic {
						wantPreview = "failed"
					}
					wantBroken := ""
					if unavailable && (ordinary || automatic) {
						wantBroken = "failed"
					}
					if outcomes["preview"] != wantPreview || outcomes["broken"] != wantBroken || outcomes["good"] != "" {
						t.Fatalf("history=%+v", history)
					}
				})
			}
		}
	}
}

func TestPolicyRuntimeFailureAdvancement(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		result                              player.Result
		skipError, skipUnavailable, advance bool
	}{
		{"ordinary enabled", player.Result{Reason: player.EndError, Err: errors.New("decode")}, true, false, true},
		{"ordinary disabled", player.Result{Reason: player.EndError, Err: errors.New("decode")}, false, true, false},
		{"permission disabled", player.Result{Reason: player.EndError, Err: platform.ErrPermissionDenied}, true, false, false},
		{"permission enabled", player.Result{Reason: player.EndError, Err: platform.ErrPermissionDenied}, false, true, true},
		{"stopped", player.Result{Reason: player.EndStopped}, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, db := playlistServer(t)
			s.skipOnError, s.skipUnavailable = tc.skipError, tc.skipUnavailable
			policyAdd(t, db, "first", "second")
			if w := policyRequest(s, http.MethodPost, "/api/playback/start"); w.Code != http.StatusAccepted {
				t.Fatal(w.Body.String())
			}
			s.pbMu.Lock()
			h := s.pbHandle.(*playlistHandle)
			s.pbMu.Unlock()
			h.done <- tc.result
			policyAwait(t, func() bool { s.pbMu.Lock(); defer s.pbMu.Unlock(); return s.pbHandle != h })
			snap := s.currentState()
			if tc.advance {
				if snap.Entry == nil || snap.Entry.TrackID != "second" {
					t.Fatalf("state=%+v", snap)
				}
			} else if snap.Entry != nil || snap.State != "idle" {
				t.Fatalf("state=%+v", snap)
			}
			history, err := db.HistoryEntries()
			want := "failed"
			if tc.result.Reason == player.EndStopped {
				want = "stopped"
			}
			if err != nil || len(history) != 1 || history[0].TrackID != "first" || history[0].Outcome != want {
				t.Fatalf("history=%+v err=%v", history, err)
			}
		})
	}
}

func TestPolicyBudgetPreservesPendingAndReportsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, catalog, db := playlistServer(t)
		s.skipOnError = true
		catalog.resolveErr = platform.ErrTemporary
		policyAdd(t, db, "one", "two")
		start := time.Now()
		w := policyRequest(s, http.MethodPost, "/api/playback/start")
		if w.Code != http.StatusGatewayTimeout || time.Since(start) != 30*time.Second {
			t.Fatalf("status=%d elapsed=%s body=%s", w.Code, time.Since(start), w.Body.String())
		}
		rows, err := db.QueueEntries()
		if err != nil || len(rows) != 2 || rows[0].Status != "pending" || rows[1].Status != "pending" {
			t.Fatalf("queue=%+v err=%v", rows, err)
		}
		history, err := db.HistoryEntries()
		if err != nil || len(history) != 0 {
			t.Fatalf("history=%+v err=%v", history, err)
		}
		if snap := s.currentState(); snap.State != "idle" || snap.Entry != nil || snap.Position != 0 || snap.Duration != 0 || snap.Error != "playback preparation timed out after 30s" {
			t.Fatalf("state=%+v", snap)
		}
	})
}

func TestPolicyCancellationPreservesQueue(t *testing.T) {
	for _, stage := range []string{"backoff", "download"} {
		for _, closeServer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/close=%t", stage, closeServer), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s, catalog, db := playlistServer(t)
					s.skipOnError = true
					policyAdd(t, db, "one", "two")
					calls := 0
					s.adapters[platform.Netease] = &policyCatalog{playlistCatalog: catalog, resolve: func(context.Context, platform.TrackRef) (platform.ResolvedSource, error) {
						calls++
						if stage == "backoff" {
							return platform.ResolvedSource{}, platform.ErrTemporary
						}
						return platform.ResolvedSource{Kind: platform.SourceClient}, nil
					}}
					if stage == "download" {
						s.player = &policyPlayer{play: func(prep, _ context.Context, _ platform.ResolvedSource) (player.Handle, error) {
							<-prep.Done()
							return nil, prep.Err()
						}}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					closed := make(chan struct{})
					go func() {
						time.Sleep(500 * time.Millisecond)
						if closeServer {
							_ = s.Close()
						} else {
							cancel()
						}
						close(closed)
					}()
					start := time.Now()
					w := httptest.NewRecorder()
					s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/playback/start", nil).WithContext(ctx))
					<-closed
					if time.Since(start) != 500*time.Millisecond || calls != 1 || w.Code < 400 {
						t.Fatalf("elapsed=%s calls=%d status=%d", time.Since(start), calls, w.Code)
					}
					rows, err := db.QueueEntries()
					if err != nil || len(rows) != 2 || rows[0].Status != "pending" || rows[1].Status != "pending" {
						t.Fatalf("queue=%+v err=%v", rows, err)
					}
					history, err := db.HistoryEntries()
					if err != nil || len(history) != 0 {
						t.Fatalf("history=%+v err=%v", history, err)
					}
					time.Sleep(time.Minute)
					if calls != 1 {
						t.Fatalf("continued preparation after cancellation: %d", calls)
					}
				})
			})
		}
	}
}

func TestPolicyCloseDoesNotAdvanceActiveQueue(t *testing.T) {
	s, _, db := playlistServer(t)
	s.skipOnError, s.skipUnavailable = true, true
	policyAdd(t, db, "one", "two")
	if w := policyRequest(s, http.MethodPost, "/api/playback/start"); w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueueEntries()
	if err != nil || len(rows) != 1 || rows[0].TrackID != "two" || rows[0].Status != "pending" {
		t.Fatalf("queue=%+v err=%v", rows, err)
	}
	history, err := db.HistoryEntries()
	if err != nil || len(history) != 1 || history[0].TrackID != "one" || history[0].Outcome != "interrupted" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
