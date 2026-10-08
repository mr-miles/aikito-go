package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// pyTruthy is Python truthiness for decoded frontmatter/TOML values.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// isDir is Path.is_dir() (follows symlinks).
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// atomicWriteText ports compat.py _atomic_write_text: write a sibling temp
// file and rename it over target, keeping an existing file's mode (new files
// get 0666 minus umask).
func atomicWriteText(target, content string) error {
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	// Created 0666 so the process umask applies, as for Python's new files.
	var f *os.File
	var tmp string
	for i := 0; ; i++ {
		tmp = filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d.%d", filepath.Base(target), os.Getpid(), i))
		var err error
		f, err = os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if err == nil {
			break
		}
		if !os.IsExist(err) || i > 100 {
			return err
		}
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if st, err := os.Stat(target); err == nil {
		if err := os.Chmod(tmp, st.Mode().Perm()); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// skillImportTransaction ports add.py's _SkillImportTransaction: stage a
// complete copy of the source next to the target, fix its frontmatter, then
// swap it into place, keeping the old directory for rollback.
type skillImportTransaction struct {
	sourcePath         string
	sourceIsDir        bool
	targetDir          string
	name               string
	description        *string
	defaultDescription string

	hadTarget         bool
	tempRoot          string
	stagedDir, backup string
	applied           bool
}

var skillImportIgnored = func(name string) bool {
	return name == ".git" || name == "__pycache__" || name == ".DS_Store" || strings.HasSuffix(name, ".pyc")
}

func (t *skillImportTransaction) prepare() error {
	_, err := os.Stat(t.targetDir)
	t.hadTarget = err == nil
	parent := filepath.Dir(t.targetDir)
	if err := os.MkdirAll(parent, 0o777); err != nil {
		return err
	}
	root, err := os.MkdirTemp(parent, "."+t.name+"-import-")
	if err != nil {
		return err
	}
	t.tempRoot = root
	t.stagedDir = filepath.Join(root, "staged")
	t.backup = filepath.Join(root, "original")

	if err := t.stage(); err != nil {
		t.discard()
		return err
	}
	return nil
}

func (t *skillImportTransaction) stage() error {
	if t.sourceIsDir {
		if err := copyTree2(t.sourcePath, t.stagedDir, t.tempRoot); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(t.stagedDir, 0o777); err != nil {
			return err
		}
		if err := copyFile2(t.sourcePath, filepath.Join(t.stagedDir, "SKILL.md")); err != nil {
			return err
		}
	}
	skillFile := filepath.Join(t.stagedDir, "SKILL.md")
	raw, err := os.ReadFile(skillFile)
	if err != nil {
		return err
	}
	text := string(raw)
	meta, _ := workspace.ParseMarkdownFrontmatter(text, nil)
	var updates []workspace.FrontmatterUpdate
	if v, ok := meta["name"].(string); !ok || v != t.name {
		updates = append(updates, workspace.FrontmatterUpdate{Key: "name", Value: t.name})
	}
	if t.description != nil && strings.TrimSpace(*t.description) != "" {
		d := strings.TrimSpace(*t.description)
		if v, ok := meta["description"].(string); !ok || v != d {
			updates = append(updates, workspace.FrontmatterUpdate{Key: "description", Value: d})
		}
	} else if _, ok := meta["description"]; !ok {
		updates = append(updates, workspace.FrontmatterUpdate{Key: "description", Value: t.defaultDescription})
	}
	if len(updates) > 0 {
		return atomicWriteText(skillFile, workspace.UpdateMarkdownFrontmatter(text, updates))
	}
	return nil
}

func (t *skillImportTransaction) apply() error {
	if t.hadTarget {
		if err := os.Rename(t.targetDir, t.backup); err != nil {
			return err
		}
	}
	if err := os.Rename(t.stagedDir, t.targetDir); err != nil {
		if t.hadTarget {
			_ = os.Rename(t.backup, t.targetDir)
		}
		return err
	}
	t.applied = true
	return nil
}

func (t *skillImportTransaction) rollback() {
	if t.applied {
		_ = os.RemoveAll(t.targetDir)
		if t.hadTarget {
			if _, err := os.Stat(t.backup); err == nil {
				_ = os.Rename(t.backup, t.targetDir)
			}
		}
		t.applied = false
	}
	t.discard()
}

func (t *skillImportTransaction) discard() {
	if t.tempRoot != "" {
		_ = os.RemoveAll(t.tempRoot)
	}
}

// copyTree2 is shutil.copytree(src, dst) with copy2 and the import ignore
// patterns: symlinks are followed, file and directory modes and mtimes are
// kept. skip (the transaction's temp dir) is never copied into itself.
func copyTree2(src, dst, skip string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if skillImportIgnored(name) || filepath.Join(src, name) == skip {
			continue
		}
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		info, err := os.Stat(s)
		if err != nil {
			return fmt.Errorf("[Errno 2] No such file or directory: '%s'", s)
		}
		if info.IsDir() {
			if err := copyTree2(s, d, skip); err != nil {
				return err
			}
			continue
		}
		if err := copyFile2(s, d); err != nil {
			return err
		}
	}
	if err := os.Chmod(dst, st.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dst, st.ModTime(), st.ModTime())
}
