package sync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mr-miles/aikito-go/internal/subagent"
)

// subagentBackupDir is subagent.py's BACKUP_DIR, under home.
const subagentBackupDir = ".local/state/aikito/backups"

// backupSubagentFile ports _backup_file: copy an existing target (mode and
// mtime preserved, as shutil.copy2 does) to
// ~/.local/state/aikito/backups/<agent>/<UTC timestamp>-<name>.
func backupSubagentFile(home, agent, target string, now time.Time) error {
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	now = now.UTC()
	stamp := fmt.Sprintf("%sT%s%06dZ", now.Format("20060102"), now.Format("150405"), now.Nanosecond()/1000)
	backup := filepath.Join(home, subagentBackupDir, agent, stamp+"-"+filepath.Base(target))
	if err := os.MkdirAll(filepath.Dir(backup), 0o777); err != nil {
		return err
	}
	src, err := os.Open(target)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(backup, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if err := os.Chmod(backup, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(backup, info.ModTime(), info.ModTime())
}

// writeSubagentFileAtomic ports _write_file_atomic.
func writeSubagentFileAtomic(target, content string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", target, os.Getpid())
	if err := os.WriteFile(tmp, []byte(content), 0o666); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// ApplySubagentPlan ports execute_subagent_plan's write phase for an
// applicable plan: mutations are grouped per physical file; a shared-patch
// file is written once with every change applied, backed up first if its
// content changes; a per-file target is backed up before UPDATE and
// REMOVE. It returns execute_subagent_plan's error message on failure.
func ApplySubagentPlan(ops []SubagentOperation, home string) (string, error) {
	var order []string
	byPath := map[string][]SubagentOperation{}
	for _, op := range ops {
		if op.TargetPath == "" {
			continue
		}
		if _, seen := byPath[op.TargetPath]; !seen {
			order = append(order, op.TargetPath)
		}
		byPath[op.TargetPath] = append(byPath[op.TargetPath], op)
	}
	failed := func(path string, err error) (string, error) {
		return fmt.Sprintf("Failed writing configuration to '%s': %v", path, err), err
	}
	for _, path := range order {
		fileOps := byPath[path]
		mutates := false
		for _, op := range fileOps {
			if op.IsAuthorized && (op.Action == SACreate || op.Action == SAUpdate || op.Action == SARemove) {
				mutates = true
			}
		}
		if !mutates {
			continue
		}
		if fileOps[0].Layout == subagent.LayoutSharedPatch {
			current, readErr := os.ReadFile(path)
			text := string(current)
			final := text
			for _, op := range fileOps {
				if !op.IsAuthorized {
					continue
				}
				switch op.Action {
				case SACreate, SAUpdate:
					final = subagent.UpdateDSHCordisSubagent(final, op.Subagent, op.RenderedPayload)
				case SARemove:
					final = subagent.RemoveDSHCordisSubagent(final, op.Subagent)
				}
			}
			if readErr == nil {
				if text == final {
					continue
				}
				if err := backupSubagentFile(home, fileOps[0].Agent, path, time.Now()); err != nil {
					return failed(path, err)
				}
			}
			if err := writeSubagentFileAtomic(path, final); err != nil {
				return failed(path, err)
			}
			continue
		}
		for _, op := range fileOps {
			if !op.IsAuthorized {
				continue
			}
			var err error
			switch op.Action {
			case SACreate:
				err = writeSubagentFileAtomic(path, op.RenderedPayload)
			case SAUpdate:
				if err = backupSubagentFile(home, op.Agent, path, time.Now()); err == nil {
					err = writeSubagentFileAtomic(path, op.RenderedPayload)
				}
			case SARemove:
				if err = backupSubagentFile(home, op.Agent, path, time.Now()); err == nil {
					if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
						err = os.Remove(path)
					}
				}
			}
			if err != nil {
				return failed(path, err)
			}
		}
	}
	return "", nil
}
