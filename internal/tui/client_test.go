package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientReadsCompleteLargePlaylistQueue(t *testing.T) {
	entries := make([]tuiQueueEntry, 6000)
	for i := range entries {
		entries[i] = tuiQueueEntry{ID: int64(i + 1), Platform: "netease",
			TrackID: strconv.Itoa(i + 1), Title: strings.Repeat("long song title ", 12)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": entries})
	}))
	defer server.Close()
	client := &tuiClient{baseURL: server.URL, http: server.Client()}
	got, err := client.queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) || got[5999].TrackID != "6000" {
		t.Fatalf("large playlist queue was truncated: %d entries", len(got))
	}
}

func TestClientLifetimeCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	lifetime, stop := context.WithCancel(context.Background())
	defer stop()
	client := &tuiClient{ctx: lifetime, baseURL: server.URL, http: server.Client()}
	ctx, cancel := client.requestContext()
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.status(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("request did not reach server")
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected lifetime cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request survived client lifetime")
	}
}

func TestClientRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{"", "  ", "null", "{", "{} {}", strings.Repeat(" ", tuiResponseLimit+1)} {
		t.Run("body-length-"+strconv.Itoa(len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := &tuiClient{baseURL: server.URL, http: server.Client()}
			ctx, cancel := client.requestContext()
			defer cancel()
			if _, err := client.status(ctx); err == nil {
				t.Fatal("invalid response reported as successful status")
			}
		})
	}
}

func TestClientPreservesPlatformErrorsAndAccountAuthDistinction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		body := `{"error":"search failed","errors":{"qqmusic":"unavailable","netease":"expired"}}`
		if r.URL.Path == "/api/qqmusic/account" {
			body = `{"error":"account not logged in"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client := &tuiClient{baseURL: server.URL, http: server.Client()}
	ctx, cancel := client.requestContext()
	defer cancel()
	_, err := client.account(ctx, tuiPlatformNetEase)
	var httpErr *tuiHTTPError
	if !errors.As(err, &httpErr) || httpErr.platformErrors["qqmusic"] != "unavailable" || httpErr.platformErrors["netease"] != "expired" {
		t.Fatalf("lost structured platform errors: %v", err)
	}
	if !strings.Contains(err.Error(), "qqmusic") || !strings.Contains(err.Error(), "netease") {
		t.Fatalf("platform failures not visible: %v", err)
	}
	account, err := client.account(ctx, tuiPlatformQQMusic)
	if err != nil || account.LoggedIn {
		t.Fatalf("logged-out account was not distinguished: %+v, %v", account, err)
	}
}

func TestLoginPollRequiresExplicitStatus(t *testing.T) {
	for _, body := range []string{`{}`, `{"code":null}`, `{"code":0}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := &tuiClient{baseURL: server.URL, http: server.Client()}
			ctx, cancel := client.requestContext()
			defer cancel()
			code, err := client.pollLogin(ctx, tuiPlatformQQMusic, "qq", "login-key")
			if body == `{"code":0}` {
				if err != nil || code != 0 {
					t.Fatalf("explicit login success rejected: code=%d err=%v", code, err)
				}
			} else if err == nil {
				t.Fatal("missing login status was treated as successful confirmation")
			}
		})
	}
}
