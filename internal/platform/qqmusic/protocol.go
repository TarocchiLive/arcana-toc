// Package qqmusic ports the QQMusicApi 0.7.2 protocol used by Arcana ToC.
// Reference: https://github.com/L-1124/QQMusicApi (GPL-3.0-or-later).
// This Go translation is distributed under this repository's GPL-3.0-or-later license.
package qqmusic

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	cgiEndpoint       = "https://u.y.qq.com/cgi-bin/musicu.fcg"
	cgiSignedEndpoint = "https://u.y.qq.com/cgi-bin/musics.fcg"
	maxResponseBytes  = 1 << 20
)

var (
	part1Indexes = [...]int{23, 14, 6, 36, 16, 7, 19}
	part2Indexes = [...]int{16, 1, 32, 12, 19, 27, 8, 5}
	scramble     = [...]byte{89, 39, 179, 150, 218, 82, 58, 252, 177, 52, 186, 123, 120, 64, 242, 133, 143, 161, 121, 179}
	safeMID      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ZZCSign computes the QQ Music CGI zzc signature exactly as qqmusic-api-python.
func ZZCSign(payload []byte) string {
	hash := sha1.Sum(payload)
	hexDigest := strings.ToUpper(hex.EncodeToString(hash[:]))
	var part1, part2 strings.Builder
	for _, index := range part1Indexes {
		part1.WriteByte(hexDigest[index])
	}
	for _, index := range part2Indexes {
		part2.WriteByte(hexDigest[index])
	}
	part3 := make([]byte, len(scramble))
	for i, value := range scramble {
		pair, _ := hex.DecodeString(hexDigest[i*2 : i*2+2])
		part3[i] = value ^ pair[0]
	}
	encoded := base64.StdEncoding.EncodeToString(part3)
	encoded = strings.NewReplacer("\\", "", "/", "", "+", "", "=", "").Replace(encoded)
	return strings.ToLower("zzc" + part1.String() + encoded + part2.String())
}

// Hash33 computes the provider's bounded hash33 value.
func Hash33(value string, seed int64) int64 {
	for _, character := range value {
		seed = (seed << 5) + seed + int64(character)
	}
	return seed & 2147483647
}

// JSONField is one member of an OrderedObject.
type JSONField struct {
	Name  string
	Value any
}

// OrderedObject preserves insertion order, which is required for signatures:
// Python's orjson serializes dictionaries in insertion order rather than sorting
// their keys. Generic map inputs remain sorted for deterministic test helpers.
type OrderedObject []JSONField

// Field constructs one ordered JSON member.
func Field(name string, value any) JSONField { return JSONField{Name: name, Value: value} }

// Object constructs an ordered JSON object.
func Object(fields ...JSONField) OrderedObject { return OrderedObject(fields) }

// RequestItem is one CGI module request.
type RequestItem struct {
	Module string
	Method string
	Param  any
	// PreserveBool retains JSON booleans when true. The Python request layer
	// otherwise recursively converts every bool to 0 or 1.
	PreserveBool bool
}

// BuildCGIPayload serializes common fields and request items with compact JSON
// suitable for signing. OrderedObject should be used for protocol calls whose
// field order comes from a Python dict/model; map inputs are sorted.
func BuildCGIPayload(comm any, requests ...RequestItem) ([]byte, error) {
	fields := make([]JSONField, 0, len(requests)+1)
	fields = append(fields, Field("comm", normalizeValue(comm, true)))
	for index, request := range requests {
		fields = append(fields, Field(fmt.Sprintf("req_%d", index), Object(
			Field("module", request.Module),
			Field("method", request.Method),
			Field("param", normalizeValue(request.Param, request.PreserveBool)),
		)))
	}
	return marshalJSON(Object(fields...))
}

func normalizeValue(value any, preserveBool bool) any {
	if value == nil {
		return nil
	}
	if !preserveBool {
		if typed, ok := value.(bool); ok {
			if typed {
				return 1
			}
			return 0
		}
	}
	switch typed := value.(type) {
	case OrderedObject:
		out := make(OrderedObject, len(typed))
		for index, field := range typed {
			out[index] = Field(field.Name, normalizeValue(field.Value, preserveBool))
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(OrderedObject, 0, len(keys))
		for _, key := range keys {
			out = append(out, Field(key, normalizeValue(typed[key], preserveBool)))
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeValue(item, preserveBool)
		}
		return out
	default:
		return normalizeReflect(value, preserveBool)
	}
}

func normalizeReflect(value any, preserveBool bool) any {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for index := range out {
			out[index] = normalizeValue(rv.Index(index).Interface(), preserveBool)
		}
		return out
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return value
		}
		keys := make([]string, 0, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			keys = append(keys, iter.Key().String())
		}
		sort.Strings(keys)
		out := make(OrderedObject, 0, len(keys))
		for _, key := range keys {
			item := rv.MapIndex(reflect.ValueOf(key).Convert(rv.Type().Key())).Interface()
			out = append(out, Field(key, normalizeValue(item, preserveBool)))
		}
		return out
	default:
		return value
	}
}

