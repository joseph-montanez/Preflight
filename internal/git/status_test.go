package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParsePorcelainZ(t *testing.T) {
	// Sample data with:
	// - modified: " M file1.txt\0"
	// - untracked: "?? file2.txt\0"
	// - deleted: " D file3.txt\0"
	// - rename: "RM new_name.txt\0old_name.txt\0"
	// - added: "A  file4.txt\0"
	raw := []byte(" M file1.txt\x00?? file2.txt\x00 D file3.txt\x00RM new_name.txt\x00old_name.txt\x00A  file4.txt\x00")

	changes := parsePorcelainZ(raw, "")
	if len(changes) != 5 {
		t.Fatalf("expected 5 changes, got %d", len(changes))
	}

	expected := []FileChange{
		{Path: "file1.txt", Status: " M"},
		{Path: "file2.txt", Status: "??"},
		{Path: "file3.txt", Status: " D"},
		{Path: "new_name.txt", OrigPath: "old_name.txt", Status: "RM"},
		{Path: "file4.txt", Status: "A "},
	}

	for i, exp := range expected {
		if changes[i] != exp {
			t.Errorf("change[%d] = %+v, want %+v", i, changes[i], exp)
		}
	}
}

func TestParsePorcelainZWithPrefix(t *testing.T) {
	// Simulated output when dir is a subfolder: prefix = "sub/"
	raw := []byte(" M sub/file1.txt\x00?? sub/nested/file2.txt\x00RM sub/renamed.txt\x00sub/original.txt\x00")

	changes := parsePorcelainZ(raw, "sub/")
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d", len(changes))
	}

	expected := []FileChange{
		{Path: "file1.txt", Status: " M"},
		{Path: "nested/file2.txt", Status: "??"},
		{Path: "renamed.txt", OrigPath: "original.txt", Status: "RM"},
	}

	for i, exp := range expected {
		if changes[i] != exp {
			t.Errorf("change[%d] = %+v, want %+v", i, changes[i], exp)
		}
	}
}

func TestGitStatusRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()

	// Initialize git repo
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, string(out))
		}
	}

	run("init")
	run("config", "user.name", "Test")
	run("config", "user.email", "test@example.com")

	if !IsGitRepo(dir) {
		t.Fatal("expected IsGitRepo to return true")
	}

	// Clean repo: no changes
	paths, err := ChangedPaths(dir, nil)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("expected 0 paths, got %v", paths)
	}

	// Create an initial commit
	if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "to_delete.txt"), []byte("del"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "committed.txt", "to_delete.txt")
	run("commit", "-m", "init")

	// Now introduce changes:
	// 1. Modify committed.txt
	if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 2. Delete to_delete.txt
	if err := os.Remove(filepath.Join(dir, "to_delete.txt")); err != nil {
		t.Fatal(err)
	}
	// 3. Create untracked new.txt
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 4. Create excluded file
	if err := os.WriteFile(filepath.Join(dir, "ignored.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err = ChangedPaths(dir, []string{"*.log"})
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}

	want := []string{"committed.txt", "new.txt", "to_delete.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestGitStatusNotRepo(t *testing.T) {
	dir := t.TempDir()

	if IsGitRepo(dir) {
		t.Fatal("expected IsGitRepo to return false for empty tempdir")
	}

	_, err := Status(dir)
	if err == nil || err != ErrNotGitRepo {
		t.Fatalf("expected ErrNotGitRepo, got %v", err)
	}
}
