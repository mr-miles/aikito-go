package mcp

import (
	"os"
	"testing"
)

// loadGolden reads testdata/mcp_golden.json using this package's own
// order-preserving JSON parser (not encoding/json's map[string]any, which
// would lose the Python-dict insertion order the fixture is encoding).
func loadGolden(t *testing.T) *OrderedObject {
	t.Helper()
	data, err := os.ReadFile("testdata/mcp_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseJSONOrdered(string(data))
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := v.(*OrderedObject)
	if !ok {
		t.Fatal("expected top-level object")
	}
	return obj
}

func mustArray(t *testing.T, v any) []any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", v)
	}
	return arr
}

func mustObj(t *testing.T, v any) *OrderedObject {
	t.Helper()
	obj, ok := v.(*OrderedObject)
	if !ok {
		t.Fatalf("expected object, got %T", v)
	}
	return obj
}

func getStr(t *testing.T, o *OrderedObject, key string) string {
	t.Helper()
	v, ok := o.Get(key)
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string for %q, got %T", key, v)
	}
	return s
}

// TestJSONCUpdateAgainstPython cross-validates UpdateJSONCServer against
// output recorded from the real Python update_jsonc_server.
func TestJSONCUpdateAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("jsonc_update", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			desired := mustObj(t, entry.GetOr("desired", NewOrderedObject()))
			wantOut := getStr(t, entry, "out")
			got, err := UpdateJSONCServer(in, server, desired)
			if err != nil {
				t.Fatalf("UpdateJSONCServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestJSONCRemoveAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("jsonc_remove", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			wantOut := getStr(t, entry, "out")
			got, err := RemoveJSONCServer(in, server)
			if err != nil {
				t.Fatalf("RemoveJSONCServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestTOMLUpdateAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("toml_update", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			desired := mustObj(t, entry.GetOr("desired", NewOrderedObject()))
			wantOut := getStr(t, entry, "out")
			got, err := UpdateTOMLServer(in, server, desired)
			if err != nil {
				t.Fatalf("UpdateTOMLServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestTOMLRemoveAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("toml_remove", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			wantOut := getStr(t, entry, "out")
			got, err := RemoveTOMLServer(in, server)
			if err != nil {
				t.Fatalf("RemoveTOMLServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestCordisUpdateAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("cordis_update", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			desired := mustObj(t, entry.GetOr("desired", NewOrderedObject()))
			wantOut := getStr(t, entry, "out")
			got, err := UpdateDSHCordisServer(in, server, desired)
			if err != nil {
				t.Fatalf("UpdateDSHCordisServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestCordisRemoveAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("cordis_remove", []any{}))
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		t.Run(name, func(t *testing.T) {
			in := getStr(t, entry, "in")
			server := getStr(t, entry, "server")
			wantOut := getStr(t, entry, "out")
			got, err := RemoveDSHCordisServer(in, server)
			if err != nil {
				t.Fatalf("RemoveDSHCordisServer error: %v", err)
			}
			if got != wantOut {
				t.Errorf("mismatch:\n got=%q\nwant=%q", got, wantOut)
			}
		})
	}
}

func TestFingerprintAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("fingerprint", []any{}))
	for i, c := range cases {
		entry := mustObj(t, c)
		value := mustObj(t, entry.GetOr("value", NewOrderedObject()))
		want := getStr(t, entry, "fingerprint")
		got := Fingerprint(value)
		if got != want {
			t.Errorf("case %d: Fingerprint mismatch: got=%s want=%s", i, got, want)
		}
	}
}

// buildFns maps each golden "name" prefix to the Go BuildDesired function it
// exercises, matching the fixture generator's test list exactly.
var buildFnsByName = map[string]func(string, *OrderedObject, *BasicTokenAuth, *OrderedObject, string) (*OrderedObject, bool, string){
	"toml_no_auth_no_headers":      buildTOML,
	"toml_with_auth":               buildTOML,
	"toml_with_headers_mixed":      buildTOML,
	"grok_with_auth":               buildGrokTOML,
	"grok_with_headers":            buildGrokTOML,
	"jsonc_default_timeout":        buildJSONC,
	"jsonc_override_timeout":       buildJSONC,
	"jsonc_with_auth":              buildJSONC,
	"jsonc_with_headers":           buildJSONC,
	"claude_with_auth":             buildClaudeJSON,
	"claude_with_headers":          buildClaudeJSON,
	"copilot_no_headers_no_auth":   buildCopilotJSON,
	"copilot_with_headers":         buildCopilotJSON,
	"copilot_with_auth_no_headers": buildCopilotJSON,
	"dsh_basic":                    buildDSHCordis,
	"dsh_with_name_override":       buildDSHCordis,
	"dsh_with_auth":                buildDSHCordis,
}

func TestBuildDesiredAgainstPython(t *testing.T) {
	golden := loadGolden(t)
	cases := mustArray(t, golden.GetOr("build_desired", []any{}))
	auth := &BasicTokenAuth{AccountEmail: "user@example.com", TokenEnv: "MY_TOKEN", AuthorizationEnv: "MY_AUTH_ENV"}
	for _, c := range cases {
		entry := mustObj(t, c)
		name := getStr(t, entry, "name")
		fn, ok := buildFnsByName[name]
		if !ok {
			t.Fatalf("no Go BuildDesired mapped for case %q", name)
		}
		t.Run(name, func(t *testing.T) {
			wantDesired := mustObj(t, entry.GetOr("desired", NewOrderedObject()))
			wantSecret, _ := entry.GetOr("contains_secret", false).(bool)
			wantMissing := getStr(t, entry, "missing_env")

			var authArg *BasicTokenAuth
			var headers *OrderedObject
			override := NewOrderedObject()
			switch name {
			case "toml_with_auth", "grok_with_auth", "jsonc_with_auth", "claude_with_auth", "copilot_with_auth_no_headers", "dsh_with_auth":
				authArg = auth
			case "toml_with_headers_mixed":
				headers = OO("X-Static", "val", "X-Env", "${FOO_ENV}")
			case "grok_with_headers":
				headers = OO("X-Custom", "val")
			case "jsonc_override_timeout":
				override = OO("timeout", int64(5000))
			case "jsonc_with_headers":
				headers = OO("X-Custom", "${FOO_ENV}", "X-Static", "val")
			case "claude_with_headers":
				headers = OO("X-Custom", "{env:FOO_ENV}")
			case "copilot_with_headers":
				headers = OO("X-Custom", "${FOO_ENV}")
			case "dsh_with_name_override":
				override = OO("name", "custom-name", "timeout", int64(1000))
			}
			if headers == nil {
				headers = NewOrderedObject()
			}

			gotDesired, gotSecret, gotMissing := fn("https://x.com", override, authArg, headers, "srv")
			if !JSONEqual(gotDesired, wantDesired) {
				t.Errorf("desired mismatch:\n got=%s\nwant=%s", DumpIndented(gotDesired), DumpIndented(wantDesired))
			}
			if gotSecret != wantSecret {
				t.Errorf("contains_secret = %v, want %v", gotSecret, wantSecret)
			}
			if gotMissing != wantMissing {
				t.Errorf("missing_env = %q, want %q", gotMissing, wantMissing)
			}
		})
	}
}
