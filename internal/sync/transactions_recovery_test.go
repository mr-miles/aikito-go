package sync

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// writeJournal writes jd as root's pending journal (mirrors Apply's
// journal-write step), for constructing mid-crash states directly.
func writeJournal(t *testing.T, root string, jd journalData) {
	t.Helper()
	data, err := json.Marshal(jd)
	if err != nil {
		t.Fatal(err)
	}
	jpath, _, err := journalPath(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicText(jpath, string(data)); err != nil {
		t.Fatal(err)
	}
}

func writeRawJournal(t *testing.T, root, content string) {
	t.Helper()
	jpath, _, err := journalPath(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicText(jpath, content); err != nil {
		t.Fatal(err)
	}
}

// validJournal returns a structurally valid pending journal for a single
// memory/foo.md create in roots[0].
func validJournal(roots []string) journalData {
	fp := digestOf("x")
	return journalData{
		Version: journalSchemaVersion,
		Roots:   append([]string(nil), roots...),
		TxID:    randomHex(16),
		Phase:   "pending",
		Changes: []journalChange{{
			Target: 0, Path: "memory/foo.md",
			Destination: filepath.Join(roots[0], "memory", "foo.md"),
			Kind:        "memory", After: &fp,
		}},
		Policy: policyToJournal(PathPolicy{}),
	}
}

// --- atomicUnlink ---

func TestAtomicUnlink(t *testing.T) {
	dir := t.TempDir()

	t.Run("removes_existing_file", func(t *testing.T) {
		p := filepath.Join(dir, "state.json")
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := atomicUnlink(p); err != nil {
			t.Fatalf("atomicUnlink: %v", err)
		}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("file should be gone, lstat err=%v", err)
		}
	})

	t.Run("missing_is_noop", func(t *testing.T) {
		if err := atomicUnlink(filepath.Join(dir, "never-existed.json")); err != nil {
			t.Fatalf("unlinking a missing file should be a no-op: %v", err)
		}
	})

	t.Run("refuses_directory", func(t *testing.T) {
		d := filepath.Join(dir, "a-dir")
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := atomicUnlink(d); err == nil {
			t.Fatal("atomicUnlink must refuse a directory")
		}
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Fatalf("refused directory must be left intact, err=%v", err)
		}
	})

	t.Run("refuses_symlink_without_touching_target", func(t *testing.T) {
		target := filepath.Join(dir, "real.json")
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := atomicUnlink(link); err == nil {
			t.Fatal("atomicUnlink must refuse a symlink")
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("refused symlink must be left in place: %v", err)
		}
		if data, _ := os.ReadFile(target); string(data) != "keep" {
			t.Fatalf("symlink target must be untouched, got %q", data)
		}
	})
}

// --- isDirNotEmptyErr ---

