package projectsync

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

var (
	conflictStartRe = regexp.MustCompile(`^<{7}([ \t].*)?$`)
	conflictSepRe   = regexp.MustCompile(`^(={7}|\|{7})([ \t].*)?$`)
	conflictEndRe   = regexp.MustCompile(`^>{7}([ \t].*)?$`)
	conflictAnyRe   = regexp.MustCompile(`^(<{7}|={7}|>{7}|\|{7})([ \t].*)?$`)
)

// splitLinesPy is str.splitlines().
func splitLinesPy(s string) []string {
	var lines []string
	start := 0
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\n', '\v', '\f', '\x1c', '\x1d', '\x1e', '\x85', ' ', ' ':
			lines = append(lines, string(runes[start:i]))
			start = i + 1
		case '\r':
			lines = append(lines, string(runes[start:i]))
			if i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(runes) {
		lines = append(lines, string(runes[start:]))
	}
	return lines
}

// blockingConflictLines is conflict.py's find_conflict_marker_lines,
// returning only the blocking line numbers.
func blockingConflictLines(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := splitLinesPy(compat.DecodeUTF8Replace(data))
	var blocking []int
	if strings.HasSuffix(path, ".toml") {
		for i, l := range lines {
			if conflictAnyRe.MatchString(l) {
				blocking = append(blocking, i+1)
			}
		}
		return blocking
	}
	set := map[int]bool{}
	pendingStart := 0
	var seps []int
	for i, l := range lines {
		n := i + 1
		switch {
		case conflictStartRe.MatchString(l):
			pendingStart, seps = n, nil
		case conflictSepRe.MatchString(l):
			if pendingStart != 0 {
				seps = append(seps, n)
			}
		case conflictEndRe.MatchString(l):
			if pendingStart != 0 && len(seps) > 0 {
				set[pendingStart] = true
				for _, s := range seps {
					set[s] = true
				}
				set[n] = true
			}
			pendingStart, seps = 0, nil
		}
	}
	for n := range set {
		blocking = append(blocking, n)
	}
	sort.Ints(blocking)
	return blocking
}

// sortPathsLikePython orders paths as sorted(list_of_Path) does: by
// component lists, not by raw string.
func sortPathsLikePython(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		a := strings.Split(filepath.ToSlash(paths[i]), "/")
		b := strings.Split(filepath.ToSlash(paths[j]), "/")
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// CollectResourceConflicts is conflict.py's collect_resource_conflicts.
func CollectResourceConflicts(paths []string, home string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, p := range paths {
		if !exists(p) {
			continue
		}
		var candidates []string
		if isFile(p) {
			candidates = []string{p}
		} else if isDir(p) {
			var all []string
			filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if path != p {
					all = append(all, path)
				}
				return nil
			})
			sortPathsLikePython(all)
			for _, c := range all {
				if isFile(c) && !isSymlink(c) {
					candidates = append(candidates, c)
				}
			}
		} else {
			continue
		}
		for _, f := range candidates {
			r := resolve(f)
			if seen[r] {
				continue
			}
			seen[r] = true
			for _, n := range blockingConflictLines(f) {
				errs = append(errs, fmt.Sprintf("%s:%d: Git conflict marker detected", safeRelativePath(f, home), n))
			}
		}
	}
	return errs
}
