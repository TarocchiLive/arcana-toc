package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
)

const (
	tuiRequestTimeout  = 45 * time.Second
	tuiPlaylistTimeout = 3 * time.Minute
	tuiResponseLimit   = 1 << 20
)

type tuiPlatform string

const (
	tuiPlatformAll     tuiPlatform = "all"
	tuiPlatformNetEase tuiPlatform = "netease"
	tuiPlatformQQMusic tuiPlatform = "qqmusic"
)

func (p tuiPlatform) label() string {
	switch p {
	case tuiPlatformAll:
		return "All platforms"
	case tuiPlatformQQMusic:
		return "QQ Music"
	default:
		return "NetEase Cloud Music"
	}
}

type tuiClient struct {
	ctx     context.Context
	baseURL string
	http    *http.Client
}

func newTUIClient(cfg config.Config) *tuiClient {
	return &tuiClient{
		baseURL: "http://" + cfg.Addr(),
		http:    &http.Client{Timeout: tuiRequestTimeout},
	}
}

func (c *tuiClient) requestContext() (context.Context, context.CancelFunc) {
	return c.requestContextWithTimeout(tuiRequestTimeout)
}

func (c *tuiClient) requestContextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}

type tuiAPIError struct {
	Error  string            `json:"error"`
	Errors map[string]string `json:"errors"`
}
type tuiHTTPError struct {
	statusCode     int
	message        string
	platformErrors map[string]string
}

func (e *tuiHTTPError) Error() string {
	message := e.message
	keys := make([]string, 0, len(e.platformErrors))
	for platform := range e.platformErrors {
		keys = append(keys, platform)
	}
	sort.Strings(keys)
	for _, platform := range keys {
		if message != "" {
			message += "; "
		}
		message += platform + ": " + e.platformErrors[platform]
	}
	if message == "" {
		return fmt.Sprintf("service returned HTTP %d", e.statusCode)
	}
	return fmt.Sprintf("service returned HTTP %d: %s", e.statusCode, message)
}

func isAccountNotLoggedIn(err error) bool {
	var httpErr *tuiHTTPError
	return errors.As(err, &httpErr) && httpErr.statusCode == http.StatusUnauthorized && httpErr.message == "account not logged in"
}

