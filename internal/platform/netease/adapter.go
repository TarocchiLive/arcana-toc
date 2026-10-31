// Package netease adapts NetEase Cloud Music to the platform contract.
//
// Requests use an adapter-owned HTTP client and cookie jar. The upstream
// library is used only for NetEase's encrypted weapi form encoding and QR
// device helpers; its process-global request path is intentionally not used.
package netease

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/go-musicfox/netease-music/util"
)

// requestCookieJar changes the cookies visible to one request while delegating
// response cookies to the adapter's persistent private jar.
type requestCookieJar struct {
	base    http.CookieJar
	host    string
	omit    map[string]struct{}
	include []*http.Cookie
}

func (j *requestCookieJar) Cookies(target *url.URL) []*http.Cookie {
	cookies := j.base.Cookies(target)
	if target == nil || !strings.EqualFold(target.Hostname(), j.host) {
		return cookies
	}
	filtered := make([]*http.Cookie, 0, len(cookies)+len(j.include))
	seen := make(map[string]struct{}, len(cookies)+len(j.include))
	for _, cookie := range cookies {
		if _, omitted := j.omit[cookie.Name]; omitted {
			continue
		}
		filtered = append(filtered, cookie)
		seen[cookie.Name] = struct{}{}
	}
	for _, cookie := range j.include {
		if _, exists := seen[cookie.Name]; !exists {
			filtered = append(filtered, cookie)
		}
	}
	return filtered
}

func (j *requestCookieJar) SetCookies(target *url.URL, cookies []*http.Cookie) {
	j.base.SetCookies(target, cookies)
}

const (
	apiBase          = "https://music.163.com/weapi/"
	requestTimeout   = 15 * time.Second
	maximumBodyBytes = 1 << 20
	pcUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0"
)

// Qualities ordered cheapest-first; values map 1:1 onto SongQualityLevel.
var Qualities = []string{"standard", "higher", "exhigh", "lossless", "hires"}

// Adapter implements platform.Adapter for NetEase Cloud Music.
type Adapter struct {
	cookiePath string
	client     *http.Client
	jar        *sessionJar
}

var _ platform.ArtistAdapter = (*Adapter)(nil)

// New opens the adapter's private session store. It restores only sessions
// previously written by this adapter; import Netscape-format cookies explicitly
// with ImportCookies.
func New(cookiePath string) (*Adapter, error) {
	jar, err := loadSessionJar(cookiePath)
	if err != nil {
		return nil, fmt.Errorf("open netease credentials: %w", err)
	}
	deviceURL, err := url.Parse(apiBase)
	if err != nil {
		return nil, fmt.Errorf("initialize netease device identity: %w", err)
	}
	if jar.cookieValue(apiBase, "sDeviceId") == "" {
		jar.SetCookies(deviceURL, []*http.Cookie{{
			Name:   "sDeviceId",
			Value:  util.GenerateSDeviceId(),
			Path:   "/",
			Secure: true,
		}})
		if err := jar.takeWriteError(); err != nil {
			return nil, fmt.Errorf("initialize netease device identity: %w", err)
		}
	}
	return &Adapter{
		cookiePath: cookiePath,
		jar:        jar,
		client: &http.Client{
			Jar:     jar,
			Timeout: requestTimeout,
		},
	}, nil
}

// CookiePath is where the adapter persists its private session jar.
func (a *Adapter) CookiePath() string { return a.cookiePath }

// Platform identifies this adapter.
func (a *Adapter) Platform() platform.Platform { return platform.Netease }

// Capabilities reports honestly supported qualities and controls.
func (a *Adapter) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		DirectURL: true,
		Seek:      true,
		Volume:    true,
		Qualities: append([]string(nil), Qualities...),
	}
}

// Account is the authenticated NetEase identity.
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LoggedIn bool   `json:"logged_in"`
}

