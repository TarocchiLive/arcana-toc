package qqmusic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

func sourceSongFixture(mid string) map[string]any {
	return map[string]any{
		"id": 1, "mid": mid, "name": mid, "type": 0, "singer": []any{},
		"album": map[string]any{}, "mv": map[string]any{}, "file": map[string]any{}, "pay": map[string]any{},
		"interval": 1, "isonly": 0, "language": 0, "genre": 0, "index_cd": 0, "index_album": 0,
		"status": 0, "label": "", "bpm": 0, "ov": 0, "sa": 0, "es": "", "vs": []string{}, "vi": []int{}, "vf": []float64{},
	}
}

func TestResolveSourcePreviewMetadata(t *testing.T) {
	const mid = "songPreview"
	for _, test := range []struct {
		name      string
		paid      bool
		requested string
		resolved  string
		preview   bool
	}{
		{name: "free default", resolved: formatMP3128},
		{name: "paid default", paid: true, resolved: formatTRY, preview: true},
		{name: "explicit preview", requested: formatTRY, resolved: formatTRY, preview: true},
		{name: "paid explicit full", paid: true, requested: formatMP3128, resolved: formatMP3128},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Req struct {
						Module string `json:"module"`
						Param  struct {
							Filename []string `json:"filename"`
						} `json:"param"`
					} `json:"req_0"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var data map[string]any
				switch request.Req.Module {
				case "music.pf_song_detail_svr":
					song := sourceSongFixture(mid)
					if test.paid {
						song["pay"] = map[string]any{"pay_play": 1}
					}
					data = map[string]any{"track_info": song}
				case "music.vkey.GetVkey":
					filename := ExpectedFilename(mid, "", test.resolved)
					if len(request.Req.Param.Filename) != 1 || request.Req.Param.Filename[0] != filename {
						t.Errorf("unexpected filename: %v, want %s", request.Req.Param.Filename, filename)
					}
					data = map[string]any{"midurlinfo": []SongURL{{MID: mid, Filename: filename, PURL: "audio.mp3"}}}
				case "music.audioCdnDispatch.cdnDispatch":
					data = map[string]any{"sip": []string{"https://example.test/"}}
				default:
					t.Errorf("unexpected module %s", request.Req.Module)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "req_0": map[string]any{"code": 0, "data": data}})
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			a := NewForTest(server.Client(), endpoint, Credential{})
			source, err := a.ResolveSource(context.Background(), platform.TrackRef{Platform: platform.QQMusic, TrackID: mid}, test.requested)
			if err != nil || source.Preview != test.preview || source.Quality != test.resolved {
				t.Fatalf("source=%+v error=%v", source, err)
			}
		})
	}
}
