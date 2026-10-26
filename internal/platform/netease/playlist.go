package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

var _ platform.PlaylistAdapter = (*Adapter)(nil)

func playlistID(id string) (string, error) {
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return "", fmt.Errorf("netease playlist: invalid id")
	}
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 {
		return "", fmt.Errorf("netease playlist: invalid id")
	}
	return strconv.FormatUint(n, 10), nil
}

func (a *Adapter) SearchPlaylists(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.Playlist], error) {
	if strings.TrimSpace(keyword) == "" {
		return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: keyword is empty")
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.Playlist]{}, err
	}
	code, body, err := a.callWeapi(ctx, "playlist search", "cloudsearch/pc", map[string]interface{}{
		"s": keyword, "type": "1000", "limit": strconv.Itoa(options.Limit), "offset": strconv.Itoa((options.Page - 1) * options.Limit),
	})
	if err != nil {
		return platform.SearchPage[platform.Playlist]{}, err
	}
	if code != http.StatusOK {
		return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: response code %d", code)
	}
	var response struct {
		Result *struct {
			PlaylistCount *int `json:"playlistCount"`
			Playlists     []struct {
				ID   json.RawMessage `json:"id"`
				Name string          `json:"name"`
			} `json:"playlists"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Result == nil {
		return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: malformed response")
	}
	result := response.Result
	if result.Playlists == nil && result.PlaylistCount == nil {
		return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: missing playlists")
	}
	playlists := make([]platform.Playlist, 0, len(result.Playlists))
	for _, item := range result.Playlists {
		id, err := responseID(item.ID)
		if err != nil {
			return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: malformed id")
		}
		id, err = playlistID(id)
		if err != nil || item.Name == "" {
			return platform.SearchPage[platform.Playlist]{}, fmt.Errorf("netease playlist search: malformed playlist")
		}
		playlists = append(playlists, platform.Playlist{Platform: platform.Netease, ID: id, Name: item.Name})
		if len(playlists) == options.Limit {
			break
		}
	}
	return platform.SearchPage[platform.Playlist]{Items: playlists, HasMore: searchHasMore(body, "playlistCount", options, len(playlists))}, nil
}

// PlaylistSongs reuses the songs returned with playlist metadata, then resolves
// only missing trackIds in serial batches. The ID list remains authoritative:
// partial provider responses must never silently shorten a replacement queue.
func (a *Adapter) PlaylistSongs(ctx context.Context, id string) ([]platform.SearchHit, error) {
	id, err := playlistID(id)
	if err != nil {
		return nil, err
	}
	code, body, err := a.callWeapi(ctx, "playlist songs", "v3/playlist/detail", map[string]interface{}{
		"id": id, "n": "1000", "s": "0",
	})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("netease playlist songs: response code %d", code)
	}
	ids, byID, err := parsePlaylist(body)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, id := range ids {
		if _, found := byID[id]; !found {
			missing = append(missing, id)
			// Reserve the ID so duplicate playlist entries need only one lookup.
			byID[id] = platform.SearchHit{}
		}
	}
	for begin := 0; begin < len(missing); begin += 500 {
		end := min(begin+500, len(missing))
		batch := missing[begin:end]
		entries := make([]map[string]string, len(batch))
		for i, trackID := range batch {
			entries[i] = map[string]string{"id": trackID}
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		code, body, err := a.callWeapi(ctx, "playlist song details", "v3/song/detail", map[string]interface{}{
			"c": string(encoded), "ids": "[" + strings.Join(batch, ",") + "]",
		})
		if err != nil {
			return nil, err
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("netease playlist song details: response code %d", code)
		}
		page, err := parsePlaylistDetails(body, batch)
		if err != nil {
			return nil, err
		}
		for _, hit := range page {
			byID[hit.Ref.TrackID] = hit
		}
	}
	hits := make([]platform.SearchHit, len(ids))
	for i, id := range ids {
		hits[i] = byID[id]
	}
	return hits, nil
}

func parsePlaylist(body []byte) ([]string, map[string]platform.SearchHit, error) {
	var response struct {
		Playlist *struct {
			TrackCount *int `json:"trackCount"`
			TrackIDs   []struct {
				ID json.RawMessage `json:"id"`
			} `json:"trackIds"`
			Tracks []json.RawMessage `json:"tracks"`
		} `json:"playlist"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Playlist == nil {
		return nil, nil, fmt.Errorf("netease playlist songs: malformed playlist")
	}
	p := response.Playlist
	if p.TrackCount == nil || *p.TrackCount < 0 || p.TrackIDs == nil || len(p.TrackIDs) != *p.TrackCount {
		return nil, nil, fmt.Errorf("netease playlist songs: incomplete track ids")
	}
	ids := make([]string, len(p.TrackIDs))
	for i, item := range p.TrackIDs {
		id, err := responseID(item.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("netease playlist songs: malformed track id")
		}
		ids[i], err = playlistID(id)
		if err != nil {
			return nil, nil, fmt.Errorf("netease playlist songs: malformed track id")
		}
	}
	byID := make(map[string]platform.SearchHit, len(ids))
	for _, raw := range p.Tracks {
		hit, err := parseArtistSong(raw)
		if err != nil {
			return nil, nil, err
		}
		byID[hit.Ref.TrackID] = hit
	}
	return ids, byID, nil
}

func parsePlaylistDetails(body []byte, ids []string) ([]platform.SearchHit, error) {
	var response struct {
		Songs []json.RawMessage `json:"songs"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Songs == nil {
		return nil, fmt.Errorf("netease playlist song details: missing songs")
	}
	byID := make(map[string]platform.SearchHit, len(response.Songs))
	for _, raw := range response.Songs {
		hit, err := parseArtistSong(raw)
		if err != nil {
			return nil, err
		}
		byID[hit.Ref.TrackID] = hit
	}
	hits := make([]platform.SearchHit, 0, len(ids))
	for _, id := range ids {
		hit, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("netease playlist song details: missing track %s", id)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}
