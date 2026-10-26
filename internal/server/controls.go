package server

import (
	"net/http"
	"time"
)

func (s *Server) valueControl(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Value int `json:"value"`
		}
		if !decode(w, r, &body) {
			return
		}
		s.pbMu.Lock()
		defer s.pbMu.Unlock()
		if action == "seek" {
			if s.pbHandle == nil {
				httpError(w, 409, "no active playback")
				return
			}
			if body.Value < 0 {
				httpError(w, 400, "seek must be nonnegative")
				return
			}
			if err := s.pbHandle.Seek(time.Duration(body.Value) * time.Millisecond); err != nil {
				httpError(w, 400, "invalid or failed control")
				return
			}
		} else {
			if body.Value < 0 || body.Value > 100 {
				httpError(w, 400, "volume must be between 0 and 100")
				return
			}
			if s.closed {
				httpError(w, http.StatusServiceUnavailable, "service stopping")
				return
			}
			if s.pbHandle != nil {
				if err := s.pbHandle.SetVolume(body.Value); err != nil {
					httpError(w, 400, "invalid or failed control")
					return
				}
			}
			previous := s.lastState.Volume
			s.lastState.Volume = body.Value
			if err := s.persistLocked(); err != nil {
				s.lastState.Volume = previous
				if s.pbHandle != nil {
					if err := s.pbHandle.SetVolume(previous); err != nil {
						s.log.Error("restore playback volume failed", "err", err)
					}
				}
				httpError(w, 500, "persist playback failed")
				return
			}
			writeJSON(w, 200, s.snapshotLocked())
			return
		}
		if err := s.persistLocked(); err != nil {
			httpError(w, 500, "persist playback failed")
			return
		}
		writeJSON(w, 200, s.snapshotLocked())
	}
}
