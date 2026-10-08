package cli

import "testing"

func TestIsReleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"v0.1.0":                             true,
		"v1.2.3-rc.1":                        true,
		"(devel)":                            false,
		"":                                   false,
		"v0.0.0-20261008143133-938c71b01fb6": false,
		"v0.0.0-20261008143133-938c71b01fb6+dirty": false,
		"v0.1.1-0.20261008143133-938c71b01fb6":     false,
		"v0.1.0+dirty":                             false,
	}
	for v, want := range cases {
		if got := isReleaseVersion(v); got != want {
			t.Errorf("isReleaseVersion(%q) = %v, want %v", v, got, want)
		}
	}
}
