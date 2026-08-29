package syncer

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"godeploy/internal/domain"
)

type walkInfo struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (f walkInfo) Name() string       { return f.name }
func (f walkInfo) Size() int64        { return 0 }
func (f walkInfo) Mode() fs.FileMode  { return f.mode }
func (f walkInfo) ModTime() time.Time { return time.Time{} }
func (f walkInfo) IsDir() bool        { return f.dir }
func (f walkInfo) Sys() any           { return nil }

// treeClient serves a static remote tree: dirs maps a directory to its
// entry names (dirs ending in "/"), files maps absolute paths to content.
type treeClient struct {
	dirs  map[string][]string
	files map[string][]byte
}

func newTreeClient() *treeClient {
	return &treeClient{
		dirs: map[string][]string{
			"/":                 {"srv/", "etc/"},
			"/srv":              {"app/"},
			"/srv/app":          {"index.php", "assets/", "secret.env"},
			"/srv/app/assets":   {"logo.png"},
			"/etc":              {"nginx/"},
			"/etc/nginx":        {"conf.d/"},
			"/etc/nginx/conf.d": {"default.conf"},
		},
		files: map[string][]byte{
			"/srv/app/index.php":             []byte("hi"),
			"/srv/app/assets/logo.png":       []byte("png"),
			"/srv/app/secret.env":            []byte("SECRET"),
			"/etc/nginx/conf.d/default.conf": []byte("conf"),
		},
	}
}

func (c *treeClient) Connect() error { return nil }
func (c *treeClient) Close() error   { return nil }
func (c *treeClient) Stat(p string) (*domain.FileMeta, error) {
	if _, ok := c.files[p]; ok {
		return &domain.FileMeta{Exists: true}, nil
	}
	return &domain.FileMeta{}, os.ErrNotExist
}
func (c *treeClient) ReadDir(p string) ([]fs.FileInfo, error) {
	names, ok := c.dirs[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]fs.FileInfo, 0, len(names))
	for _, n := range names {
		switch {
		case strings.HasSuffix(n, "/"):
			out = append(out, walkInfo{name: strings.TrimSuffix(n, "/"), dir: true, mode: fs.ModeDir | 0o755})
		case strings.HasSuffix(n, "@"):
			// trailing @ marks a symlink entry for walk tests
			out = append(out, walkInfo{name: strings.TrimSuffix(n, "@"), mode: fs.ModeSymlink})
		default:
			out = append(out, walkInfo{name: n, mode: 0o644})
		}
	}
	return out, nil
}
func (c *treeClient) OpenReader(string) (io.ReadCloser, error)  { return nil, errors.New("nope") }
func (c *treeClient) OpenWriter(string) (io.WriteCloser, error) { return nil, errors.New("nope") }
func (c *treeClient) Remove(string) error                       { return nil }
func (c *treeClient) MkdirAll(string) error                     { return nil }
func (c *treeClient) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

var _ domain.RemoteClient = (*treeClient)(nil)

func TestWalkRemoteCollectsAllFiles(t *testing.T) {
	client := newTreeClient()
	var got []string
	err := WalkRemote(client, "/srv/app", func(p string, _ *domain.FileMeta) {
		got = append(got, p)
	})
	if err != nil {
		t.Fatalf("WalkRemote: %v", err)
	}
	want := map[string]bool{
		"/srv/app/index.php":       true,
		"/srv/app/assets/logo.png": true,
		"/srv/app/secret.env":      true,
	}
	if len(got) != len(want) {
		t.Fatalf("WalkRemote returned %v, want %d files", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected file %q (all: %v)", p, got)
		}
	}
}

func TestWalkRemoteSkipsSymlinks(t *testing.T) {
	client := newTreeClient()
	client.dirs["/srv/app"] = append(client.dirs["/srv/app"], "link@")
	var got []string
	err := WalkRemote(client, "/srv/app", func(p string, _ *domain.FileMeta) {
		got = append(got, p)
	})
	if err != nil {
		t.Fatalf("WalkRemote: %v", err)
	}
	for _, p := range got {
		if strings.HasSuffix(p, "link") {
			t.Fatalf("symlink leaked into walk: %q (all: %v)", p, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("WalkRemote = %v, want the 3 regular files", got)
	}
}

func TestWalkRemoteErrors(t *testing.T) {
	client := newTreeClient()
	err := WalkRemote(client, "/missing", func(string, *domain.FileMeta) {})
	if err == nil || !strings.Contains(err.Error(), "walk remote") {
		t.Fatalf("expected a walk error, got %v", err)
	}
}

func TestWalkRemoteRootItself(t *testing.T) {
	client := newTreeClient()
	var got []string
	if err := WalkRemote(client, "/", func(p string, _ *domain.FileMeta) { got = append(got, p) }); err != nil {
		t.Fatalf("WalkRemote(/): %v", err)
	}
	// /srv/app/* plus /etc/nginx/conf.d/default.conf
	if len(got) != 4 {
		t.Fatalf("WalkRemote(/) = %v, want 4 files across the tree", got)
	}
}

func TestWalkLocalRecursive(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "a/b", "a/b/c"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"top.txt", "a/x.txt", "a/b/y.txt", "a/b/c/z.txt", "a/b/c/deep.txt"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := WalkLocal(root, nil, func(p string, _ *domain.FileMeta) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("WalkLocal = %d files, want 5: %v", len(got), got)
	}
	for _, f := range got {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("reported path %q not on disk: %v", f, err)
		}
	}
}

func TestWalkLocalSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "dirlink")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var got []string
	if err := WalkLocal(root, nil, func(p string, _ *domain.FileMeta) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("WalkLocal = %v, want only real.txt", got)
	}
}

func TestWalkLocalIgnoresCallback(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"keep.txt", "dist/skip.js", "dist/sub/deep.tmp"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Prune any directory named "dist" and skip *.tmp files.
	var sawPrune bool
	ignored := func(abs, walkRel string, isDir bool) bool {
		if isDir && filepath.Base(abs) == "dist" {
			sawPrune = true
			return true
		}
		return !isDir && strings.HasSuffix(walkRel, ".tmp")
	}
	var got []string
	if err := WalkLocal(root, ignored, func(p string, _ *domain.FileMeta) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if !sawPrune {
		t.Fatal("the dist dir should have been offered to the callback for pruning")
	}
	if len(got) != 1 || got[0] != filepath.Join(root, "keep.txt") {
		t.Fatalf("WalkLocal = %v, want only keep.txt", got)
	}
}

func TestWalkLocalEmptyDirsNeverEmitted(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := WalkLocal(root, nil, func(p string, _ *domain.FileMeta) { got = append(got, p) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("WalkLocal = %v, want no files (empty dir only)", got)
	}
}
