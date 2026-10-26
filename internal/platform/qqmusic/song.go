package qqmusic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	formatMP3128 = "MP3_128"
	formatFLAC   = "FLAC"
	formatTRY    = "TRY"
	formatACC96  = "ACC_96"
)

var formatInfo = map[string]struct {
	prefix string
	ext    string
}{
	formatMP3128: {prefix: "M500", ext: ".mp3"},
	formatFLAC:   {prefix: "F000", ext: ".flac"},
	formatTRY:    {prefix: "RS02", ext: ".mp3"},
	formatACC96:  {prefix: "C400", ext: ".m4a"},
}

var (
	ErrCredentialInvalid = errors.New("qqmusic: invalid credential")
	ErrNotLoggedIn       = errors.New("qqmusic: account not logged in")
	ErrCredentialExpired = errors.New("qqmusic: credential expired")
	ErrLoginDeviceLimit  = errors.New("qqmusic: login device limit exceeded (20279)")
	ErrPermissionDenied  = errors.New("qqmusic: permission denied")
	ErrVKeyFailed        = errors.New("qqmusic: vkey failed")
	ErrDeviceRestricted  = errors.New("qqmusic: device restricted")
	ErrSourceInvalid     = errors.New("qqmusic: invalid source")
)

// Format describes one supported, unencrypted QQ Music file type.
type Format struct {
	Name   string
	Prefix string
	Ext    string
}

func formatByName(name string) (Format, error) {
	item, ok := formatInfo[name]
	if !ok {
		return Format{}, fmt.Errorf("qqmusic: unsupported format %q", name)
	}
	return Format{Name: name, Prefix: item.prefix, Ext: item.ext}, nil
}

// ExpectedFilename mirrors SongFileInfo's path construction.
func ExpectedFilename(mid, mediaMID, format string) string {
	item, ok := formatInfo[format]
	if !ok {
		return ""
	}
	if mediaMID != "" {
		return item.prefix + mediaMID + item.ext
	}
	return item.prefix + mid + mid + item.ext
}

func ValidateMID(mid string) error {
	if !safeMID.MatchString(mid) {
		return errors.New("qqmusic: invalid song MID")
	}
	return nil
}

func songDetailParams(value string) OrderedObject {
	if value != "" && strings.Trim(value, "0123456789") == "" {
		number := strings.TrimLeft(value, "0")
		if number == "" {
			number = "0"
		}
		return Object(Field("song_id", json.Number(number)))
	}
	return Object(Field("song_mid", value))
}

// Payment captures QQ's play/download/month permission flags.
type Payment struct {
	Month  int `json:"pay_month"`
	Play   int `json:"pay_play"`
	Down   int `json:"pay_down"`
	Status int `json:"pay_status"`
}

// SongDetail is the subset needed by search and playback.
type SongDetail struct {
	MID      string
	Title    string
	Artists  []string
	MediaMID string
	Duration time.Duration
	Payment  Payment
	FileSize map[string]int64
}

