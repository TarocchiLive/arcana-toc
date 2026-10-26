package qqmusic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/google/uuid"
)

const (
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	maxQQBodyBytes   = 1 << 20
)

var (
	qqStatusPattern   = regexp.MustCompile(`ptuiCB\((.*?)\)`)
	qqArgumentPattern = regexp.MustCompile(`'((?:\\.|[^'])*)'`)
	qqSigPattern      = regexp.MustCompile(`[?&]ptsigx=([^&]+)&s_url`)
	qqUINPattern      = regexp.MustCompile(`[?&]uin=([^&]+)&service`)
	wxStatusPattern   = regexp.MustCompile(`window\.wx_errcode=(\d+);window\.wx_code='([^']*)'`)
)

// Qualities are ordered from standard to lossless-compatible formats.
var qualities = []string{formatMP3128, formatFLAC, formatTRY, formatACC96}

func Qualities() []string { return append([]string(nil), qualities...) }

// Account is a sanitized QQ Music identity.
type Account struct {
	MusicID  string `json:"musicid,omitempty"`
	LoggedIn bool   `json:"logged_in"`
}

// QR is a provider QR image and polling identifier. Image is sensitive login
// material: it may be returned only by the authenticated private API and must
// never be written to logs or public responses.
type QR struct {
	Channel    string `json:"channel"`
	Identifier string `json:"identifier"`
	MIME       string `json:"mime,omitempty"`
	Image      []byte `json:"-"`
}

const (
	QRCodeWaiting = 66
	QRCodeScanned = 67
	QRCodeDone    = 0
	QRCodeExpired = 65
	QRCodeRefused = 68
)

// Adapter implements platform.Adapter for QQ Music.
type Adapter struct {
	credentialPath     string
	credential         Credential
	client             *http.Client
	baseURL            *url.URL
	device             *DeviceContext
	guid               string
	mu                 sync.RWMutex
	loginMu            sync.Mutex
	mobile             map[string]*mobileSession
	enrollmentLock     *CredentialLock
	enrollment         *enrollmentSession
	enrollmentTimeout  time.Duration
	enrollmentStarting bool
	closed             bool
	resolveHook        func(context.Context, platform.TrackRef, string) (platform.ResolvedSource, error)
}

var _ platform.ArtistAdapter = (*Adapter)(nil)

// New restores a private credential file. Missing credentials create an
// anonymous adapter; account-required operations return ErrNotLoggedIn.
func New(credentialPath string) (*Adapter, error) {
	credential, err := ReadCredential(credentialPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		credential = Credential{}
	}
	client := &http.Client{Timeout: 40 * time.Second}
	device, err := NewDeviceContextWithOptions(DeviceContextOptions{HTTPClient: client, Credential: credential})
	if err != nil {
		return nil, err
	}
	return &Adapter{
		credentialPath: credentialPath,
		credential:     credential,
		client:         client,
		device:         device,
		guid:           randomGUID(),
	}, nil
}

// NewForTest injects an HTTP endpoint and credential for deterministic tests.
func NewForTest(client *http.Client, endpoint *url.URL, credential Credential) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if endpoint == nil {
		endpoint, _ = url.Parse(cgiEndpoint)
	}
	device := AndroidDevice{Version: AndroidOSVersion{Release: "10", SDK: 29}, QIMEI: "fixture-q16", QIMEI36: "fixture-q36", QIMEISaveTime: time.Now().Unix(), SessionUID: "fixture-uid", SessionSID: "fixture-sid", SessionSaveTime: time.Now().Unix(), OpenUDID: randomGUID()}
	state, err := NewDeviceContextWithOptions(DeviceContextOptions{HTTPClient: client, Credential: credential, Device: &device})
	if err != nil {
		panic(err)
	}
	return &Adapter{client: client, baseURL: endpoint, device: state, credential: credential, guid: randomGUID()}
}

func randomGUID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (a *Adapter) Platform() platform.Platform { return platform.QQMusic }

func (a *Adapter) Capabilities() platform.Capabilities {
	return platform.Capabilities{DirectURL: true, Seek: true, Volume: true, Qualities: Qualities()}
}

func (a *Adapter) CredentialPath() string { return a.credentialPath }

