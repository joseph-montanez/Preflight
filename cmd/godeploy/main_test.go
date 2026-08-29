package main

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"godeploy/internal/config"
	"godeploy/internal/domain"
	ignoreengine "godeploy/internal/ignore"
	"godeploy/internal/localbrowser"
	"godeploy/internal/remotebrowser"
)

// fakeDownloadClient serves a static tree for buildDownloadItems tests.
type fakeDownloadClient struct {
	dirs  map[string][]string
	files map[string][]byte
}

func newFakeDownloadClient() *fakeDownloadClient {
	return &fakeDownloadClient{
		dirs: map[string][]string{
			"/var/www/api-backend":        {"assets/", "index.php", ".env"},
			"/var/www/api-backend/assets": {"logo.png"},
			"/etc/nginx/conf.d":           {"default.conf"},
		},
		files: map[string][]byte{
			"/var/www/api-backend/index.php":       []byte("hi"),
			"/var/www/api-backend/.env":            []byte("SECRET"),
			"/var/www/api-backend/assets/logo.png": []byte("png"),
			"/etc/nginx/conf.d/default.conf":       []byte("conf"),
		},
	}
}

type dwInfo struct {
	name string
	dir  bool
}

func (f dwInfo) Name() string       { return f.name }
func (f dwInfo) Size() int64        { return 0 }
func (f dwInfo) Mode() fs.FileMode  { return 0o644 }
func (f dwInfo) ModTime() time.Time { return time.Time{} }
func (f dwInfo) IsDir() bool        { return f.dir }
func (f dwInfo) Sys() any           { return nil }

func (c *fakeDownloadClient) Connect() error { return nil }
func (c *fakeDownloadClient) Close() error   { return nil }
func (c *fakeDownloadClient) Stat(p string) (*domain.FileMeta, error) {
	if _, ok := c.files[p]; ok {
		return &domain.FileMeta{Size: 1, Exists: true}, nil
	}
	if _, ok := c.dirs[p]; ok {
		return &domain.FileMeta{Mode: fs.ModeDir, Exists: true}, nil
	}
	return nil, os.ErrNotExist
}
func (c *fakeDownloadClient) ReadDir(p string) ([]fs.FileInfo, error) {
	names, ok := c.dirs[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]fs.FileInfo, 0, len(names))
	for _, n := range names {
		out = append(out, dwInfo{name: strings.TrimSuffix(n, "/"), dir: strings.HasSuffix(n, "/")})
	}
	return out, nil
}
func (c *fakeDownloadClient) OpenReader(string) (io.ReadCloser, error) { return nil, nil }
func (c *fakeDownloadClient) OpenWriter(string) (io.WriteCloser, error) {
	return nil, nil
}
func (c *fakeDownloadClient) Remove(string) error   { return nil }
func (c *fakeDownloadClient) MkdirAll(string) error { return nil }
func (c *fakeDownloadClient) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

var _ domain.RemoteClient = (*fakeDownloadClient)(nil)

func TestBuildDownloadItemsMappedDir(t *testing.T) {
	client := newFakeDownloadClient()
	roots := []remotebrowser.DownloadRoot{
		{RemotePath: "/var/www/api-backend/assets", LocalPath: filepath.FromSlash("/home/dev/code/public/assets")},
	}
	items, err := buildDownloadItems(client, roots, "", nil)
	if err != nil {
		t.Fatalf("buildDownloadItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 file under the marked dir", len(items))
	}
	it := items[0]
	if it.RemotePath != "/var/www/api-backend/assets/logo.png" {
		t.Fatalf("RemotePath = %q", it.RemotePath)
	}
	if it.LocalPath != filepath.FromSlash("/home/dev/code/public/assets/logo.png") {
		t.Fatalf("LocalPath = %q", it.LocalPath)
	}
	if it.Action != domain.ActionDownload {
		t.Fatalf("Action = %v, want download", it.Action)
	}
}

