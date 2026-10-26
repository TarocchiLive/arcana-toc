package qqmusic

import (
	"encoding/json"
	"strings"
)

func searchModelFields(model map[string]any) (map[string]json.RawMessage, error) {
	fields := make(map[string]json.RawMessage, len(model))
	for name, value := range model {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, errSearchModel
		}
		fields[name] = raw
	}
	return fields, nil
}

func requireSearchFields(fields map[string]json.RawMessage, names, kind string) error {
	for _, name := range strings.Fields(names) {
		if _, ok := fields[name]; !ok {
			return errSearchModel
		}
	}
	return normalizeModelFields(fields, names, kind)
}

func validateAuxSearchModel(model map[string]any, kind string) error {
	fields, err := searchModelFields(model)
	if err != nil {
		return err
	}
	var stringsRequired, intsRequired string
	switch kind {
	case "user":
		return nil
	case "singer":
		stringsRequired = "singerPic subtitle"
		intsRequired = "songNum albumNum mvNum"
		holder := map[string]json.RawMessage{"singer": json.RawMessage("[" + string(mustJSON(model)) + "]")}
		if err := normalizeSingerModels(holder); err != nil {
			return err
		}
	case "mv":
		stringsRequired = "pic publish_date singermid singername"
		intsRequired = "play_count duration singerid"
		if err := normalizeMVModel(fields); err != nil {
			return err
		}
	case "songlist":
		stringsRequired = "nickname"
		for _, group := range []struct{ aliases, kind string }{
			{"id tid dissid", "int"}, {"dirid dirId", "int"}, {"title dissname name", "str"}, {"picurl cover logo picUrl", "str"}, {"desc description", "str"}, {"songnum songNum song_cnt", "int"}, {"listennum playCnt play_cnt", "int"},
		} {
			if err := normalizeAliasField(fields, group.aliases, group.kind); err != nil {
				return err
			}
		}
		if err := normalizeModelFields(fields, "dirtype", "int"); err != nil {
			return err
		}
	case "album", "audio_alum":
		stringsRequired = "description hotness_desc pic pic_icon singer url"
		if config, ok := model["core_album_config"].(map[string]any); ok {
			for _, entry := range []struct{ path, target string }{{"album_type", "type"}, {"award_label", "award_label"}} {
				if value, ok := config[entry.path]; ok {
					raw, err := json.Marshal(value)
					if err != nil {
						return errSearchModel
					}
					fields[entry.target] = raw
				}
			}
		}
		if err := normalizeAlbumModel(fields); err != nil {
			return err
		}
		if err := normalizeModelFields(fields, "description2 award_label", "str"); err != nil {
			return err
		}
		if err := normalizeModelFields(fields, "type", "int"); err != nil {
			return err
		}
		for _, name := range []string{"desc_detail", "hotness", "label_new"} {
			var object map[string]any
			if json.Unmarshal(fields[name], &object) != nil || object == nil {
				return errSearchModel
			}
		}
		var ranking map[string]json.RawMessage
		if json.Unmarshal(fields["audio_play"], &ranking) != nil || ranking == nil {
			return errSearchModel
		}
		if err := requireSearchFields(ranking, "rank toplist", "str"); err != nil {
			return err
		}
		if err := requireSearchFields(fields, "tag_list", "strlist"); err != nil {
			return err
		}
		if err := normalizeSingerModels(map[string]json.RawMessage{"singer": fields["singer_list"]}); err != nil {
			return err
		}
	}
	if err := requireSearchFields(fields, stringsRequired, "str"); err != nil {
		return err
	}
	return requireSearchFields(fields, intsRequired, "int")
}

func validateSearchSelectors(root, body map[string]any) error {
	value, found := root["selectors"]
	if extra, ok := body["multi_extern_info"].(map[string]any); ok {
		if nested, ok := extra["selectors"]; ok {
			value = nested
			found = true
		}
	}
	if !found {
		return nil
	}
	groups, ok := value.([]any)
	if !ok {
		return errSearchModel
	}
	for _, group := range groups {
		items, ok := group.([]any)
		if !ok {
			return errSearchModel
		}
		for _, item := range items {
			model, ok := item.(map[string]any)
			if !ok {
				return errSearchModel
			}
			fields, err := searchModelFields(model)
			if err != nil {
				return err
			}
			if err := requireSearchFields(fields, "id type", "int"); err != nil {
				return err
			}
			if err := requireSearchFields(fields, "name", "str"); err != nil {
				return err
			}
		}
	}
	return nil
}
