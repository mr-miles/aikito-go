//go:build aikito_faultinject

package faultinject

import (
	"os"
	"strconv"
	"strings"
)

// AIKITO_FAULT=<point>:<n> exits with status 137 (as SIGKILL would) right
// after the n-th time <point> is reached.
func init() {
	name, count, ok := strings.Cut(os.Getenv("AIKITO_FAULT"), ":")
	n, err := strconv.Atoi(count)
	if !ok || err != nil || n < 1 {
		return
	}
	seen := 0
	hook = func(point string) {
		if point != name {
			return
		}
		seen++
		if seen == n {
			os.Exit(137)
		}
	}
}
