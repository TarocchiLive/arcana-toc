package qqmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

var _ platform.PlaylistAdapter = (*Adapter)(nil)

func playlistID(id string) (uint64, error) {
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return 0, errors.New("qqmusic playlist: invalid id")
	}
	value, err := strconv.ParseUint(id, 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("qqmusic playlist: invalid id")
	}
	return value, nil
}

func (a *Adapter) SearchPlaylists(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.Playlist], error) {
	if strings.TrimSpace(keyword) == "" {
		return platform.SearchPage[platform.Playlist]{}, errors.New("qqmusic playlist search: keyword is empty")
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.Playlist]{}, err
	}
	response, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{
		Module: "music.search.SearchCgiService", Method: "DoSearchForQQMusicMobile", Param: Object(
			Field("searchid", randomSearchID()), Field("query", keyword), Field("search_type", 3), Field("num_per_page", options.Limit), Field("page_num", options.Page), Field("highlight", false), Field("grp", true), Field("selectors", Object()), Field("vec_selectors", []any{}),
		),
	})
	if err != nil {
		return platform.SearchPage[platform.Playlist]{}, err
	}
	if response.Code != 0 {
		return platform.SearchPage[platform.Playlist]{}, resultError(response.Code)
	}
	items, err := parsePlaylistSearchResponse(response.Data, options.Limit)
	return platform.SearchPage[platform.Playlist]{Items: items, HasMore: searchHasMore(response.Data, options, len(items))}, err
}

func parsePlaylistSearchResponse(data map[string]any, limit int) ([]platform.Playlist, error) {
	if _, err := validateSearchResponseModel(data); err != nil {
		return nil, errors.New("qqmusic playlist search: invalid response")
	}
	body, _ := data["body"].(map[string]any)
	value, _ := nestedOrModelField(data, body, "item_songlist", "songlist")
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("qqmusic playlist search: missing playlists")
	}
	playlists := make([]platform.Playlist, 0, min(limit, len(items)))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("qqmusic playlist search: malformed playlist")
		}
		var rawID any
		for _, key := range []string{"id", "tid", "dissid"} {
			if value, found := item[key]; found {
				rawID = value
				break
			}
		}
		id, err := playlistID(fmt.Sprint(rawID))
		name := stringValue(item, "title", "dissname", "name")
		if err != nil || name == "" {
			return nil, errors.New("qqmusic playlist search: malformed playlist")
		}
		playlists = append(playlists, platform.Playlist{Platform: platform.QQMusic, ID: strconv.FormatUint(id, 10), Name: name})
		if len(playlists) == limit {
			break
		}
	}
	return playlists, nil
}

// PlaylistSongs uses QQMusicApi's CgiGetDiss offset protocol with orderlist=true.
// Counts must remain consistent across pages; unavailable or changing listings
// are errors rather than a silently shortened replacement queue.
func (a *Adapter) PlaylistSongs(ctx context.Context, id string) ([]platform.SearchHit, error) {
	numericID, err := playlistID(id)
	if err != nil {
		return nil, err
	}
	hits := make([]platform.SearchHit, 0)
	total := -1
	for {
		response, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{
			Module: "music.srfDissInfo.DissInfo", Method: "CgiGetDiss", Param: Object(
				Field("disstid", numericID), Field("dirid", 0), Field("tag", false), Field("song_begin", len(hits)), Field("song_num", 100), Field("userinfo", false), Field("orderlist", true), Field("onlysonglist", false),
			),
		})
		if err != nil {
			return nil, err
		}
		if response.Code != 0 {
			return nil, resultError(response.Code)
		}
		page, count, err := parsePlaylistPage(response.Data, len(hits), total)
		if err != nil {
			return nil, err
		}
		total = count
		hits = append(hits, page...)
		if len(hits) == total {
			return hits, nil
		}
	}
}

func playlistCount(value any) (int, error) {
	// CGI decoding uses json.Number. Marshal also supports integer fixture values
	// without accepting truncation of fractional provider counts.
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, errors.New("qqmusic playlist: invalid count")
	}
	count, err := strconv.Atoi(string(raw))
	if err != nil || count < 0 {
		return 0, errors.New("qqmusic playlist: invalid count")
	}
	return count, nil
}

func parsePlaylistPage(data map[string]any, offset, expectedTotal int) ([]platform.SearchHit, int, error) {
	for _, key := range []string{"code", "subcode"} {
		if value, found := data[key]; found {
			code, err := playlistCount(value)
			if err != nil || code != 0 {
				return nil, 0, fmt.Errorf("qqmusic playlist songs: provider %s %v", key, value)
			}
		}
	}
	total, err := playlistCount(data["total_song_num"])
	if err != nil || (expectedTotal >= 0 && total != expectedTotal) || offset > total {
		return nil, 0, errors.New("qqmusic playlist songs: invalid or changed total")
	}
	items, ok := data["songlist"].([]any)
	if !ok || len(items) > total-offset || (len(items) == 0 && offset < total) {
		return nil, 0, errors.New("qqmusic playlist songs: incomplete song list")
	}
	size, err := playlistCount(data["songlist_size"])
	if err != nil || size != len(items) {
		return nil, 0, errors.New("qqmusic playlist songs: inconsistent page size")
	}
	if value, found := data["hasmore"]; found {
		more, err := playlistCount(value)
		if err != nil || more > 1 || (more == 1) != (offset+len(items) < total) {
			return nil, 0, errors.New("qqmusic playlist songs: inconsistent continuation")
		}
	}
	hits := make([]platform.SearchHit, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, 0, errors.New("qqmusic playlist songs: malformed song")
		}
		hit, err := parseCatalogSong(item, "qqmusic playlist songs")
		if err != nil {
			return nil, 0, err
		}
		hits = append(hits, hit)
	}
	return hits, total, nil
}