func ParseSongDetail(raw []byte) (SongDetail, error) {
	var envelope struct {
		Track json.RawMessage `json:"track_info"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return SongDetail{}, errors.New("qqmusic: malformed song detail")
	}
	if len(envelope.Track) == 0 || string(envelope.Track) == "null" {
		return SongDetail{}, errors.New("qqmusic: song detail missing track")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Track, &fields); err != nil {
		return SongDetail{}, errors.New("qqmusic: malformed song detail track")
	}
	for _, name := range [...]string{"id", "mid", "name", "type", "singer", "album", "mv", "file", "pay", "interval", "isonly", "language", "genre", "index_cd", "index_album", "status", "label", "bpm", "ov", "sa", "es", "vs", "vi", "vf"} {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return SongDetail{}, errors.New("qqmusic: incomplete song detail track")
		}
	}
	if err := validateSongFields(fields); err != nil {
		return SongDetail{}, err
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return SongDetail{}, errSongModel
	}
	var item struct {
		MID      string                       `json:"mid"`
		Name     string                       `json:"name"`
		Title    string                       `json:"title"`
		Interval int64                        `json:"interval"`
		Singer   []map[string]json.RawMessage `json:"singer"`
		File     struct {
			MediaMID  string `json:"media_mid"`
			Size128   int64  `json:"size_128mp3"`
			SizeFLAC  int64  `json:"size_flac"`
			SizeTRY   int64  `json:"size_try"`
			SizeACC96 int64  `json:"size_96aac"`
		} `json:"file"`
		Pay struct {
			Month  int `json:"pay_month"`
			Play   int `json:"pay_play"`
			Down   int `json:"pay_down"`
			Status int `json:"pay_status"`
		} `json:"pay"`
	}
	if err := json.Unmarshal(normalized, &item); err != nil {
		return SongDetail{}, errors.New("qqmusic: malformed song detail track")
	}
	if err := ValidateMID(item.MID); err != nil {
		return SongDetail{}, err
	}
	title := item.Title
	if title == "" {
		title = item.Name
	}
	artists := make([]string, len(item.Singer))
	for index, singer := range item.Singer {
		for _, alias := range [...]string{"name", "singerName", "singer_name"} {
			if raw, ok := singer[alias]; ok {
				if json.Unmarshal(raw, &artists[index]) != nil {
					return SongDetail{}, errors.New("qqmusic: malformed singer name")
				}
				break
			}
		}
	}
	return SongDetail{
		MID:      item.MID,
		Title:    title,
		Artists:  artists,
		MediaMID: item.File.MediaMID,
		Duration: time.Duration(item.Interval) * time.Second,
		Payment:  Payment{Month: item.Pay.Month, Play: item.Pay.Play, Down: item.Pay.Down, Status: item.Pay.Status},
		FileSize: map[string]int64{formatMP3128: item.File.Size128, formatFLAC: item.File.SizeFLAC, formatTRY: item.File.SizeTRY, formatACC96: item.File.SizeACC96},
	}, nil
}

// SongURL is one per-format authorization result.
type SongURL struct {
	MID      string `json:"songmid"`
	Filename string `json:"filename"`
	PURL     string `json:"purl"`
	VKey     string `json:"vkey"`
	EKey     string `json:"ekey"`
	Result   int    `json:"result"`
}

type SongURLs struct {
	Expiration int       `json:"expiration"`
	Items      []SongURL `json:"midurlinfo"`
}

func ParseSongURLs(raw []byte) (SongURLs, error) {
	var got SongURLs
	if err := json.Unmarshal(raw, &got); err != nil {
		return SongURLs{}, errors.New("qqmusic: malformed song URLs")
	}
	if got.Items == nil {
		got.Items = []SongURL{}
	}
	for _, item := range got.Items {
		if item.MID != "" {
			if err := ValidateMID(item.MID); err != nil {
				return SongURLs{}, err
			}
		}
	}
	return got, nil
}

func ErrorForResult(code int) error {
	switch code {
	case 0:
		return nil
	case 104003:
		return fmt.Errorf("%w: result %d", ErrPermissionDenied, code)
	case 104004:
		return fmt.Errorf("%w: result %d", ErrVKeyFailed, code)
	case 104013:
		return fmt.Errorf("%w: result %d", ErrDeviceRestricted, code)
	default:
		return fmt.Errorf("qqmusic: authorization failed: result %d", code)
	}
}

func SelectCDN(cdns []string) (string, error) {
	for _, raw := range cdns {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		return raw, nil
	}
	return "", errors.New("qqmusic: no valid HTTP(S) CDN")
}

func JoinCDNSource(base, purl string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", ErrSourceInvalid
	}
	relative, err := url.Parse(purl)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.Path == "" {
		return "", ErrSourceInvalid
	}
	for _, segment := range strings.Split(relative.Path, "/") {
		if segment == ".." {
			return "", ErrSourceInvalid
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	return parsed.ResolveReference(relative).String(), nil
}
