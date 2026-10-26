package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/LingLambda/arcana-toc/internal/platform/netease"
	"github.com/LingLambda/arcana-toc/internal/platform/qqmusic"
)

type qqMusicService interface {
	Account(context.Context) (qqmusic.Account, error)
	Refresh(context.Context) error
	QRStart(context.Context, string) (qqmusic.QR, error)
	QRPoll(context.Context, string, string) (int, error)
}

// Music binds the local control API to concrete, live service components.
type Music struct {
	Adapter *netease.Adapter
	QQMusic qqMusicService
}

func controlPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			respond(w, 403, map[string]string{"error": "browser access is not enabled"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bindMusic(mux *http.ServeMux, m Music) {
	register := mux.HandleFunc
	if m.Adapter != nil {
		register("GET /api/netease/account", func(w http.ResponseWriter, r *http.Request) {
			a, err := m.Adapter.Account(r.Context())
			reply(w, a, err)
		})
		register("POST /api/netease/refresh", func(w http.ResponseWriter, r *http.Request) {
			err := m.Adapter.Refresh(r.Context())
			reply(w, map[string]bool{"refreshed": err == nil}, err)
		})
		register("POST /api/netease/login", func(w http.ResponseWriter, r *http.Request) {
			qr, err := m.Adapter.QRStart(r.Context())
			reply(w, qr, err)
		})
		register("GET /api/netease/login", func(w http.ResponseWriter, r *http.Request) {
			code, err := m.Adapter.QRPoll(r.Context(), r.URL.Query().Get("key"))
			reply(w, map[string]int{"code": code}, err)
		})
	}
	if m.QQMusic != nil {
		register("GET /api/qqmusic/account", func(w http.ResponseWriter, r *http.Request) {
			a, err := m.QQMusic.Account(r.Context())
			reply(w, a, err)
		})
		register("POST /api/qqmusic/refresh", func(w http.ResponseWriter, r *http.Request) {
			err := m.QQMusic.Refresh(r.Context())
			reply(w, map[string]bool{"refreshed": err == nil}, err)
		})
		register("POST /api/qqmusic/login", func(w http.ResponseWriter, r *http.Request) {
			channel := r.URL.Query().Get("channel")
			if channel == "" {
				channel = "qq"
			}
			if !validQQChannel(channel) {
				respond(w, http.StatusBadRequest, map[string]string{"error": "invalid QQ Music login channel"})
				return
			}
			qr, err := m.QQMusic.QRStart(r.Context(), channel)
			reply(w, struct {
				Channel    string `json:"channel"`
				Identifier string `json:"identifier"`
				MIME       string `json:"mime"`
				Image      []byte `json:"image"`
			}{qr.Channel, qr.Identifier, qr.MIME, qr.Image}, err)
		})
		register("GET /api/qqmusic/login", func(w http.ResponseWriter, r *http.Request) {
			channel := r.URL.Query().Get("channel")
			identifier := r.URL.Query().Get("identifier")
			if !validQQChannel(channel) || identifier == "" {
				respond(w, http.StatusBadRequest, map[string]string{"error": "QQ Music login channel and identifier are required"})
				return
			}
			code, err := m.QQMusic.QRPoll(r.Context(), channel, identifier)
			reply(w, map[string]int{"code": code}, err)
		})
	}
}

func validQQChannel(channel string) bool {
	return channel == "qq" || channel == "wx" || channel == "mobile"
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		respond(w, 400, map[string]string{"error": "invalid JSON body"})
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		respond(w, 400, map[string]string{"error": "one JSON value required"})
		return false
	}
	return true
}

func reply(w http.ResponseWriter, value any, err error) {
	if err == nil {
		respond(w, http.StatusOK, value)
		return
	}
	status := http.StatusBadGateway
	message := "operation failed"
	switch {
	case errors.Is(err, qqmusic.ErrLoginDeviceLimit):
		status = http.StatusForbidden
		message = "QQ Music login device limit exceeded (20279); remove unused devices in the QQ Music app and retry"
		slog.Warn("QQ Music login rejected", "reason", "login_device_limit", "provider_code", 20279)
	case errors.Is(err, platform.ErrVerificationRequired):
		status = 403
		message = "platform rejected request (-462); verification cause is unknown"
	case errors.Is(err, platform.ErrNotLoggedIn), errors.Is(err, qqmusic.ErrNotLoggedIn), errors.Is(err, qqmusic.ErrCredentialExpired):
		status = 401
		message = "account not logged in"
	case errors.Is(err, platform.ErrPermissionDenied):
		status = 403
		message = "permission denied"
	case errors.Is(err, platform.ErrTrackNotFound):
		status = 404
		message = "track not found"
	case errors.Is(err, platform.ErrUnsupportedCapability):
		status = 422
		message = "unsupported capability"
	}
	respond(w, status, map[string]string{"error": message})
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
