package syncer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"godeploy/internal/config"
	diffengine "godeploy/internal/diff"
	"godeploy/internal/domain"
)

// memClient is an in-memory RemoteClient that can simulate servers which
// refuse to rename over an existing file.
type memClient struct {
	files      map[string][]byte
	dirs       []string
	renames    int
	failRename bool
	times      int
}

func newMemClient() *memClient {
	return &memClient{files: map[string][]byte{}}
}

func (c *memClient) Connect() error { return nil }
func (c *memClient) Close() error   { return nil }
func (c *memClient) Stat(path string) (*domain.FileMeta, error) {
	data, ok := c.files[path]
	if !ok {
		// Any path that is a parent of a stored file is a directory.
		for name := range c.files {
			if strings.HasPrefix(name, path+"/") {
				return &domain.FileMeta{Mode: fs.ModeDir, Exists: true}, nil
			}
		}
		return &domain.FileMeta{}, os.ErrNotExist
	}
	return &domain.FileMeta{Size: int64(len(data)), Exists: true}, nil
}
func (c *memClient) ReadDir(path string) ([]fs.FileInfo, error) { return nil, nil }
func (c *memClient) OpenReader(path string) (io.ReadCloser, error) {
	data, ok := c.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (c *memClient) OpenWriter(path string) (io.WriteCloser, error) {
	return &memWriter{client: c, path: path}, nil
}
func (c *memClient) Remove(path string) error {
	delete(c.files, path)
	return nil
}
func (c *memClient) MkdirAll(path string) error {
	c.dirs = append(c.dirs, path)
	return nil
}
func (c *memClient) FastHash(string) (string, error) { return "", domain.ErrFastHashUnsupported }
func (c *memClient) Rename(oldPath, newPath string) error {
	c.renames++
	if c.failRename {
		return errors.New("rename refused by server")
	}
	c.files[newPath] = c.files[oldPath]
	delete(c.files, oldPath)
	return nil
}
func (c *memClient) Chtimes(string, time.Time, time.Time) error {
	c.times++
	return nil
}

type memWriter struct {
	client *memClient
	path   string
	buf    bytes.Buffer
}

type changingReaderClient struct {
	*memClient
	data         []byte
	snapshotSize int
}

func (c *changingReaderClient) OpenReader(string) (io.ReadCloser, error) {
	return &changingReader{
		data:         c.data,
		snapshotSize: c.snapshotSize,
	}, nil
}

type changingReader struct {
	data         []byte
	snapshotSize int
	offset       int
}

func (r *changingReader) Read(p []byte) (int, error) {
	if r.offset >= r.snapshotSize {
		return 0, errors.New("remote file changed")
	}
	end := min(r.snapshotSize, r.offset+len(p))
	n := copy(p, r.data[r.offset:end])
	r.offset += n
	return n, nil
}

func (r *changingReader) Close() error { return nil }

func (w *memWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *memWriter) Close() error {
	w.client.files[w.path] = w.buf.Bytes()
	return nil
}

func executor(t *testing.T, client domain.RemoteClient) *Executor {
	t.Helper()
	return &Executor{
		LocalRoot:  t.TempDir(),
		RemoteRoot: "/srv",
		Client:     client,
		Workers:    2,
	}
}

func uploadItem(name string) *domain.SyncFileItem {
	return &domain.SyncFileItem{
		RelativePath: name,
		Action:       domain.ActionUpload,
		LocalStat:    &domain.FileMeta{Exists: true, ModTime: time.Now()},
	}
}

func TestUploadRenameRefusedFallsBackToDirectWrite(t *testing.T) {
	dir := t.TempDir()
	content := "hello deploy"
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	client := newMemClient()
	client.failRename = true
	client.files["/srv/file.txt"] = []byte("old content") // target exists
	e := executor(t, client)
	e.LocalRoot = dir

	if err := e.Execute(context.Background(), []*domain.SyncFileItem{uploadItem("file.txt")}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := string(client.files["/srv/file.txt"]); got != content {
		t.Fatalf("remote content = %q, want %q", got, content)
	}
	if _, ok := client.files["/srv/file.txt.godeploy-tmp"]; ok {
		t.Fatal("tmp file should be removed after fallback")
	}
	if client.renames != 1 || client.times != 1 {
		t.Fatalf("renames=%d times=%d, want 1/1", client.renames, client.times)
	}
}

func TestExecutorReportsUploadByteProgress(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello deploy")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	client := newMemClient()
	e := executor(t, client)
	e.LocalRoot = dir
	var progress []int64
	e.OnProgress = func(event Progress) {
		progress = append(progress, event.Bytes)
	}
	item := uploadItem("file.txt")
	item.LocalStat.Size = int64(len(content))

	if err := e.Execute(context.Background(), []*domain.SyncFileItem{item}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(progress) == 0 || progress[len(progress)-1] != int64(len(content)) {
		t.Fatalf("progress = %v, want final byte count %d", progress, len(content))
	}
}

func TestDownloadStopsAtPlannedSnapshotSize(t *testing.T) {
	const snapshot = "original log"
	client := &changingReaderClient{
		memClient:    newMemClient(),
		data:         []byte(snapshot + "\nnew line appended while downloading"),
		snapshotSize: len(snapshot),
	}
	dir := t.TempDir()
	e := executor(t, client)
	e.LocalRoot = dir
	item := &domain.SyncFileItem{
		RelativePath: "app.log",
		Action:       domain.ActionDownload,
		RemoteStat: &domain.FileMeta{
			Size:   int64(len(snapshot)),
			Exists: true,
		},
	}

	if err := e.Execute(context.Background(), []*domain.SyncFileItem{item}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != snapshot {
		t.Fatalf("downloaded content = %q, want planned snapshot %q", got, snapshot)
	}
}

func TestOverwriteExistingFileTruncatesDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "download.tmp")
	targetPath := filepath.Join(dir, "server.log")
	want := []byte("new")
	if err := os.WriteFile(sourcePath, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("old content that is longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := overwriteExistingFile(sourcePath, targetPath); err != nil {
		t.Fatalf("overwriteExistingFile: %v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination = %q, want %q", got, want)
	}
}

func TestPrecreateCreatesEachDirOnce(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a/x.txt", "a/y.txt", "a/b/z.txt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	client := newMemClient()
	e := executor(t, client)
	e.LocalRoot = dir
	items := []*domain.SyncFileItem{uploadItem("a/x.txt"), uploadItem("a/y.txt"), uploadItem("a/b/z.txt")}
	if err := e.Execute(context.Background(), items); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(client.dirs) != 2 {
		t.Fatalf("MkdirAll called %d times, want 2 unique dirs: %v", len(client.dirs), client.dirs)
	}
	for _, d := range client.dirs {
		if d != "/srv/a" && d != "/srv/a/b" {
			t.Fatalf("unexpected dir %q", d)
		}
	}
	if len(client.files) != 3 {
		t.Fatalf("uploaded %d files, want 3", len(client.files))
	}
}

func TestUploadErrorCarriesContext(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644)
	client := newMemClient()
	client.failRename = true
	e := executor(t, client)
	e.LocalRoot = dir
	// Rename refused AND direct write... succeeds here, so force failure via
	// a target dir that cannot exist: make OpenWriter fail instead.
	broken := &brokenWriterClient{memClient: newMemClient()}
	e2 := executor(t, broken)
	e2.LocalRoot = dir
	err := e2.Execute(context.Background(), []*domain.SyncFileItem{uploadItem("f.txt")})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "open remote") {
		t.Fatalf("error missing operation context: %v", err)
	}
}

type brokenWriterClient struct {
	*memClient
}

func (c *brokenWriterClient) OpenWriter(string) (io.WriteCloser, error) {
	return nil, errors.New("boom")
}

func TestPlannerMultiScopeSetsItemScopes(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "b"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a", "x.txt"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b", "y.txt"), []byte("2"), 0o644)
	// Also a scope that does not exist locally: must not fail the scan.
	p := &Planner{
		Project: config.Project{LocalRoot: dir, RemoteRoot: "/srv"},
		Client:  newMemClient(),
		Diff:    diffengine.NewEngine(),
		Scopes:  []string{"a", "b"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	byScope := map[string]string{}
	for _, it := range items {
		byScope[it.Scope] = it.RelativePath
	}
	if byScope["a"] != "x.txt" || byScope["b"] != "y.txt" {
		t.Fatalf("items by scope = %v, want a=x.txt b=y.txt", byScope)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
}

func TestPlannerResolvesDifferentRemoteMappings(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"public/index.html":  "site",
		"ops/config/app.ini": "config",
	} {
		full := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	p := &Planner{
		Project: config.Project{
			LocalRoot: dir,
			PathMappings: []config.PathMapping{
				{LocalPath: "public", RemotePath: "/var/www/site"},
				{LocalPath: "ops/config", RemotePath: "/etc/site"},
			},
		},
		Client: newMemClient(),
		Diff:   diffengine.NewEngine(),
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	remoteByScope := map[string]string{}
	for _, item := range items {
		remoteByScope[item.Scope] = item.RemotePath
		if item.LocalPath == "" {
			t.Fatalf("local path was not resolved for %+v", item)
		}
	}
	if remoteByScope["public"] != "/var/www/site/index.html" {
		t.Fatalf("public remote = %q", remoteByScope["public"])
	}
	if remoteByScope["ops/config"] != "/etc/site/app.ini" {
		t.Fatalf("config remote = %q", remoteByScope["ops/config"])
	}
}

func TestPlannerKeepsSelectionSuffixWithinMapping(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "public", "assets", "logo.svg")
	_ = os.MkdirAll(filepath.Dir(full), 0o755)
	_ = os.WriteFile(full, []byte("logo"), 0o644)
	p := &Planner{
		Project: config.Project{LocalRoot: dir, PathMappings: []config.PathMapping{{LocalPath: "public", RemotePath: "/web"}}},
		Client:  newMemClient(),
		Diff:    diffengine.NewEngine(),
		Scopes:  []string{"public/assets"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RemotePath != "/web/assets/logo.svg" || items[0].Scope != "public/assets" {
		t.Fatalf("mapped item = %+v", items)
	}
}

func TestExecutorUsesResolvedItemPaths(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "source.txt")
	_ = os.WriteFile(localPath, []byte("mapped"), 0o644)
	client := newMemClient()
	e := executor(t, client)
	item := uploadItem("source.txt")
	item.LocalPath = localPath
	item.RemotePath = "/unrelated/target.txt"
	if err := e.Execute(context.Background(), []*domain.SyncFileItem{item}); err != nil {
		t.Fatal(err)
	}
	if got := string(client.files["/unrelated/target.txt"]); got != "mapped" {
		t.Fatalf("mapped upload content = %q", got)
	}
}
