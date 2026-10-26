package qqmusic

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

var (
	errSongModel        = errors.New("qqmusic: invalid song model field")
	pydanticIntegerText = regexp.MustCompile(`^[+-]?[0-9](?:_?[0-9])*(?:\.0+)?$`)
)

// validateSongFields applies the field types used by the fixed Python Song
// model. Unknown provider additions remain ignored, as in Pydantic.
func validateSongFields(fields map[string]json.RawMessage) error {
	for _, group := range []struct{ names, kind string }{
		{"id type interval isonly language genre index_cd index_album status bpm ov sa", "int"},
		{"mid name title subtitle time_public label es", "str"},
		{"vs", "strlist"}, {"vi", "intlist"}, {"vf", "floatlist"},
	} {
		if err := normalizeModelFields(fields, group.names, group.kind); err != nil {
			return err
		}
	}
	if err := normalizeSingerModels(fields); err != nil {
		return err
	}
	for _, name := range []string{"album", "mv", "file", "pay"} {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(fields[name], &nested); err != nil || nested == nil {
			return errSongModel
		}
		switch name {
		case "album":
			if err := normalizeAlbumModel(nested); err != nil {
				return err
			}
		case "mv":
			if err := normalizeMVModel(nested); err != nil {
				return err
			}
		case "file":
			if err := normalizeModelFields(nested, "size_24aac size_48aac size_96aac size_192ogg size_192aac size_128mp3 size_320mp3 size_flac size_dts size_try try_begin try_end size_96ogg size_dolby", "int"); err != nil {
				return err
			}
			if err := normalizeModelFields(nested, "media_mid", "str"); err != nil {
				return err
			}
			if err := normalizeModelFields(nested, "size_new", "intlist"); err != nil {
				return err
			}
		case "pay":
			if err := normalizeModelFields(nested, "pay_month price_track price_album pay_play pay_down pay_status time_free", "int"); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(nested)
		if err != nil {
			return errSongModel
		}
		fields[name] = encoded
	}
	return nil
}

func normalizeSingerModels(fields map[string]json.RawMessage) error {
	var singers []map[string]json.RawMessage
	if err := json.Unmarshal(fields["singer"], &singers); err != nil || singers == nil {
		return errSongModel
	}
	for _, singer := range singers {
		for _, field := range []struct{ aliases, kind string }{
			{"id singerID singerId SingerID singer_id", "int"},
			{"mid singerMid singerMID SingerMid singer_mid", "str"},
			{"name singerName singer_name", "str"},
			{"title singerName name", "str"},
			{"type SingerType vt", "int"},
			{"uin", "int"},
			{"pmid singerPmid singer_pmid pic_mid", "str"},
		} {
			if err := normalizeAliasField(singer, field.aliases, field.kind); err != nil {
				return err
			}
		}
	}
	encoded, err := json.Marshal(singers)
	if err != nil {
		return errSongModel
	}
	fields["singer"] = encoded
	return nil
}

func normalizeAlbumModel(fields map[string]json.RawMessage) error {
	for _, field := range []struct{ aliases, kind string }{
		{"id albumID", "int"},
		{"mid albumMid albumMID albummid", "str"},
		{"name albumName", "str"},
		{"title albumName name", "str"},
		{"subtitle albumTranName", "str"},
		{"time_public publish_date publishDate", "str"},
		{"pmid logo", "str"},
	} {
		if err := normalizeAliasField(fields, field.aliases, field.kind); err != nil {
			return err
		}
	}
	return nil
}

func normalizeMVModel(fields map[string]json.RawMessage) error {
	for _, field := range []struct{ aliases, kind string }{
		{"id sid mvid singerId", "int"},
		{"vid", "str"},
		{"vt type", "int"},
		{"name mvname title", "str"},
		{"title title_main name", "str"},
	} {
		if err := normalizeAliasField(fields, field.aliases, field.kind); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAliasField(fields map[string]json.RawMessage, aliases, kind string) error {
	for _, alias := range strings.Fields(aliases) {
		raw, ok := fields[alias]
		if !ok {
			continue
		}
		normalized, err := normalizeModelValue(raw, kind)
		if err != nil {
			return err
		}
		fields[alias] = normalized
		return nil
	}
	return nil
}

func normalizeModelFields(fields map[string]json.RawMessage, names, kind string) error {
	for _, name := range strings.Fields(names) {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		normalized, err := normalizeModelValue(raw, kind)
		if err != nil {
			return err
		}
		fields[name] = normalized
	}
	return nil
}

func normalizeModelValue(raw json.RawMessage, kind string) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil, errSongModel
	}
	if strings.HasSuffix(kind, "list") {
		var values []json.RawMessage
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &values) != nil {
			return nil, errSongModel
		}
		for i := range values {
			value, err := normalizeModelValue(values[i], strings.TrimSuffix(kind, "list"))
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		result, err := json.Marshal(values)
		return result, err
	}
	if kind == "str" {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, errSongModel
		}
		return raw, nil
	}
	value := string(raw)
	quoted := len(raw) > 0 && raw[0] == '"'
	if quoted {
		if json.Unmarshal(raw, &value) != nil {
			return nil, errSongModel
		}
	}
	if string(raw) == "true" {
		value = "1"
	} else if string(raw) == "false" {
		value = "0"
	}
	if kind == "int" {
		if quoted {
			value = strings.TrimSpace(value)
			if !pydanticIntegerText.MatchString(value) {
				return nil, errSongModel
			}
			value = strings.ReplaceAll(value, "_", "")
		}
		number, ok := new(big.Rat).SetString(value)
		if !ok || !number.IsInt() {
			return nil, errSongModel
		}
		return json.RawMessage(number.Num().String()), nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, errSongModel
	}
	encoded, err := json.Marshal(number)
	if err != nil {
		return nil, errSongModel
	}
	return encoded, nil
}
