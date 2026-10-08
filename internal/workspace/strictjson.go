package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeStrictJSON parses a single JSON value with the same strictness as
// Python's json.loads(text, object_pairs_hook=<reject duplicates>,
// parse_constant=<reject NaN/Infinity>): duplicate object keys are an error,
// and (via Go's standard JSON grammar, which has no NaN/Infinity extension at
// all) bare NaN/Infinity/-Infinity tokens are already rejected with no extra
// work needed. Numbers decode as json.Number so callers can distinguish
// integer from float literals exactly as Python's int/float does.
func DecodeStrictJSON(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	val, err := decodeStrictJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return val, nil
}

func decodeStrictJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("expected string object key")
				}
				if _, exists := obj[key]; exists {
					return nil, fmt.Errorf("duplicate object key: %s", key)
				}
				val, err := decodeStrictJSONValue(dec)
				if err != nil {
					return nil, err
				}
				obj[key] = val
			}
			if _, err := dec.Token(); err != nil { // consume closing '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeStrictJSONValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume closing ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %v", t)
		}
	case nil, bool, json.Number, string:
		return t, nil
	default:
		return nil, fmt.Errorf("unexpected JSON token %v", tok)
	}
}
