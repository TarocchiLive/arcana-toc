package qqmusic

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

var errSearchModel = errors.New("qqmusic: invalid search response model")

var requiredSearchSongFields = [...]string{
	"id", "mid", "name", "type", "singer", "album", "mv", "file", "pay",
	"interval", "isonly", "language", "genre", "index_cd", "index_album", "status",
	"label", "bpm", "ov", "sa", "es", "vs", "vi", "vf",
	"search_title", "title_main", "title_extra", "fav_show", "desc", "desc_icon",
	"hotness", "hotness_desc", "vec_hotness", "content", "newStatus", "protect",
	"relatedword_group",
}

// QQMusicApi v0.7.2 SearchByTypeResponse documents meta.nextpage as the
// next page number, with -1 meaning exhausted. It also maps meta.sum to
// total_num; unlike estimate_sum this is the exact total.
func searchHasMore(data map[string]any, options platform.SearchOptions, count int) bool {
	if count == 0 {
		return false
	}
	meta, _ := data["meta"].(map[string]any)
	if value, found := nestedOrModelField(data, meta, "nextpage", "nextpage"); found {
		next, valid := searchPageInteger(value)
		if valid && next == -1 {
			return false
		}
		if valid && next > options.Page {
			return true
		}
	}
	if value, found := nestedOrModelField(data, meta, "sum", "total_num"); found {
		total, valid := searchPageInteger(value)
		if valid && total >= 0 {
			return (options.Page-1)*options.Limit+count < total
		}
	}
	return true
}

func searchPageInteger(value any) (int, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	raw, err = normalizeModelValue(raw, "int")
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil
}

func validateSearchResponseModel(data map[string]any) ([]map[string]any, error) {
	meta, ok := data["meta"].(map[string]any)
	if !ok {
		meta = nil
	}
	for _, field := range []struct {
		model string
		path  string
		kind  string
	}{
		{model: "searchid", path: "searchid", kind: "str"},
		{model: "perpage", path: "perpage", kind: "int"},
		{model: "nextpage", path: "nextpage", kind: "int"},
		{model: "estimate_sum", path: "estimate_sum", kind: "int"},
		{model: "total_num", path: "sum", kind: "int"},
	} {
		value, found := nestedOrModelField(data, meta, field.path, field.model)
		if !found || !validSearchValue(value, field.kind) {
			return nil, errSearchModel
		}
	}

	body, ok := data["body"].(map[string]any)
	if !ok {
		body = nil
	}
	paths := []struct {
		field string
		path  string
	}{
		{field: "song", path: "item_song"},
		{field: "singer", path: "singer"},
		{field: "album", path: "item_album"},
		{field: "songlist", path: "item_songlist"},
		{field: "user", path: "item_user"},
		{field: "audio_alum", path: "item_audio"},
		{field: "mv", path: "item_mv"},
	}
	var songs []map[string]any
	for _, item := range paths {
		value, found := nestedOrModelField(data, body, item.path, item.field)
		items, ok := value.([]any)
		if !found || !ok {
			return nil, errSearchModel
		}
		for _, raw := range items {
			model, ok := raw.(map[string]any)
			if !ok {
				return nil, errSearchModel
			}
			if item.field == "song" {
				if err := validateSearchSongModel(model); err != nil {
					return nil, err
				}
				songs = append(songs, model)
			}
			if item.field != "song" {
				if err := validateAuxSearchModel(model, item.field); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := validateSearchSelectors(data, body); err != nil {
		return nil, err
	}
	return songs, nil
}

func nestedOrModelField(root, nested map[string]any, path, model string) (any, bool) {
	if nested != nil {
		if value, ok := nested[path]; ok {
			return value, true
		}
	}
	value, ok := root[model]
	return value, ok
}

func validSearchValue(value any, kind string) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	_, err = normalizeModelValue(raw, kind)
	return err == nil
}

func validateSearchSongModel(song map[string]any) error {
	fields := make(map[string]json.RawMessage, len(song))
	for name, value := range song {
		raw, err := json.Marshal(value)
		if err != nil {
			return errSearchModel
		}
		fields[name] = raw
	}
	for _, name := range requiredSearchSongFields {
		if raw, ok := fields[name]; !ok || string(raw) == "null" {
			return errSearchModel
		}
	}
	if err := validateSongFields(fields); err != nil {
		return errSearchModel
	}
	for _, group := range []struct{ names, kind string }{
		{"search_title title_main title_extra fav_show desc desc_icon hotness_desc content", "str"},
		{"newStatus protect", "int"},
	} {
		if err := normalizeModelFields(fields, group.names, group.kind); err != nil {
			return errSearchModel
		}
	}
	for _, name := range []string{"hotness", "relatedword_group"} {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(fields[name], &value); err != nil || value == nil {
			return errSearchModel
		}
	}
	var hotness []map[string]json.RawMessage
	if err := json.Unmarshal(fields["vec_hotness"], &hotness); err != nil || hotness == nil {
		return errSearchModel
	}
	for name, raw := range fields {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return errSearchModel
		}
		song[name] = value
	}
	return nil
}