func (a *Adapter) Account(ctx context.Context) (Account, error) {
	a.mu.RLock()
	credential := a.credential
	a.mu.RUnlock()
	if credential.MusicID == "" || credential.MusicKey == "" {
		return Account{LoggedIn: false}, fmt.Errorf("qqmusic account: %w", platform.ErrNotLoggedIn)
	}
	response, err := a.callCGI(ctx, false, map[string]any{"tmeLoginType": credential.LoginType}, RequestItem{
		Module: "music.UserInfo.userInfoServer", Method: "GetLoginUserInfo", Param: Object(), PreserveBool: false,
	})
	if err != nil {
		if errors.Is(err, ErrCredentialExpired) || errors.Is(err, ErrNotLoggedIn) {
			return Account{LoggedIn: false}, fmt.Errorf("qqmusic account: %w", platform.ErrNotLoggedIn)
		}
		return Account{}, err
	}
	if response.Code != 0 {
		return Account{LoggedIn: false}, resultError(response.Code)
	}
	if intValue(response.Data["code"]) != 0 {
		return Account{LoggedIn: false}, fmt.Errorf("qqmusic account: %w", platform.ErrNotLoggedIn)
	}
	return Account{MusicID: credential.MusicID, LoggedIn: true}, nil
}

func (a *Adapter) Refresh(ctx context.Context) error {
	lock, err := a.lockAndReloadCredential()
	if err != nil {
		return err
	}
	err = a.refreshLocked(ctx)
	if lock != nil {
		err = errors.Join(err, lock.Close())
	}
	return err
}

// RefreshIfExpired checks the latest persisted credential and conditionally
// refreshes it while holding one cross-process credential transaction lock.
func (a *Adapter) RefreshIfExpired(ctx context.Context) (account Account, refreshed bool, err error) {
	lock, err := a.lockAndReloadCredential()
	if err != nil {
		return Account{}, false, err
	}
	if lock != nil {
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	account, err = a.Account(ctx)
	if err == nil && account.LoggedIn {
		return account, false, nil
	}
	if err != nil && !errors.Is(err, platform.ErrNotLoggedIn) {
		return Account{}, false, err
	}
	if err = a.refreshLocked(ctx); err != nil {
		return Account{}, false, err
	}
	account, err = a.Account(ctx)
	if err != nil {
		return account, true, err
	}
	if !account.LoggedIn {
		return account, true, fmt.Errorf("qqmusic refresh: %w", platform.ErrNotLoggedIn)
	}
	return account, true, nil
}
func (a *Adapter) lockAndReloadCredential() (*CredentialLock, error) {
	if a.credentialPath == "" {
		return nil, nil
	}
	lock, err := LockCredential(a.credentialPath)
	if err != nil {
		return nil, err
	}
	latest, err := ReadCredential(a.credentialPath)
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	a.mu.Lock()
	a.credential = latest
	a.mu.Unlock()
	return lock, nil
}

func (a *Adapter) refreshLocked(ctx context.Context) error {
	a.mu.RLock()
	credential := a.credential
	a.mu.RUnlock()
	if err := ValidateCredential(credential); err != nil {
		return fmt.Errorf("qqmusic refresh: %w", platform.ErrNotLoggedIn)
	}
	param := refreshParams(credential)
	responses, err := a.callCGI(ctx, false, map[string]any{"tmeLoginType": credential.LoginType}, RequestItem{
		Module: "music.login.LoginServer", Method: "Login", Param: param,
	})
	if err != nil {
		return err
	}
	if responses.Code != 0 {
		return &CredentialRefreshError{Code: responses.Code}
	}
	if code := intValue(responses.Data["code"]); code != 0 {
		return &CredentialRefreshError{Code: code}
	}
	var updated Credential
	if err := mapToCredential(responses.Data, &updated); err != nil {
		return fmt.Errorf("qqmusic refresh: invalid provider credential")
	}
	if err := ValidateCredential(updated); err != nil {
		return fmt.Errorf("qqmusic refresh: %w", ErrCredentialExpired)
	}
	if a.credentialPath != "" {
		if err := WriteCredential(a.credentialPath, updated); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.credential = updated
	a.mu.Unlock()
	return nil
}

func refreshParams(credential Credential) OrderedObject {
	if credential.LoginType == 1 {
		return Object(Field("openid", credential.OpenID), Field("refresh_token", credential.RefreshToken), Field("str_musicid", firstNonEmpty(credential.StrMusicID, credential.MusicID)), Field("musickey", credential.MusicKey), Field("unionid", credential.UnionID), Field("refresh_key", credential.RefreshKey), Field("loginMode", 2))
	}
	musicID := json.Number(credential.MusicID)
	if credential.LoginType == 2 {
		return Object(Field("openid", credential.OpenID), Field("access_token", credential.AccessToken), Field("refresh_token", credential.RefreshToken), Field("expired_in", credential.Expired), Field("musicid", musicID), Field("musickey", credential.MusicKey), Field("refresh_key", credential.RefreshKey), Field("loginMode", 2))
	}
	return Object(Field("openid", credential.OpenID), Field("access_token", credential.AccessToken), Field("refresh_token", credential.RefreshToken), Field("expired_in", credential.Expired), Field("str_musicid", firstNonEmpty(credential.StrMusicID, credential.MusicID)), Field("musicid", musicID), Field("musickey", credential.MusicKey), Field("unionid", credential.UnionID), Field("refresh_key", credential.RefreshKey), Field("loginMode", 2))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func mapToCredential(data map[string]any, destination *Credential) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}

func (a *Adapter) Search(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.SearchHit], error) {
	if strings.TrimSpace(keyword) == "" {
		return platform.SearchPage[platform.SearchHit]{}, errors.New("qqmusic search: keyword is empty")
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.SearchHit]{}, err
	}
	response, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{
		Module: "music.search.SearchCgiService", Method: "DoSearchForQQMusicMobile", Param: Object(
			Field("searchid", randomSearchID()), Field("query", keyword), Field("search_type", 0), Field("num_per_page", options.Limit), Field("page_num", options.Page), Field("highlight", false), Field("grp", true), Field("selectors", Object()), Field("vec_selectors", []any{}),
		),
	})
	if err != nil {
		return platform.SearchPage[platform.SearchHit]{}, err
	}
	if response.Code != 0 {
		return platform.SearchPage[platform.SearchHit]{}, resultError(response.Code)
	}
	items, err := parseSearchResponse(response.Data, options.Limit)
	return platform.SearchPage[platform.SearchHit]{Items: items, HasMore: searchHasMore(response.Data, options, len(items))}, err
}