func TestBuildDownloadItemsUnmappedFileWithDestBase(t *testing.T) {
	client := newFakeDownloadClient()
	roots := []remotebrowser.DownloadRoot{
		{RemotePath: "/etc/nginx/conf.d/default.conf"}, // LocalPath empty → unmapped
	}
	items, err := buildDownloadItems(client, roots, filepath.FromSlash("/tmp/dl"), nil)
	if err != nil {
		t.Fatalf("buildDownloadItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	want := filepath.FromSlash("/tmp/dl/etc/nginx/conf.d/default.conf")
	if items[0].LocalPath != want {
		t.Fatalf("LocalPath = %q, want %q", items[0].LocalPath, want)
	}
}

func TestBuildDownloadItemsIgnoresNothing(t *testing.T) {
	client := newFakeDownloadClient()
	roots := []remotebrowser.DownloadRoot{
		{RemotePath: "/var/www/api-backend", LocalPath: filepath.FromSlash("/home/dev/code/public")},
	}
	items, err := buildDownloadItems(client, roots, "", nil)
	if err != nil {
		t.Fatalf("buildDownloadItems: %v", err)
	}
	paths := map[string]bool{}
	for _, it := range items {
		paths[it.RelativePath] = true
	}
	// .env is exactly the kind of file a sync would ignore — it must be here.
	if !paths["/var/www/api-backend/.env"] {
		t.Fatalf("ignored file .env missing from download items: %v", paths)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 files (index.php, .env, assets/logo.png)", len(items))
	}
}

func TestBuildDownloadItemsProgress(t *testing.T) {
	client := newFakeDownloadClient()
	roots := []remotebrowser.DownloadRoot{
		{RemotePath: "/var/www/api-backend/index.php", LocalPath: filepath.FromSlash("/home/dev/code/public/index.php")},
		{RemotePath: "/var/www/api-backend/assets", LocalPath: filepath.FromSlash("/home/dev/code/public/assets")},
	}
	var stages []string
	_, err := buildDownloadItems(client, roots, "", func(stage string, done, total int) {
		stages = append(stages, stage)
	})
	if err != nil {
		t.Fatalf("buildDownloadItems: %v", err)
	}
	if len(stages) != 2 || stages[0] != "walking remote files" {
		t.Fatalf("progress = %v, want 2 walking-remote-files stages", stages)
	}
}

// uploadFixture builds a temp project for buildUploadItems tests: a
// mapped folder public (→ /var/www/public) with a gitignored file, an
// excluded file, and an excluded folder; plus an unmapped root-level
// file. Returns the project root and the exclusions-only engine.
func uploadFixture(t *testing.T) (string, *ignoreengine.Engine, []config.PathMapping) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"public/dist", "public/sub"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"public/index.php", "public/secret.env", "public/dist/bundle.js", "public/sub/other.txt", "README.md"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mappings := []config.PathMapping{{LocalPath: "public", RemotePath: "/var/www/public"}}
	return root, ignoreengine.NewExclusionsOnly([]string{"secret.env", "dist/"}), mappings
}

func TestBuildUploadItemsMappedDirFilterOff(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	roots := []localbrowser.UploadRoot{{LocalPath: filepath.Join(root, "public"), RemotePath: "/var/www/public"}}
	items, err := buildUploadItems(root, mappings, roots, "", false, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	// Filter OFF: everything uploads, including the excluded file and folder.
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4 (everything uploads): %+v", len(items), items)
	}
	byRel := map[string]string{}
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.LocalPath)
		byRel[filepath.ToSlash(rel)] = it.RemotePath
	}
	want := map[string]string{
		"public/index.php":      "/var/www/public/index.php",
		"public/secret.env":     "/var/www/public/secret.env",
		"public/dist/bundle.js": "/var/www/public/dist/bundle.js",
		"public/sub/other.txt":  "/var/www/public/sub/other.txt",
	}
	for rel, remote := range want {
		if byRel[rel] != remote {
			t.Fatalf("item %s → %q, want %q (all: %v)", rel, byRel[rel], remote, byRel)
		}
	}
	for _, it := range items {
		if it.Action != domain.ActionUpload {
			t.Fatalf("action = %v, want upload", it.Action)
		}
		if it.DiffDetails == nil || !strings.HasPrefix(it.DiffDetails.Summary, "→ ") {
			t.Fatalf("diff summary = %v, want → remote path", it.DiffDetails)
		}
	}
}

func TestBuildUploadItemsMappedDirFilterOn(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	roots := []localbrowser.UploadRoot{{LocalPath: filepath.Join(root, "public"), RemotePath: "/var/www/public"}}
	items, err := buildUploadItems(root, mappings, roots, "", true, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2 (excluded file + folder skipped): %+v", len(items), items)
	}
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.LocalPath)
		rel = filepath.ToSlash(rel)
		if rel == "public/secret.env" || strings.HasPrefix(rel, "public/dist") {
			t.Fatalf("excluded path leaked into items: %s", rel)
		}
	}
}

func TestBuildUploadItemsRootMatchExcludesOutright(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	// Picking public/dist itself: the root matches the exclusion dist/,
	// so the whole folder is excluded even with the filter OFF.
	roots := []localbrowser.UploadRoot{{LocalPath: filepath.Join(root, "public/dist"), RemotePath: "/var/www/public/dist"}}
	items, err := buildUploadItems(root, mappings, roots, "", false, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("root-match must exclude everything, got %d items", len(items))
	}
	// The same selection with filter ON behaves identically.
	items, err = buildUploadItems(root, mappings, roots, "", true, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("root-match must exclude everything regardless of toggle, got %d items", len(items))
	}
}

func TestBuildUploadItemsUnmappedFileWithDestBase(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	roots := []localbrowser.UploadRoot{{LocalPath: filepath.Join(root, "README.md")}}
	items, err := buildUploadItems(root, mappings, roots, "/var/www/misc", false, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].RemotePath != "/var/www/misc/README.md" {
		t.Fatalf("remote = %q, want /var/www/misc/README.md", items[0].RemotePath)
	}
}

func TestBuildUploadItemsUnmappedDirPreservesProjectRel(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	// An unmapped folder inside the project: its project-relative path is
	// preserved under the picked base.
	sub := filepath.Join(root, "public/sub")
	roots := []localbrowser.UploadRoot{{LocalPath: sub}}
	items, err := buildUploadItems(root, mappings, roots, "/var/www/misc", false, ignores, nil)
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %+v", len(items), items)
	}
	if items[0].RemotePath != "/var/www/misc/public/sub/other.txt" {
		t.Fatalf("remote = %q, want /var/www/misc/public/sub/other.txt", items[0].RemotePath)
	}
}

func TestBuildUploadItemsProgress(t *testing.T) {
	root, ignores, mappings := uploadFixture(t)
	roots := []localbrowser.UploadRoot{
		{LocalPath: filepath.Join(root, "public"), RemotePath: "/var/www/public"},
		{LocalPath: filepath.Join(root, "README.md")},
	}
	var stages []string
	done := 0
	items, err := buildUploadItems(root, mappings, roots, "/var/www/misc", false, ignores, func(stage string, d, total int) {
		stages = append(stages, stage)
		done = d
	})
	if err != nil {
		t.Fatalf("buildUploadItems: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	if done != 2 {
		t.Fatalf("progress done = %d, want 2 roots", done)
	}
	if len(stages) == 0 || stages[0] != "walking local files" {
		t.Fatalf("stages = %v", stages)
	}
}
