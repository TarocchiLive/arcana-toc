package netease

import (
	"github.com/LingLambda/arcana-toc/internal/platform"
	"testing"
	"time"
)

func TestParseSearch(t *testing.T) {
	body := []byte(`{"result":{"songs":[
		{"id":186016,"name":"晴天","fee":1,"ar":[{"name":"周杰伦"}],"album":{"name":"叶惠美"},"duration":269000},
		{"id":3391116218,"name":"晴天(翻唱)","fee":8,"artists":[{"name":"路人"}],"duration":228000}
	]},"code":200}`)

	hits, err := parseSearch(body)
	if err != nil {
		t.Fatalf("parseSearch: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2", len(hits))
	}
	vip := hits[0]
	if vip.Ref.TrackID != "186016" || !vip.PaidHint || len(vip.Artists) != 1 || vip.Artists[0] != "周杰伦" {
		t.Fatalf("vip hit = %+v", vip)
	}
	if vip.Duration != 269*time.Second {
		t.Fatalf("vip duration = %v", vip.Duration)
	}
	free := hits[1]
	if free.PaidHint || free.Ref.TrackID != "3391116218" || free.Artists[0] != "路人" {
		t.Fatalf("free hit = %+v", free)
	}
}

func TestParseSongURLFree(t *testing.T) {
	body := []byte(`{"code":200,"data":[{"id":186016,"code":200,"url":"http://m801.music.126.net/x/a.mp3","level":"exhigh","br":128000,"fee":8,"expi":1200}]}`)
	src, err := parseSongURLFor(body, 200, "standard", "186016")
	if err != nil {
		t.Fatalf("parseSongURLFor: %v", err)
	}
	if src.Kind != platform.SourceURL || src.URL != "http://m801.music.126.net/x/a.mp3" {
		t.Fatalf("source = %+v", src)
	}
	if src.Quality != "exhigh" {
		t.Fatalf("Quality = %q, want response level to win", src.Quality)
	}
	if src.ExpiresAt.IsZero() || time.Until(src.ExpiresAt) > 1200*time.Second {
		t.Fatalf("ExpiresAt = %v, want nominal ~20min advisory", src.ExpiresAt)
	}
}