// SearchArtists searches QQ Music's singer catalog. Singer MIDs, rather than
// numeric display IDs, are returned because the singer-song endpoint uses MID
// as its stable identity.
func (a *Adapter) SearchArtists(ctx context.Context, keyword string, options platform.SearchOptions) (platform.SearchPage[platform.Artist], error) {
	if strings.TrimSpace(keyword) == "" {
		return platform.SearchPage[platform.Artist]{}, errors.New("qqmusic artist search: keyword is empty")
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return platform.SearchPage[platform.Artist]{}, err
	}
	response, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{
		Module: "music.search.SearchCgiService", Method: "DoSearchForQQMusicMobile", Param: Object(
			Field("searchid", randomSearchID()), Field("query", keyword), Field("search_type", 1), Field("num_per_page", options.Limit), Field("page_num", options.Page), Field("highlight", false), Field("grp", true), Field("selectors", Object()), Field("vec_selectors", []any{}),
		),
	})
	if err != nil {
		return platform.SearchPage[platform.Artist]{}, err
	}
	if response.Code != 0 {
		return platform.SearchPage[platform.Artist]{}, resultError(response.Code)
	}
	items, err := parseArtistSearchResponse(response.Data, options.Limit)
	return platform.SearchPage[platform.Artist]{Items: items, HasMore: searchHasMore(response.Data, options, len(items))}, err
}

// ArtistSongs returns up to 100 hot songs for a QQ Music singer MID.
func (a *Adapter) ArtistSongs(ctx context.Context, id string) ([]platform.SearchHit, error) {
	id = strings.TrimSpace(id)
	if id == "" || !safeMID.MatchString(id) || strings.Trim(id, "0123456789") == "" {
		return nil, errors.New("qqmusic artist songs: invalid artist id")
	}
	response, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{
		Module: "musichall.song_list_server", Method: "GetSingerSongList", Param: Object(
			Field("singerMid", id), Field("order", 1), Field("number", 100), Field("begin", 0),
		),
	})
	if err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, resultError(response.Code)
	}
	return parseArtistSongsResponse(response.Data)
}

