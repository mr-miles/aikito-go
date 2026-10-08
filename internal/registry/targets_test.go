package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targets_vectors.json is generated from the reference resolve_targets by
// testdata/gen_targets_vectors.py.
func TestResolveTargetsMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/targets_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	type vec struct {
		Path       string   `json:"path"`
		Canonical  string   `json:"canonical"`
		Consumers  []string `json:"consumers"`
		Display    []string `json:"display"`
		SameObject bool     `json:"same_object"`
	}
	var scenarios map[string]struct {
		Dirs    []string         `json:"dirs"`
		Targets map[string][]vec `json:"targets"`
	}
	if err := json.Unmarshal(data, &scenarios); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")
	for name, sc := range scenarios {
		t.Run(name, func(t *testing.T) {
			home := resolvedTempDir(t)
			proj := filepath.Join(home, "proj")
			for _, d := range append([]string{"proj"}, sc.Dirs...) {
				if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			doc := map[string]map[string]any{}
			for _, n := range BuiltinAgents {
				doc[n] = BundledAgentSpec(n)
			}
			reg, err := AgentRegistryFromDocument(doc, home)
			if err != nil {
				t.Fatal(err)
			}
			rel := func(p string) string {
				if strings.HasPrefix(p, proj) {
					return "@" + filepath.ToSlash(p[len(proj):])
				}
				if strings.HasPrefix(p, home) {
					return "~" + filepath.ToSlash(p[len(home):])
				}
				return p
			}
			for key, want := range sc.Targets {
				kind, mode, _ := strings.Cut(key, "/")
				got, err := ResolveTargets(kind, filepath.Join(home, "aikito"), home, reg,
					ResolveTargetsOptions{ProjectPath: proj, ProjectName: "p", ActiveOnly: mode == "active"})
				if err != nil {
					t.Fatal(err)
				}
				var gotV []vec
				for _, tg := range got {
					gotV = append(gotV, vec{rel(tg.Path), rel(tg.CanonicalSource), tg.Consumers, tg.ConsumerDisplayNames, tg.IsSameObject()})
				}
				g, _ := json.Marshal(gotV)
				w, _ := json.Marshal(want)
				if len(want) == 0 && len(gotV) == 0 {
					continue
				}
				if string(g) != string(w) {
					t.Errorf("%s:\n got %s\nwant %s", key, g, w)
				}
			}
		})
	}
}
