package netease

import (
	"errors"
	"fmt"
	"testing"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

func TestSourcePreviewMetadata(t *testing.T) {
	for _, trial := range []string{"", `,"freeTrialInfo":null`, `,"freeTrialInfo":{"start":0}`, `,"freeTrialInfo":{}`} {
		for _, invalid := range []string{"", "identity", "url", "quality"} {
			id, sourceURL, quality := 123, "https://example.test/audio", "exhigh"
			switch invalid {
			case "identity":
				id = 456
			case "url":
				sourceURL = ""
			case "quality":
				quality = "standard"
			}
			body := fmt.Sprintf(`{"code":200,"data":[{"id":%d,"url":%q,"level":%q%s}]}`, id, sourceURL, quality, trial)
			source, err := parseSongURLFor([]byte(body), 200, "exhigh", "123")
			if invalid != "" {
				if err == nil || errors.Is(err, platform.ErrTemporary) {
					t.Fatalf("invalid %s with %s: %v", invalid, trial, err)
				}
				continue
			}
			want := trial != "" && trial != `,"freeTrialInfo":null`
			if err != nil || source.Preview != want {
				t.Fatalf("trial %s: source=%+v error=%v", trial, source, err)
			}
		}
	}
}
