package workspace

import toml "github.com/pelletier/go-toml/v2"

// DecodeTOML parses TOML bytes into a plain map[string]any tree: nested
// tables as map[string]any, arrays as []any, and TOML-native date/time
// values as go-toml/v2's Local* types or time.Time for offset datetimes —
// exactly the value shapes ValueFingerprint/CanonicalJSON expect.
func DecodeTOML(data []byte) (map[string]any, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}