func TestIsDirNotEmptyErr(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rmErr := os.Remove(full)
	if rmErr == nil {
		t.Fatal("expected removing a non-empty dir to fail")
	}
	if !isDirNotEmptyErr(rmErr) {
		t.Errorf("real non-empty rmdir error should be classified as not-empty: %v", rmErr)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"enotempty", &os.PathError{Op: "remove", Path: "x", Err: syscall.ENOTEMPTY}, true},
		{"eexist", &os.PathError{Op: "remove", Path: "x", Err: syscall.EEXIST}, true},
		{"eacces", &os.PathError{Op: "remove", Path: "x", Err: syscall.EACCES}, false},
		{"enoent", &os.PathError{Op: "remove", Path: "x", Err: syscall.ENOENT}, false},
		{"not_a_patherror", errors.New("directory not empty"), false},
	}
	for _, tc := range cases {
		if got := isDirNotEmptyErr(tc.err); got != tc.want {
			t.Errorf("%s: isDirNotEmptyErr = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- resourcesEqual / stringsEqual ---

func TestResourcesAndStringsEqual(t *testing.T) {
	if !resourcesEqual(nil, nil) || !resourcesEqual(nil, [][2]string{}) {
		t.Error("nil and empty resource lists must compare equal (journal JSON round-trips nil as [] or null)")
	}
	a := [][2]string{{"inbox", "x.md"}, {"memory", "y.md"}}
	if !resourcesEqual(a, [][2]string{{"inbox", "x.md"}, {"memory", "y.md"}}) {
		t.Error("identical lists must be equal")
	}
	if resourcesEqual(a, [][2]string{{"memory", "y.md"}, {"inbox", "x.md"}}) {
		t.Error("order matters")
	}
	if resourcesEqual(a, a[:1]) {
		t.Error("length mismatch must be unequal")
	}
	if resourcesEqual(a, [][2]string{{"inbox", "x.md"}, {"memory", "z.md"}}) {
		t.Error("element mismatch must be unequal")
	}

	if !stringsEqual(nil, []string{}) {
		t.Error("nil and empty string lists must compare equal")
	}
	if !stringsEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Error("identical lists must be equal")
	}
	if stringsEqual([]string{"a", "b"}, []string{"b", "a"}) || stringsEqual([]string{"a"}, []string{"a", "b"}) {
		t.Error("order/length differences must be unequal")
	}
}

// --- PendingKinds ---

func TestPendingKindsAcrossRoots(t *testing.T) {
	r1, r2 := t.TempDir(), t.TempDir()
	roots := []string{r1, r2}
	fp := digestOf("x")
	jd := journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: randomHex(16), Phase: "pending",
		Changes: []journalChange{
			{Target: 0, Path: "memory/a.md", Destination: filepath.Join(r1, "memory", "a.md"), Kind: "memory", After: &fp},
			{Target: 1, Path: "skills/s", Destination: filepath.Join(r2, "skills", "s"), Kind: "skill", After: &fp},
		},
		Policy: policyToJournal(PathPolicy{}),
	}
	writeJournal(t, r1, jd)
	writeJournal(t, r2, jd)

	kinds, err := PendingKinds(roots)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"memory", "skill"} {
		if _, ok := kinds[k]; !ok {
			t.Errorf("expected kind %q pending, got %v", k, kinds)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("expected exactly 2 kinds, got %v", kinds)
	}

	// Only one root has a journal (crash mid fan-out): still reported.
	r3 := t.TempDir()
	jd3 := validJournal([]string{r3})
	writeJournal(t, r3, jd3)
	kinds, err = PendingKinds([]string{r3, t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kinds["memory"]; !ok {
		t.Errorf("journal on only one root should still report its kinds, got %v", kinds)
	}
}

func TestPendingKindsCorruptedJournalIsError(t *testing.T) {
	root := t.TempDir()
	writeRawJournal(t, root, "{not json")
	if _, err := PendingKinds([]string{root}); err == nil {
		t.Fatal("corrupted journal must be an error, not silently 'nothing pending'")
	}
}

func TestPendingKindsJournalIsDirectoryIsUnsafe(t *testing.T) {
	root := t.TempDir()
	jpath, _, err := journalPath(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(jpath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PendingKinds([]string{root}); err == nil {
		t.Fatal("a directory where the journal should be must be reported unsafe")
	}
}

// --- readJournal / validateJournal ---

func TestReadJournalCorruptedJSON(t *testing.T) {
	root := t.TempDir()
	writeRawJournal(t, root, `{"version": 2, "roots": [`)
	if _, err := readJournal([]string{root}, PathPolicy{}, nil); err == nil {
		t.Fatal("corrupted journal JSON must be an error")
	}
	// Recover must refuse rather than guess, and must not delete the evidence.
	if _, err := Recover([]string{root}, PathPolicy{}, nil); err == nil {
		t.Fatal("Recover must refuse a corrupted journal")
	}
	jpath, _, _ := journalPath(root, false)
	if _, err := os.Stat(jpath); err != nil {
		t.Fatalf("corrupted journal must be left in place for a human to inspect: %v", err)
	}
	// And Apply must refuse to start over the top of it.
	if err := Apply([]string{root}, nil, nil, nil, PathPolicy{}, nil); err == nil {
		t.Fatal("Apply must refuse to start while an unreadable journal exists")
	}
}

func TestReadJournalMultiRoot(t *testing.T) {
	t.Run("journal_on_one_root_only", func(t *testing.T) {
		r1, r2 := t.TempDir(), t.TempDir()
		roots := []string{r1, r2}
		jd := validJournal(roots)
		writeJournal(t, r1, jd)
		got, err := readJournal(roots, PathPolicy{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || got.TxID != jd.TxID {
			t.Fatalf("expected the single root's journal, got %+v", got)
		}
	})

	t.Run("differ_only_in_phase_normalizes_to_pending", func(t *testing.T) {
		r1, r2 := t.TempDir(), t.TempDir()
		roots := []string{r1, r2}
		jd := validJournal(roots)
		committed := jd
		committed.Phase = "committed"
		writeJournal(t, r1, committed)
		writeJournal(t, r2, jd)
		got, err := readJournal(roots, PathPolicy{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Phase != "pending" {
			t.Fatalf("phase disagreement must normalize to pending, got %q", got.Phase)
		}
	})

	t.Run("differ_in_content_is_hard_error", func(t *testing.T) {
		r1, r2 := t.TempDir(), t.TempDir()
		roots := []string{r1, r2}
		jd := validJournal(roots)
		other := validJournal(roots) // different random txid
		writeJournal(t, r1, jd)
		writeJournal(t, r2, other)
		_, err := readJournal(roots, PathPolicy{}, nil)
		if err == nil || !strings.Contains(err.Error(), "differ") {
			t.Fatalf("inconsistent multi-root journals must be a hard error, got %v", err)
		}
		if _, err := Recover(roots, PathPolicy{}, nil); err == nil {
			t.Fatal("Recover must refuse inconsistent multi-root journals")
		}
	})
}

func TestValidateJournalRejects(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	bad := "not-a-fingerprint"

	cases := []struct {
		name   string
		mutate func(*journalData)
		policy PathPolicy
	}{
		{"wrong_version", func(j *journalData) { j.Version = 1 }, PathPolicy{}},
		{"roots_count_mismatch", func(j *journalData) { j.Roots = append(j.Roots, "/elsewhere") }, PathPolicy{}},
		{"roots_value_mismatch", func(j *journalData) { j.Roots = []string{"/some/other/root"} }, PathPolicy{}},
		{"bad_txid", func(j *journalData) { j.TxID = "../../etc" }, PathPolicy{}},
		{"bad_phase", func(j *journalData) { j.Phase = "rolling-back" }, PathPolicy{}},
		{"target_out_of_range", func(j *journalData) { j.Changes[0].Target = 5 }, PathPolicy{}},
		{"unknown_kind", func(j *journalData) { j.Changes[0].Kind = "rootkit" }, PathPolicy{}},
		{"unsafe_path", func(j *journalData) {
			j.Changes[0].Path = "memory/../../escape.md"
			j.Changes[0].Destination = filepath.Join(root, "memory", "..", "..", "escape.md")
		}, PathPolicy{}},
		{"destination_mismatch", func(j *journalData) { j.Changes[0].Destination = "/tmp/elsewhere.md" }, PathPolicy{}},
		{"bad_fingerprint", func(j *journalData) { j.Changes[0].After = &bad }, PathPolicy{}},
		{"empty_change", func(j *journalData) { j.Changes[0].After = nil }, PathPolicy{}},
		{"created_parent_not_an_ancestor", func(j *journalData) {
			j.Changes[0].CreatedParents = []string{"skills"}
			j.Policy.CreateParents = true
		}, PathPolicy{}},
		{"created_parents_without_policy", func(j *journalData) { j.Changes[0].CreatedParents = []string{"memory"} }, PathPolicy{}},
		{"caller_policy_resources_differ", func(j *journalData) {}, PathPolicy{Resources: [][2]string{{"inbox", "x.md"}}}},
		{"caller_policy_states_differ", func(j *journalData) {}, PathPolicy{States: []string{".local/state/aikito/x.json"}}},
		{"state_path_not_in_policy", func(j *journalData) {
			j.States = []journalState{{Target: 0, Path: ".local/state/aikito/x.json", After: "{}"}}
		}, PathPolicy{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jd := validJournal(roots)
			tc.mutate(&jd)
			if err := validateJournal(jd, roots, tc.policy, nil); err == nil {
				t.Fatalf("validateJournal should reject %s", tc.name)
			}
		})
	}

	// Sanity: the unmutated journal is valid.
	if err := validateJournal(validJournal(roots), roots, PathPolicy{}, nil); err != nil {
		t.Fatalf("baseline journal should validate: %v", err)
	}
}

// --- Recover ---

// A StateUpdate whose live content matches neither Before nor After means
// something external touched it after the crash: Recover must refuse, and
// must not have rolled back any resource either (validation precedes all
// writes).
func TestRecoverRefusesExternallyChangedState(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	statePath := ".local/state/aikito/my-state.json"
	if err := os.MkdirAll(filepath.Join(root, ".local", "state", "aikito"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := PathPolicy{States: []string{statePath}}

	// Resource change already applied (current == After).
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	fpNew := digestOf("new")

	before := `{"v":1}`
	if err := os.WriteFile(filepath.Join(root, statePath), []byte(`{"v":"someone-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	txid := randomHex(16)
	if _, err := txDir(root, txid, true); err != nil {
		t.Fatal(err)
	}
	jd := journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{{Target: 0, Path: "memory/foo.md", Destination: target, Kind: "memory", After: &fpNew}},
		States:  []journalState{{Target: 0, Path: statePath, Before: &before, After: `{"v":2}`}},
		Policy:  policyToJournal(policy),
	}
	writeJournal(t, root, jd)

	_, err := Recover(roots, policy, nil)
	if err == nil || !strings.Contains(err.Error(), "externally changed state") {
		t.Fatalf("expected externally-changed-state refusal, got %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Errorf("no resource may be rolled back when state validation fails; got %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(root, statePath)); string(data) != `{"v":"someone-else"}` {
		t.Errorf("externally changed state must be preserved; got %q", data)
	}
	pending, _ := HasPending(roots, policy, nil)
	if !pending {
		t.Error("journal must remain pending for manual resolution after a refused recovery")
	}
}

func TestRecoverRefusesExternallyChangedResource(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("human edit after crash"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1, fp2 := digestOf("v1"), digestOf("v2")
	txid := randomHex(16)
	if _, err := txDir(root, txid, true); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{{Target: 0, Path: "memory/foo.md", Destination: target, Kind: "memory", Before: &fp1, After: &fp2}},
		Policy:  policyToJournal(PathPolicy{}),
	})
	if _, err := Recover(roots, PathPolicy{}, nil); err == nil {
		t.Fatal("Recover must refuse to clobber a resource changed outside the transaction")
	}
	if data, _ := os.ReadFile(target); string(data) != "human edit after crash" {
		t.Fatalf("human edit must survive a refused recovery, got %q", data)
	}
}

// Recover restores a StateUpdate to Before (or deletes it when Before is
// nil) when the transaction had already written After.
func TestRecoverRestoresStateUpdates(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	stateDirPath := filepath.Join(root, ".local", "state", "aikito")
	if err := os.MkdirAll(stateDirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	updated := ".local/state/aikito/updated.json"
	created := ".local/state/aikito/created.json"
	policy := PathPolicy{States: []string{updated, created}}

	before := `{"v":1}`
	if err := os.WriteFile(filepath.Join(root, updated), []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, created), []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	txid := randomHex(16)
	if _, err := txDir(root, txid, true); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		States: []journalState{
			{Target: 0, Path: updated, Before: &before, After: `{"v":2}`},
			{Target: 0, Path: created, After: `{"new":true}`},
		},
		Policy: policyToJournal(policy),
	})

	ok, err := Recover(roots, policy, nil)
	if err != nil || !ok {
		t.Fatalf("Recover: ok=%v err=%v", ok, err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, updated)); string(data) != before {
		t.Errorf("updated state should be restored to Before, got %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, created)); !os.IsNotExist(err) {
		t.Errorf("state created by the transaction should be removed, stat err=%v", err)
	}
}

// End-to-end through Apply: a verify failure triggers Recover, which must
// remove the empty parent directories the transaction itself created.
func TestRecoverRemovesCreatedParents(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	policy := PathPolicy{CreateParents: true}
	fp := digestOf("note")
	src := writeSourceFile(t, "note")

	err := Apply(roots, []Change{{Target: 0, Path: "memory/notes/x.md", Kind: "memory", Source: src, After: &fp}},
		nil, func() error { return coreErrorf("simulated verify failure") }, policy, nil)
	if err == nil {
		t.Fatal("expected the verify failure to surface")
	}
	for _, rel := range []string{"memory/notes/x.md", "memory/notes", "memory"} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s should have been rolled back/cleaned up, lstat err=%v", rel, err)
		}
	}
	if pending, _ := HasPending(roots, policy, nil); pending {
		t.Error("no transaction should remain pending")
	}
}

// If unrelated content lands in a transaction-created parent directory
// before recovery runs, recovery must leave that directory (and the
// unrelated content) alone — never force-delete a non-empty parent.
func TestRecoverLeavesCreatedParentWithUnrelatedContent(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	policy := PathPolicy{CreateParents: true}
	fp := digestOf("note")
	src := writeSourceFile(t, "note")
	unrelated := filepath.Join(root, "memory", "notes", "someone-elses.md")

	err := Apply(roots, []Change{{Target: 0, Path: "memory/notes/x.md", Kind: "memory", Source: src, After: &fp}},
		nil, func() error {
			if err := os.WriteFile(unrelated, []byte("not ours"), 0o644); err != nil {
				t.Fatal(err)
			}
			return coreErrorf("simulated verify failure")
		}, policy, nil)
	if err == nil {
		t.Fatal("expected the verify failure to surface")
	}
	if _, err := os.Lstat(filepath.Join(root, "memory", "notes", "x.md")); !os.IsNotExist(err) {
		t.Errorf("the transaction's own file must be rolled back, lstat err=%v", err)
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "not ours" {
		t.Fatalf("unrelated content in a created parent must survive recovery: data=%q err=%v", data, err)
	}
	if pending, _ := HasPending(roots, policy, nil); pending {
		t.Error("recovery should still complete (non-empty parent is not an error)")
	}
}

// A journal already marked committed (crash after the commit marker, before
// cleanup) must just be cleaned up — never rolled back.
func TestRecoverCommittedJournalOnlyCleansUp(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1, fp2 := digestOf("v1"), digestOf("v2")
	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}
	// A backup copy is still lying around; it must NOT be used to roll back.
	backup := filepath.Join(tx, "backup", "memory", "foo.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "committed",
		Changes: []journalChange{{Target: 0, Path: "memory/foo.md", Destination: target, Kind: "memory", Before: &fp1, After: &fp2}},
		Policy:  policyToJournal(PathPolicy{}),
	})

	ok, err := Recover(roots, PathPolicy{}, nil)
	if err != nil || !ok {
		t.Fatalf("Recover: ok=%v err=%v", ok, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "v2" {
		t.Fatalf("a committed transaction must not be rolled back, got %q", data)
	}
	if _, err := os.Stat(tx); !os.IsNotExist(err) {
		t.Errorf("tx staging dir should be cleaned up, stat err=%v", err)
	}
	if pending, _ := HasPending(roots, PathPolicy{}, nil); pending {
		t.Error("journal should be removed")
	}
}

// Crash *during* recovery: the resource was already restored to Before but
// the journal was never re-marked committed. Re-running Recover must
// finish cleanly (skip the already-restored resource) — then a third call
// is a no-op.
func TestRecoverIsIdempotentAfterPartialRecovery(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(root, "memory", "a.md")
	b := filepath.Join(root, "memory", "b.md")
	// a: already restored by the interrupted recovery. b: not yet.
	if err := os.WriteFile(a, []byte("a1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b2"), 0o644); err != nil {
		t.Fatal(err)
	}
	fa1, fa2 := digestOf("a1"), digestOf("a2")
	fb1, fb2 := digestOf("b1"), digestOf("b2")
	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}
	backupB := filepath.Join(tx, "backup", "memory", "b.md")
	if err := os.MkdirAll(filepath.Dir(backupB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupB, []byte("b1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{
			{Target: 0, Path: "memory/a.md", Destination: a, Kind: "memory", Before: &fa1, After: &fa2},
			{Target: 0, Path: "memory/b.md", Destination: b, Kind: "memory", Before: &fb1, After: &fb2},
		},
		Policy: policyToJournal(PathPolicy{}),
	})

	ok, err := Recover(roots, PathPolicy{}, nil)
	if err != nil || !ok {
		t.Fatalf("first Recover: ok=%v err=%v", ok, err)
	}
	if data, _ := os.ReadFile(a); string(data) != "a1" {
		t.Errorf("a must stay restored, got %q", data)
	}
	if data, _ := os.ReadFile(b); string(data) != "b1" {
		t.Errorf("b must be restored from backup, got %q", data)
	}
	ok, err = Recover(roots, PathPolicy{}, nil)
	if err != nil || ok {
		t.Fatalf("second Recover should be a no-op: ok=%v err=%v", ok, err)
	}
}

// Recover restores from the backup/ copy (not moved/) when the target was
// fully replaced by After content.
func TestRecoverFromBackupAfterFullApply(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1, fp2 := digestOf("v1"), digestOf("v2")
	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(tx, "backup", "memory", "foo.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{{Target: 0, Path: "memory/foo.md", Destination: target, Kind: "memory", Before: &fp1, After: &fp2}},
		Policy:  policyToJournal(PathPolicy{}),
	})
	ok, err := Recover(roots, PathPolicy{}, nil)
	if err != nil || !ok {
		t.Fatalf("Recover: ok=%v err=%v", ok, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "v1" {
		t.Fatalf("expected v1 restored from backup/, got %q", data)
	}
}

// Recover refuses when the only recovery copy doesn't actually hold the
// Before content (corrupted/tampered backup) rather than restoring junk.
func TestRecoverRefusesCorruptBackup(t *testing.T) {
	root := t.TempDir()
	roots := []string{root}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "memory", "foo.md")
	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp1, fp2 := digestOf("v1"), digestOf("v2")
	txid := randomHex(16)
	tx, err := txDir(root, txid, true)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(tx, "backup", "memory", "foo.md")
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJournal(t, root, journalData{
		Version: journalSchemaVersion, Roots: roots, TxID: txid, Phase: "pending",
		Changes: []journalChange{{Target: 0, Path: "memory/foo.md", Destination: target, Kind: "memory", Before: &fp1, After: &fp2}},
		Policy:  policyToJournal(PathPolicy{}),
	})
	if _, err := Recover(roots, PathPolicy{}, nil); err == nil {
		t.Fatal("Recover must refuse a backup whose fingerprint doesn't match Before")
	}
	if data, _ := os.ReadFile(target); string(data) != "v2" {
		t.Fatalf("target must be untouched by a refused recovery, got %q", data)
	}
}