func (c *tuiClient) call(ctx context.Context, method, path string, request, response any) error {
	var body io.Reader
	if request != nil {
		raw, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.http
	if method == http.MethodPost && path == "/api/playlists/play" {
		// Full catalog loading and first-track download need the playlist budget.
		copy := *client
		copy.Timeout = tuiPlaylistTimeout
		client = &copy
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	limit := tuiResponseLimit
	if method == http.MethodGet && path == "/api/queue" {
		// A complete imported playlist can contain thousands of queue entries.
		limit = 16 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(raw) > limit {
		return errors.New("service response too large")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiErr tuiAPIError
		if json.Unmarshal(raw, &apiErr) == nil {
			return &tuiHTTPError{statusCode: resp.StatusCode, message: apiErr.Error, platformErrors: apiErr.Errors}
		}
		return &tuiHTTPError{statusCode: resp.StatusCode}
	}
	if response == nil {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("service returned an empty JSON response")
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return errors.New("service returned malformed JSON")
	}
	return nil
}

func (c *tuiClient) health(ctx context.Context) error {
	var response struct {
		Status string `json:"status"`
	}
	if err := c.call(ctx, http.MethodGet, "/healthz", nil, &response); err != nil {
		return err
	}
	if response.Status != "ok" {
		return errors.New("service health check failed")
	}
	return nil
}

type tuiQueueEntry struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	TrackID  string `json:"track_id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	VIP      bool   `json:"vip"`
	Status   string `json:"status"`
}

type tuiHistoryEntry struct {
	ID         int64     `json:"id"`
	QueueID    int64     `json:"queue_id"`
	Platform   string    `json:"platform"`
	TrackID    string    `json:"track_id"`
	Title      string    `json:"title"`
	Artist     string    `json:"artist"`
	VIP        bool      `json:"vip"`
	Outcome    string    `json:"outcome"`
	FinishedAt time.Time `json:"finished_at"`
}

type tuiPlayback struct {
	State    string         `json:"state"`
	Mode     string         `json:"mode"`
	Entry    *tuiQueueEntry `json:"entry"`
	Position time.Duration  `json:"position"`
	Duration time.Duration  `json:"duration"`
	Volume   int            `json:"volume"`
	Error    string         `json:"error"`
}

type tuiStatus struct {
	Version  string      `json:"version"`
	Playback tuiPlayback `json:"playback"`
}

type tuiAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LoggedIn bool   `json:"logged_in"`
}

type tuiSearchHit struct {
	Ref struct {
		Platform string `json:"platform"`
		TrackID  string `json:"track_id"`
	} `json:"ref"`
	Title    string        `json:"title"`
	Artists  []string      `json:"artists"`
	Album    string        `json:"album"`
	Duration time.Duration `json:"duration_ns"`
	PaidHint bool          `json:"paid_hint"`
}

func (c *tuiClient) status(ctx context.Context) (tuiStatus, error) {
	var response tuiStatus
	err := c.call(ctx, http.MethodGet, "/api/status", nil, &response)
	return response, err
}

func (c *tuiClient) queue(ctx context.Context) ([]tuiQueueEntry, error) {
	var response struct {
		Entries []tuiQueueEntry `json:"entries"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/queue", nil, &response); err != nil {
		return nil, err
	}
	return response.Entries, nil
}

func (c *tuiClient) history(ctx context.Context) ([]tuiHistoryEntry, error) {
	var response struct {
		Entries []tuiHistoryEntry `json:"entries"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/history", nil, &response); err != nil {
		return nil, err
	}
	return response.Entries, nil
}

func (c *tuiClient) account(ctx context.Context, platform tuiPlatform) (tuiAccount, error) {
	if platform == tuiPlatformQQMusic {
		var response struct {
			MusicID  string `json:"musicid"`
			LoggedIn bool   `json:"logged_in"`
		}
		err := c.call(ctx, http.MethodGet, "/api/qqmusic/account", nil, &response)
		if isAccountNotLoggedIn(err) {
			return tuiAccount{LoggedIn: false}, nil
		}
		return tuiAccount{ID: response.MusicID, Name: response.MusicID, LoggedIn: response.LoggedIn}, err
	}
	var response tuiAccount
	err := c.call(ctx, http.MethodGet, "/api/netease/account", nil, &response)
	if isAccountNotLoggedIn(err) {
		return tuiAccount{LoggedIn: false}, nil
	}
	return response, err
}

func (c *tuiClient) refreshAccount(ctx context.Context, platform tuiPlatform) error {
	return c.call(ctx, http.MethodPost, "/api/"+string(platform)+"/refresh", nil, nil)
}

type tuiSearchPage struct {
	Hits      []tuiSearchHit    `json:"hits"`
	Artists   []tuiArtist       `json:"artists"`
	Playlists []tuiPlaylist     `json:"playlists"`
	Errors    map[string]string `json:"errors"`
	Page      int               `json:"page"`
	HasMore   map[string]bool   `json:"has_more"`
}

func (c *tuiClient) searchPage(ctx context.Context, platform tuiPlatform, mode tuiSearchMode, keyword string, page int) (tuiSearchPage, error) {
	var response tuiSearchPage
	if mode == tuiSearchPlaylists {
		query := url.Values{"platform": {string(platform)}, "q": {keyword}, "limit": {"20"}, "page": {fmt.Sprint(page)}}
		err := c.call(ctx, http.MethodGet, "/api/playlists/search?"+query.Encode(), nil, &response)
		return response, err
	}
	request := struct {
		Platform string `json:"platform"`
		Keyword  string `json:"keyword"`
		Limit    int    `json:"limit"`
		Page     int    `json:"page"`
	}{Platform: string(platform), Keyword: keyword, Limit: 20, Page: page}
	path := "/api/search"
	if mode == tuiSearchArtists {
		path = "/api/artists/search"
	}
	err := c.call(ctx, http.MethodPost, path, request, &response)
	return response, err
}

type tuiArtist struct {
	Platform tuiPlatform `json:"platform"`
	ID       string      `json:"id"`
	Name     string      `json:"name"`
}

type tuiPlaylist struct {
	Platform tuiPlatform `json:"platform"`
	ID       string      `json:"id"`
	Name     string      `json:"name"`
}

func (c *tuiClient) playPlaylist(ctx context.Context, playlist tuiPlaylist) error {
	request := struct {
		Platform tuiPlatform `json:"platform"`
		ID       string      `json:"id"`
	}{Platform: playlist.Platform, ID: playlist.ID}
	return c.call(ctx, http.MethodPost, "/api/playlists/play", request, nil)
}

func (c *tuiClient) artistSongs(ctx context.Context, platform tuiPlatform, id string) ([]tuiSearchHit, error) {
	var response struct {
		Hits []tuiSearchHit `json:"hits"`
	}
	path := "/api/artists/" + url.PathEscape(string(platform)) + "/" + url.PathEscape(id) + "/songs"
	if err := c.call(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Hits, nil
}

func (c *tuiClient) enqueue(ctx context.Context, hit tuiSearchHit) error {
	request := struct {
		Platform string `json:"platform"`
		TrackID  string `json:"track_id"`
		Title    string `json:"title"`
		Artist   string `json:"artist"`
		VIP      bool   `json:"vip"`
	}{
		Platform: hit.Ref.Platform,
		TrackID:  hit.Ref.TrackID,
		Title:    hit.Title,
		Artist:   strings.Join(hit.Artists, ", "),
		VIP:      hit.PaidHint,
	}
	return c.call(ctx, http.MethodPost, "/api/queue", request, nil)
}

func (c *tuiClient) deleteQueueEntry(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodDelete, fmt.Sprintf("/api/queue/%d", id), nil, nil)
}

func (c *tuiClient) playback(ctx context.Context, path string) error {
	return c.call(ctx, http.MethodPost, path, nil, nil)
}

func (c *tuiClient) setPlaybackMode(ctx context.Context, mode string) error {
	return c.call(ctx, http.MethodPost, "/api/playback/mode", struct {
		Mode string `json:"mode"`
	}{Mode: mode}, nil)
}

func (c *tuiClient) setVolume(ctx context.Context, value int) error {
	return c.call(ctx, http.MethodPost, "/api/playback/volume", struct {
		Value int `json:"value"`
	}{Value: value}, nil)
}

func (c *tuiClient) seek(ctx context.Context, value int) error {
	return c.call(ctx, http.MethodPost, "/api/playback/seek", struct {
		Value int `json:"value"`
	}{Value: value}, nil)
}

type tuiLoginQR struct {
	Key        string `json:"key"`
	URL        string `json:"url"`
	Channel    string `json:"channel"`
	Identifier string `json:"identifier"`
	MIME       string `json:"mime"`
	Image      []byte `json:"image"`
}

func (c *tuiClient) startLogin(ctx context.Context, platform tuiPlatform, channel string) (tuiLoginQR, error) {
	var response tuiLoginQR
	path := "/api/netease/login"
	if platform == tuiPlatformQQMusic {
		if channel != "qq" && channel != "wx" && channel != "mobile" {
			return tuiLoginQR{}, errors.New("unsupported QQ Music login channel")
		}
		path = "/api/qqmusic/login?channel=" + url.QueryEscape(channel)
	}
	if err := c.call(ctx, http.MethodPost, path, nil, &response); err != nil {
		return tuiLoginQR{}, err
	}
	if platform == tuiPlatformQQMusic {
		if response.Identifier == "" || response.Channel != channel || len(response.Image) == 0 {
			return tuiLoginQR{}, errors.New("service returned an invalid QQ Music login QR code")
		}
		return response, nil
	}
	if response.Key == "" || response.URL == "" {
		return tuiLoginQR{}, errors.New("service returned an invalid login QR code")
	}
	response.Channel = "netease"
	response.Identifier = response.Key
	return response, nil
}

func (c *tuiClient) pollLogin(ctx context.Context, platform tuiPlatform, channel, identifier string) (int, error) {
	var response struct {
		Code *int `json:"code"`
	}
	path := "/api/netease/login?key=" + url.QueryEscape(identifier)
	if platform == tuiPlatformQQMusic {
		path = "/api/qqmusic/login?channel=" + url.QueryEscape(channel) + "&identifier=" + url.QueryEscape(identifier)
	}
	if err := c.call(ctx, http.MethodGet, path, nil, &response); err != nil {
		return 0, err
	}
	if response.Code == nil {
		return 0, errors.New("service returned no login status")
	}
	return *response.Code, nil
}