func parseArtistSearchResponse(data map[string]any, limit int) ([]platform.Artist, error) {
	if _, err := validateSearchResponseModel(data); err != nil {
		return nil, errors.New("qqmusic artist search: invalid response")
	}
	body, ok := data["body"].(map[string]any)
	if !ok {
		body = nil
	}
	value, found := nestedOrModelField(data, body, "singer", "singer")
	rawItems, ok := value.([]any)
	if !found || !ok {
		return nil, errors.New("qqmusic artist search: invalid response")
	}
	artists := make([]platform.Artist, 0, minInt(limit, len(rawItems)))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("qqmusic artist search: malformed artist")
		}
		mid := stringValue(item, "mid", "singerMid", "singerMID", "SingerMid", "singer_mid")
		name := stringValue(item, "name", "singerName", "singer_name")
		if mid == "" || !safeMID.MatchString(mid) || strings.Trim(mid, "0123456789") == "" || name == "" {
			return nil, errors.New("qqmusic artist search: malformed artist")
		}
		artists = append(artists, platform.Artist{Platform: platform.QQMusic, ID: mid, Name: name})
		if len(artists) == limit {
			break
		}
	}
	return artists, nil
}

func parseArtistSongsResponse(data map[string]any) ([]platform.SearchHit, error) {
	value, ok := data["songList"]
	if !ok {
		value = data["song_list"]
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("qqmusic artist songs: invalid response")
	}
	hits := make([]platform.SearchHit, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("qqmusic artist songs: malformed entry")
		}
		song, ok := item["songInfo"].(map[string]any)
		if !ok {
			song, ok = item["song_info"].(map[string]any)
		}
		if !ok {
			return nil, errors.New("qqmusic artist songs: malformed entry")
		}
		hit, err := parseCatalogSong(song, "qqmusic artist songs")
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func parseCatalogSong(item map[string]any, operation string) (platform.SearchHit, error) {
	fields, err := searchModelFields(item)
	if err != nil {
		return platform.SearchHit{}, errors.New(operation + ": malformed song")
	}
	for _, name := range []string{"id", "mid", "name", "type", "singer", "album", "mv", "file", "pay", "interval", "isonly", "language", "genre", "index_cd", "index_album", "status", "label", "bpm", "ov", "sa", "es", "vs", "vi", "vf"} {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return platform.SearchHit{}, errors.New(operation + ": malformed song")
		}
	}
	if err := validateSongFields(fields); err != nil {
		return platform.SearchHit{}, errors.New(operation + ": malformed song")
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return platform.SearchHit{}, errors.New(operation + ": malformed song")
	}
	var song map[string]any
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	if err := decoder.Decode(&song); err != nil {
		return platform.SearchHit{}, errors.New(operation + ": malformed song")
	}
	mid, _ := song["mid"].(string)
	title := stringValue(song, "name", "title")
	if err := ValidateMID(mid); err != nil || title == "" {
		return platform.SearchHit{}, errors.New(operation + ": malformed song")
	}
	album := ""
	if value, ok := song["album"].(map[string]any); ok {
		album = stringValue(value, "name", "albumName")
	}
	pay, _ := song["pay"].(map[string]any)
	return platform.SearchHit{
		Ref: platform.TrackRef{Platform: platform.QQMusic, TrackID: mid}, Title: title,
		Artists: stringList(song["singer"]), Album: album,
		Duration: time.Duration(int64Value(song["interval"])) * time.Second,
		PaidHint: intValue(pay["pay_play"]) != 0 || intValue(pay["pay_down"]) != 0,
	}, nil
}

func randomSearchID() string {
	return strconv.FormatInt(int64(rand.IntN(20)+1)*18014398509481984+int64(rand.IntN(4194305))*4294967296+time.Now().UnixMilli()%86400000, 10)
}

func parseSearchResponse(data map[string]any, limit int) ([]platform.SearchHit, error) {
	items, err := validateSearchResponseModel(data)
	if err != nil {
		return nil, err
	}
	hits := make([]platform.SearchHit, 0, minInt(limit, len(items)))
	for _, item := range items {
		mid, _ := item["mid"].(string)
		title := stringValue(item, "name", "title", "title_main")
		artists := stringList(item["singer"])
		album := ""
		if value, ok := item["album"].(map[string]any); ok {
			album, _ = value["name"].(string)
		}
		interval := int64Value(item["interval"])
		pay, _ := item["pay"].(map[string]any)
		hits = append(hits, platform.SearchHit{Ref: platform.TrackRef{Platform: platform.QQMusic, TrackID: mid}, Title: title, Artists: artists, Album: album, Duration: time.Duration(interval) * time.Second, PaidHint: intValue(pay["pay_play"]) != 0 || intValue(pay["pay_down"]) != 0})
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}

func stringValue(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(item, "name", "singerName", "singer_name")
		if name != "" {
			result = append(result, name)
		}
	}
	return result
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		value, _ := strconv.Atoi(typed.String())
		return value
	default:
		return 0
	}
}