// Account obtains the identity for the current session. An anonymous or
// expired session is represented without surfacing the provider response.
func (a *Adapter) Account(ctx context.Context) (Account, error) {
	code, body, err := a.callWeapi(ctx, "account", "nuser/account/get", map[string]interface{}{})
	if err != nil {
		return Account{}, err
	}
	if code != http.StatusOK {
		if code == 301 || code == 302 || code == http.StatusUnauthorized {
			return Account{LoggedIn: false}, nil
		}
		return Account{}, accountCodeError("account", code)
	}

	var response struct {
		Account *struct {
			ID       json.RawMessage `json:"id"`
			UserName string          `json:"userName"`
		} `json:"account"`
		Profile *struct {
			UserID   json.RawMessage `json:"userId"`
			Nickname string          `json:"nickname"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Account{}, fmt.Errorf("netease account: malformed response")
	}
	if response.Account == nil && response.Profile == nil {
		return Account{LoggedIn: false}, nil
	}

	var id string
	if response.Account != nil {
		id, err = responseID(response.Account.ID)
	}
	if id == "" && response.Profile != nil {
		id, err = responseID(response.Profile.UserID)
	}
	if err != nil || id == "" {
		return Account{}, fmt.Errorf("netease account: malformed response")
	}

	var name string
	if response.Profile != nil {
		name = response.Profile.Nickname
	}
	if name == "" && response.Account != nil {
		name = response.Account.UserName
	}
	if name == "" {
		return Account{}, fmt.Errorf("netease account: malformed response")
	}
	return Account{ID: id, Name: name, LoggedIn: true}, nil
}

// Refresh renews the current authenticated session.
func (a *Adapter) Refresh(ctx context.Context) error {
	if a.jar == nil || a.client == nil {
		return fmt.Errorf("netease refresh session: adapter is not initialized")
	}
	if a.jar.cookieValue(apiBase+"login/token/refresh", "MUSIC_U") == "" {
		return accountCodeError("refresh session", http.StatusUnauthorized)
	}
	code, _, err := a.callWeapi(ctx, "refresh session", "login/token/refresh", map[string]interface{}{})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return accountCodeError("refresh session", code)
	}
	return nil
}

// Cloud search exposes per-kind totals and sometimes hasMore. A short page is
// not an end marker: upstream may return fewer items than the requested limit.
func searchHasMore(body []byte, countField string, options platform.SearchOptions, count int) bool {
	if count == 0 {
		return false
	}
	var response struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	var more bool
	if raw, ok := response.Result["hasMore"]; ok && json.Unmarshal(raw, &more) == nil {
		return more
	}
	var total int
	if raw, ok := response.Result[countField]; ok && json.Unmarshal(raw, &total) == nil && total >= 0 {
		return (options.Page-1)*options.Limit+count < total
	}
	// Older responses omit totals; continue until an empty page.
	return true
}

// Search queries the cloud-search song endpoint.
func (a *Adapter) Search(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.SearchHit], error) {
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.SearchHit]{}, err
	}
	code, body, err := a.callWeapi(ctx, "search", "cloudsearch/pc", map[string]interface{}{
		"s":      keyword,
		"type":   "1",
		"limit":  strconv.Itoa(options.Limit),
		"offset": strconv.Itoa((options.Page - 1) * options.Limit),
	})
	if err != nil {
		return platform.SearchPage[platform.SearchHit]{}, err
	}
	if code != http.StatusOK {
		return platform.SearchPage[platform.SearchHit]{}, fmt.Errorf("netease search: response code %d", code)
	}
	items, err := parseSearch(body)
	return platform.SearchPage[platform.SearchHit]{Items: items, HasMore: searchHasMore(body, "songCount", options, len(items))}, err
}

// SearchArtists searches the NetEase artist catalog without changing the
// generic song-search contract exposed by Adapter.Search.
func (a *Adapter) SearchArtists(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.Artist], error) {
	if strings.TrimSpace(keyword) == "" {
		return platform.SearchPage[platform.Artist]{}, fmt.Errorf("netease artist search: keyword is empty")
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.Artist]{}, err
	}
	code, body, err := a.callWeapi(ctx, "artist search", "cloudsearch/pc", map[string]interface{}{
		"s":      keyword,
		"type":   "100",
		"limit":  strconv.Itoa(options.Limit),
		"offset": strconv.Itoa((options.Page - 1) * options.Limit),
	})
	if err != nil {
		return platform.SearchPage[platform.Artist]{}, err
	}
	if code != http.StatusOK {
		return platform.SearchPage[platform.Artist]{}, fmt.Errorf("netease artist search: response code %d", code)
	}
	items, err := parseArtists(body)
	return platform.SearchPage[platform.Artist]{Items: items, HasMore: searchHasMore(body, "artistCount", options, len(items))}, err
}

// ArtistSongs returns the catalog songs for one NetEase artist. The endpoint
// is the same upstream API used by the pinned netease-music client.
func (a *Adapter) ArtistSongs(ctx context.Context, id string) ([]platform.SearchHit, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("netease artist songs: artist id is empty")
	}
	code, body, err := a.callWeapi(ctx, "artist songs", "v1/artist/songs", map[string]interface{}{
		"id":            id,
		"limit":         "100",
		"offset":        "0",
		"order":         "hot",
		"work_type":     "1",
		"private_cloud": "true",
	})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("netease artist songs: response code %d", code)
	}
	return parseArtistSongs(body)
}

// ResolveSource prepares a near-playback URL for ref at the requested quality.
// Permission failures surface as platform.ErrPermissionDenied; sources are never
// substituted. Preview metadata lets callers enforce their playback policy.
func (a *Adapter) ResolveSource(ctx context.Context, ref platform.TrackRef, quality string) (platform.ResolvedSource, error) {
	if ref.Platform != platform.Netease {
		return platform.ResolvedSource{}, fmt.Errorf("netease adapter: %w for %s", platform.ErrUnsupportedCapability, ref)
	}
	if ref.TrackID == "" {
		return platform.ResolvedSource{}, fmt.Errorf("netease song url: %w", platform.ErrTrackNotFound)
	}
	if quality == "" {
		quality = "standard"
	}
	if !supportedQuality(quality) {
		return platform.ResolvedSource{}, &platform.QualityError{Track: ref, Quality: quality}
	}

	code, body, err := a.callWeapi(ctx, "song url", "song/enhance/player/url/v1", map[string]interface{}{
		"ids":        "[" + ref.TrackID + "]",
		"level":      quality,
		"encodeType": "flac",
	})
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	return parseSongURLFor(body, float64(code), quality, ref.TrackID)
}

func supportedQuality(quality string) bool {
	for _, supported := range Qualities {
		if quality == supported {
			return true
		}
	}
	return false
}

func qualityRank(quality string) int {
	for i, supported := range Qualities {
		if quality == supported {
			return i
		}
	}
	return -1
}

// callWeapi makes one context-aware encrypted weapi request. It deliberately
// keeps request bodies, cookies, and response bodies out of errors and logs.
func (a *Adapter) callWeapi(ctx context.Context, operation, endpoint string, payload map[string]interface{}) (int, []byte, error) {
	return a.callWeapiWithJar(ctx, operation, endpoint, payload, a.jar)
}

func (a *Adapter) callWeapiWithJar(ctx context.Context, operation, endpoint string, payload map[string]interface{}, jar http.CookieJar) (int, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if a.client == nil || a.jar == nil {
		return 0, nil, fmt.Errorf("netease %s: adapter is not initialized", operation)
	}

	// Authenticated weapi endpoints require the CSRF cookie in the encrypted
	// payload as well as in the request's Cookie header.
	target, err := url.Parse(apiBase + endpoint)
	if err != nil {
		return 0, nil, fmt.Errorf("netease %s: build request", operation)
	}
	for _, cookie := range jar.Cookies(target) {
		if cookie.Name == "__csrf" && cookie.Value != "" {
			payload["csrf_token"] = cookie.Value
			break
		}
	}
	form, err := util.ApiParamsEncode(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("netease %s: encrypt request", operation)
	}

	values := make(url.Values, len(form))
	for key, value := range form {
		values.Set(key, value)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return 0, nil, fmt.Errorf("netease %s: build request", operation)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", pcUserAgent)
	req.Header.Set("Referer", "https://music.163.com")

	client := &http.Client{
		Transport:     a.client.Transport,
		CheckRedirect: a.client.CheckRedirect,
		Jar:           jar,
		Timeout:       a.client.Timeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("netease %s: request failed: %w", operation, platform.ErrTemporary)
	}
	defer func() { _ = resp.Body.Close() }()
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}

	bodyLimit := maximumBodyBytes
	// Full playlist metadata and batched song details legitimately exceed the
	// small account/search response limit. Keep their larger bound explicit.
	if endpoint == "v3/playlist/detail" || endpoint == "v3/song/detail" {
		bodyLimit = 16 << 20
	}
	body, err := readResponseBody(resp.Body, bodyLimit)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, fmt.Errorf("netease %s: %w", operation, err)
	}
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	if err := a.jar.takeWriteError(); err != nil {
		return 0, nil, fmt.Errorf("netease %s: failed to persist session", operation)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if platform.IsTemporaryHTTPStatus(resp.StatusCode) {
			return 0, nil, fmt.Errorf("netease %s: server returned HTTP %d: %w", operation, resp.StatusCode, platform.ErrTemporary)
		}
		return 0, nil, fmt.Errorf("netease %s: server returned HTTP %d", operation, resp.StatusCode)
	}

	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, nil, fmt.Errorf("netease %s: malformed response", operation)
	}
	if response.Code == -462 {
		slog.WarnContext(ctx, "netease request rejected", "operation", operation, "http_status", resp.StatusCode, "platform_code", response.Code)
		return response.Code, nil, platform.ErrVerificationRequired
	}
	return response.Code, body, nil
}

func readResponseBody(body io.Reader, limit int) ([]byte, error) {
	limited := io.LimitReader(body, int64(limit)+1)
	response, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", platform.ErrTemporary)
	}
	if len(response) > limit {
		return nil, fmt.Errorf("response too large")
	}
	return response, nil
}

func accountCodeError(operation string, code int) error {
	switch code {
	case 301, 302, http.StatusUnauthorized:
		return fmt.Errorf("netease %s: %w", operation, platform.ErrNotLoggedIn)
	default:
		return fmt.Errorf("netease %s: response code %d", operation, code)
	}
}

func responseID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	switch id := value.(type) {
	case string:
		if id == "" {
			return "", fmt.Errorf("empty id")
		}
		return id, nil
	case json.Number:
		if _, err := strconv.ParseUint(id.String(), 10, 64); err != nil {
			return "", err
		}
		return id.String(), nil
	default:
		return "", fmt.Errorf("invalid id")
	}
}

func parseSongURLFor(body []byte, code float64, requested, trackID string) (platform.ResolvedSource, error) {
	var parsed struct {
		Code int `json:"code"`
		Data []struct {
			ID            json.RawMessage `json:"id"`
			Code          int             `json:"code"`
			URL           string          `json:"url"`
			Level         string          `json:"level"`
			Expi          int             `json:"expi"`
			FreeTrialInfo json.RawMessage `json:"freeTrialInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return platform.ResolvedSource{}, fmt.Errorf("netease song url payload: malformed response")
	}
	if len(parsed.Data) != 1 {
		if code == http.StatusNotFound || parsed.Code == http.StatusNotFound {
			return platform.ResolvedSource{}, fmt.Errorf("netease song url: %w", platform.ErrPermissionDenied)
		}
		return platform.ResolvedSource{}, fmt.Errorf("netease song url: malformed response")
	}
	d := parsed.Data[0]
	responseTrackID, err := responseID(d.ID)
	if err != nil || responseTrackID != trackID {
		return platform.ResolvedSource{}, fmt.Errorf("netease song url: response track mismatch")
	}
	if d.URL == "" {
		switch {
		case d.Code == 301 || parsed.Code == 301 || int(code) == 301:
			return platform.ResolvedSource{}, fmt.Errorf("netease song url: %w", platform.ErrNotLoggedIn)
		case d.Code == 403 || d.Code == 404 || d.Code == -110 || parsed.Code == 403 || parsed.Code == 404 || int(code) == 403 || int(code) == 404:
			return platform.ResolvedSource{}, fmt.Errorf("netease song url: %w", platform.ErrPermissionDenied)
		default:
			return platform.ResolvedSource{}, fmt.Errorf("netease song url: no playable source")
		}
	}

	quality := d.Level
	if quality == "" {
		quality = requested
	}
	if !supportedQuality(quality) {
		return platform.ResolvedSource{}, fmt.Errorf("netease song url: unsupported source quality %q", quality)
	}
	if requested != "" && qualityRank(quality) < qualityRank(requested) {
		return platform.ResolvedSource{}, fmt.Errorf("netease song url: %w", platform.ErrPermissionDenied)
	}

	var expires time.Time
	if d.Expi > 0 {
		expires = time.Now().UTC().Add(time.Duration(d.Expi) * time.Second)
	}
	return platform.ResolvedSource{
		Kind:      platform.SourceURL,
		URL:       d.URL,
		Quality:   quality,
		Preview:   trialResponse(d.FreeTrialInfo),
		ExpiresAt: expires,
	}, nil
}

func trialResponse(raw json.RawMessage) bool {
	return len(raw) != 0 && string(bytes.TrimSpace(raw)) != "null"
}

// parseSearch decodes the cloud-search payload, accepting both the `ar`
// (cloud search) and `artists` (classic) artist shapes.
func parseSearch(body []byte) ([]platform.SearchHit, error) {
	var parsed struct {
		Result struct {
			Songs []struct {
				ID   json.RawMessage `json:"id"`
				Name string          `json:"name"`
				Fee  int             `json:"fee"`
				AR   []struct {
					Name string `json:"name"`
				} `json:"ar"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Album struct {
					Name string `json:"name"`
				} `json:"album"`
				Duration int `json:"duration"`
			} `json:"songs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("netease search payload: malformed response")
	}

	hits := make([]platform.SearchHit, 0, len(parsed.Result.Songs))
	for _, song := range parsed.Result.Songs {
		id, err := responseID(song.ID)
		if err != nil || song.Name == "" {
			return nil, fmt.Errorf("netease search payload: malformed song")
		}
		artists := make([]string, 0, len(song.AR))
		for _, artist := range song.AR {
			if artist.Name != "" {
				artists = append(artists, artist.Name)
			}
		}
		if len(artists) == 0 {
			for _, artist := range song.Artists {
				if artist.Name != "" {
					artists = append(artists, artist.Name)
				}
			}
		}
		hits = append(hits, platform.SearchHit{
			Ref:      platform.TrackRef{Platform: platform.Netease, TrackID: id},
			Title:    song.Name,
			Artists:  artists,
			Album:    song.Album.Name,
			Duration: time.Duration(song.Duration) * time.Millisecond,
			PaidHint: song.Fee == 1,
		})
	}
	return hits, nil
}

// parseArtists decodes the cloud-search artist payload.
func parseArtists(body []byte) ([]platform.Artist, error) {
	var parsed struct {
		Result struct {
			Artists []struct {
				ID   json.RawMessage `json:"id"`
				Name string          `json:"name"`
			} `json:"artists"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("netease artist search payload: malformed response")
	}
	artists := make([]platform.Artist, 0, len(parsed.Result.Artists))
	for _, item := range parsed.Result.Artists {
		id, err := responseID(item.ID)
		if err != nil || id == "" || item.Name == "" {
			return nil, fmt.Errorf("netease artist search payload: malformed artist")
		}
		artists = append(artists, platform.Artist{Platform: platform.Netease, ID: id, Name: item.Name})
	}
	return artists, nil
}

// parseArtistSongs decodes the artist song endpoint response.
func parseArtistSongs(body []byte) ([]platform.SearchHit, error) {
	var parsed struct {
		Songs    []json.RawMessage `json:"songs"`
		HotSongs []json.RawMessage `json:"hotSongs"`
		Result   struct {
			Songs []json.RawMessage `json:"songs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("netease artist songs payload: malformed response")
	}
	songs := parsed.Songs
	if len(songs) == 0 {
		songs = parsed.HotSongs
	}
	if len(songs) == 0 {
		songs = parsed.Result.Songs
	}
	hits := make([]platform.SearchHit, 0, len(songs))
	for _, raw := range songs {
		hit, err := parseArtistSong(raw)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func parseArtistSong(raw json.RawMessage) (platform.SearchHit, error) {
	var song struct {
		ID   json.RawMessage `json:"id"`
		Name string          `json:"name"`
		Fee  int             `json:"fee"`
		AR   []struct {
			Name string `json:"name"`
		} `json:"ar"`
		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
		AL struct {
			Name string `json:"name"`
		} `json:"al"`
		Album struct {
			Name string `json:"name"`
		} `json:"album"`
		DT       int `json:"dt"`
		Duration int `json:"duration"`
	}
	if err := json.Unmarshal(raw, &song); err != nil {
		return platform.SearchHit{}, fmt.Errorf("netease artist songs payload: malformed song")
	}
	id, err := responseID(song.ID)
	if err != nil || id == "" || song.Name == "" {
		return platform.SearchHit{}, fmt.Errorf("netease artist songs payload: malformed song")
	}
	artists := make([]string, 0, len(song.AR))
	for _, artist := range song.AR {
		if artist.Name != "" {
			artists = append(artists, artist.Name)
		}
	}
	if len(artists) == 0 {
		for _, artist := range song.Artists {
			if artist.Name != "" {
				artists = append(artists, artist.Name)
			}
		}
	}
	album := song.AL.Name
	if album == "" {
		album = song.Album.Name
	}
	duration := song.DT
	if duration == 0 {
		duration = song.Duration
	}
	return platform.SearchHit{Ref: platform.TrackRef{Platform: platform.Netease, TrackID: id}, Title: song.Name, Artists: artists, Album: album, Duration: time.Duration(duration) * time.Millisecond, PaidHint: song.Fee == 1}, nil
}