func marshalJSON(value any) ([]byte, error) {
	return appendJSON(nil, value)
}

func appendJSON(dst []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case OrderedObject:
		dst = append(dst, '{')
		for index, field := range typed {
			if index > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendJSONPrimitive(dst, field.Name)
			if err != nil {
				return nil, err
			}
			dst = append(dst, ':')
			dst, err = appendJSON(dst, field.Value)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, '}'), nil
	case []any:
		dst = append(dst, '[')
		for index, item := range typed {
			if index > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendJSON(dst, item)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case json.RawMessage:
		if !json.Valid(typed) {
			return nil, errors.New("qqmusic: invalid raw JSON")
		}
		return append(dst, typed...), nil
	default:
		rv := reflect.ValueOf(value)
		if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			dst = append(dst, '[')
			for index := 0; index < rv.Len(); index++ {
				if index > 0 {
					dst = append(dst, ',')
				}
				var err error
				dst, err = appendJSON(dst, rv.Index(index).Interface())
				if err != nil {
					return nil, err
				}
			}
			return append(dst, ']'), nil
		}
		return appendJSONPrimitive(dst, value)
	}
}

func appendJSONPrimitive(dst []byte, value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return append(dst, encoded...), nil
}

// BuildCGIRequest returns the exact JSON body and optional signed query.
func BuildCGIRequest(comm any, requests []RequestItem, sign bool, now time.Time) ([]byte, url.Values, error) {
	body, err := BuildCGIPayload(comm, requests...)
	if err != nil {
		return nil, nil, err
	}
	query := make(url.Values)
	if sign {
		query.Set("_", fmt.Sprintf("%d", now.UnixMilli()))
		query.Set("sign", ZZCSign(body))
	}
	return body, query, nil
}

// ParseCGIBatchResponse unwraps the gateway response while retaining request order.
func ParseCGIBatchResponse(raw []byte, expected int) ([]CGIResponse, error) {
	if expected < 0 {
		return nil, errors.New("qqmusic: negative CGI response count")
	}
	if len(raw) == 0 {
		return nil, errors.New("qqmusic: empty CGI response")
	}
	if len(raw) > maxResponseBytes {
		return nil, errors.New("qqmusic: CGI response too large")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("qqmusic: malformed CGI response")
	}
	var gatewayCode int
	if rawCode, ok := object["code"]; ok {
		if err := json.Unmarshal(rawCode, &gatewayCode); err != nil {
			return nil, errors.New("qqmusic: malformed CGI gateway code")
		}
	}
	if gatewayCode != 0 {
		return nil, fmt.Errorf("qqmusic: CGI gateway error %d", gatewayCode)
	}
	responses := make([]CGIResponse, expected)
	for index := range responses {
		rawItem, ok := object[fmt.Sprintf("req_%d", index)]
		if !ok {
			return nil, fmt.Errorf("qqmusic: CGI response missing req_%d", index)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &envelope); err != nil || envelope == nil {
			return nil, fmt.Errorf("qqmusic: malformed req_%d", index)
		}
		if _, ok := envelope["code"]; !ok {
			return nil, fmt.Errorf("qqmusic: malformed req_%d", index)
		}
		var item CGIResponse
		decoder := json.NewDecoder(bytes.NewReader(rawItem))
		decoder.UseNumber()
		if err := decoder.Decode(&item); err != nil {
			return nil, fmt.Errorf("qqmusic: malformed req_%d", index)
		}
		if _, ok := envelope["data"]; !ok {
			item.Data = make(map[string]any)
		}
		responses[index] = item
	}
	return responses, nil
}

// CGIResponse is a single unwrapped provider response.
type CGIResponse struct {
	Code int            `json:"code"`
	Data map[string]any `json:"data"`
}