func int64Value(value any) int64 {
	return int64(intValue(value))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a *Adapter) ResolveSource(ctx context.Context, ref platform.TrackRef, quality string) (platform.ResolvedSource, error) {
	if ref.Platform != platform.QQMusic {
		return platform.ResolvedSource{}, fmt.Errorf("qqmusic adapter: %w for %s", platform.ErrUnsupportedCapability, ref)
	}
	if err := ValidateMID(ref.TrackID); err != nil {
		return platform.ResolvedSource{}, fmt.Errorf("qqmusic song: %w", platform.ErrTrackNotFound)
	}
	defaultQuality := quality == ""
	if defaultQuality {
		quality = formatMP3128
	}
	if _, err := formatByName(quality); err != nil {
		return platform.ResolvedSource{}, &platform.QualityError{Track: ref, Quality: quality}
	}
	if a.resolveHook != nil {
		source, err := a.resolveHook(ctx, ref, quality)
		return source, mapPlatformResultError(err)
	}
	return a.resolveSource(ctx, ref, quality, defaultQuality)
}

func (a *Adapter) resolveSource(ctx context.Context, ref platform.TrackRef, quality string, defaultQuality bool) (platform.ResolvedSource, error) {
	detailResponse, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{Module: "music.pf_song_detail_svr", Method: "get_song_detail_yqq", Param: songDetailParams(ref.TrackID)})
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	if detailResponse.Code != 0 {
		return platform.ResolvedSource{}, resultError(detailResponse.Code)
	}
	detail, err := ParseSongDetail(mustJSON(detailResponse.Data))
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	if detail.MID != ref.TrackID || detail.Duration < 0 {
		return platform.ResolvedSource{}, errors.New("qqmusic: invalid song detail identity or duration")
	}
	// Default playback follows catalog entitlement: VIP tracks use the
	// provider's explicit TRY preview instead of requesting an unauthorized
	// complete MP3. Explicit quality requests never silently downgrade.
	if defaultQuality && detail.Payment.Play != 0 {
		quality = formatTRY
	}
	filename := ExpectedFilename(ref.TrackID, detail.MediaMID, quality)
	vkeyResponse, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{Module: "music.vkey.GetVkey", Method: "UrlGetVkey", Param: Object(Field("uin", a.musicID()), Field("filename", []string{filename}), Field("guid", a.guid), Field("songmid", []string{ref.TrackID}), Field("songtype", []int{0}), Field("ctx", 0))})
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	if vkeyResponse.Code != 0 {
		return platform.ResolvedSource{}, resultError(vkeyResponse.Code)
	}
	urls, err := ParseSongURLs(mustJSON(vkeyResponse.Data))
	if err != nil {
		return platform.ResolvedSource{}, errors.New("qqmusic: malformed vkey response")
	}
	var item SongURL
	matches := 0
	for _, candidate := range urls.Items {
		if candidate.MID == ref.TrackID && candidate.Filename == filename {
			item = candidate
			matches++
		}
	}
	if matches != 1 {
		return platform.ResolvedSource{}, errors.New("qqmusic: authorization response mismatch")
	}
	if err := ErrorForResult(item.Result); err != nil {
		return platform.ResolvedSource{}, mapPlatformResultError(err)
	}
	if item.PURL == "" {
		return platform.ResolvedSource{}, ErrVKeyFailed
	}
	cdnResponse, err := a.callCGI(ctx, false, map[string]any{}, RequestItem{Module: "music.audioCdnDispatch.cdnDispatch", Method: "GetCdnDispatch", Param: Object(Field("guid", a.guid), Field("uid", "0"), Field("use_new_domain", 1), Field("use_ipv6", 1))})
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	if cdnResponse.Code != 0 {
		return platform.ResolvedSource{}, resultError(cdnResponse.Code)
	}
	var dispatch struct {
		SIP []string `json:"sip"`
	}
	if err := json.Unmarshal(mustJSON(cdnResponse.Data), &dispatch); err != nil {
		return platform.ResolvedSource{}, errors.New("qqmusic: malformed CDN response")
	}
	base, err := SelectCDN(dispatch.SIP)
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	source, err := JoinCDNSource(base, item.PURL)
	if err != nil {
		return platform.ResolvedSource{}, err
	}
	expires := time.Time{}
	if urls.Expiration > 0 {
		expires = time.Now().UTC().Add(time.Duration(urls.Expiration) * time.Second)
	}
	return platform.ResolvedSource{Kind: platform.SourceURL, URL: source, Quality: quality, ExpiresAt: expires}, nil
}

func mustJSON(value map[string]any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func (a *Adapter) musicID() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.credential.StrMusicID
}

func mapPlatformResultError(err error) error {
	switch {
	case errors.Is(err, ErrPermissionDenied):
		return fmt.Errorf("qqmusic: %w", platform.ErrPermissionDenied)
	case errors.Is(err, ErrVKeyFailed):
		return fmt.Errorf("qqmusic: %w", platform.ErrPermissionDenied)
	case errors.Is(err, ErrDeviceRestricted):
		return fmt.Errorf("qqmusic: %w", platform.ErrPermissionDenied)
	default:
		return err
	}
}

func resultError(code int) error {
	switch code {
	case 20279:
		return ErrLoginDeviceLimit
	case 1000, 104400, 104401:
		return fmt.Errorf("qqmusic: %w", platform.ErrNotLoggedIn)
	case 104003, 104004, 104013:
		return mapPlatformResultError(ErrorForResult(code))
	default:
		return fmt.Errorf("qqmusic: provider result %d", code)
	}
}

func (a *Adapter) callCGI(ctx context.Context, signed bool, comm map[string]any, requests ...RequestItem) (CGIResponse, error) {
	responses, err := a.callCGIBatch(ctx, signed, comm, requests...)
	if err != nil {
		return CGIResponse{}, err
	}
	return responses[0], nil
}

func (a *Adapter) callCGIBatch(ctx context.Context, signed bool, comm map[string]any, requests ...RequestItem) ([]CGIResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(requests) == 0 {
		return nil, errors.New("qqmusic: empty CGI batch")
	}
	a.mu.RLock()
	credential := a.credential
	a.mu.RUnlock()
	var common OrderedObject
	var err error
	userAgent := "QQMusic 14090008(android 10)"
	if requests[0].Module == "music.pf_song_detail_svr" {
		common, err = BuildWebComm(credential)
		userAgent = defaultUserAgent
	} else {
		a.device.SetCredential(credential)
		err = a.device.EnsureSession(ctx)
		common = a.device.AndroidComm()
	}
	if err != nil {
		return nil, err
	}
	for key, value := range comm {
		found := false
		for index := range common {
			if common[index].Name == key {
				common[index].Value = value
				found = true
				break
			}
		}
		if !found {
			common = append(common, Field(key, value))
		}
	}
	body, query, err := BuildCGIRequest(common, requests, signed, time.Now())
	if err != nil {
		return nil, err
	}
	endpoint := cgiEndpoint
	if a.baseURL != nil {
		endpoint = a.baseURL.String()
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("qqmusic: invalid CGI endpoint")
	}
	if a.baseURL != nil && (parsed.Path == "" || parsed.Path == "/") {
		parsed.Path = "/cgi-bin/musicu.fcg"
	}
	if signed {
		if a.baseURL != nil {
			parsed.Path = "/cgi-bin/musics.fcg"
		} else {
			parsed, _ = url.Parse(cgiSignedEndpoint)
		}
	}
	if len(query) > 0 {
		parsed.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("qqmusic: build CGI request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if a.client == nil {
		a.client = http.DefaultClient
	}
	response, err := a.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("qqmusic: CGI request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qqmusic: CGI HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxQQBodyBytes+1))
	if err != nil || len(raw) > maxQQBodyBytes {
		return nil, errors.New("qqmusic: CGI response invalid")
	}
	return ParseCGIBatchResponse(raw, len(requests))
}

func decodeBase64Image(value string) ([]byte, error) {
	if index := strings.LastIndexByte(value, ','); index >= 0 {
		value = value[index+1:]
	}
	return base64.StdEncoding.DecodeString(value)
}
