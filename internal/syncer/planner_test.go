package syncer

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"preflight/internal/config"
	diffengine "preflight/internal/diff"
	"preflight/internal/domain"
)

type fakeInfo struct {
	name string
	size int64
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// slowReadClient simulates a high-latency transport: every remote read
// takes delay and the client tracks the maximum number of concurrent
// reads in flight.
type slowReadClient struct {
	*memClient
	delay     time.Duration
	active    atomic.Int32
	maxActive atomic.Int32
}

func (c *slowReadClient) ReadDir(path string) ([]fs.FileInfo, error) {
	var infos []fs.FileInfo
	for name := range c.files {
		if !strings.HasPrefix(name, path+"/") {
			continue
		}
		rel := strings.TrimPrefix(name, path+"/")
		if strings.Contains(rel, "/") {
			continue
		}
		infos = append(infos, fakeInfo{name: rel, size: int64(len(c.files[name]))})
	}
	return infos, nil
}

func (c *slowReadClient) OpenReader(path string) (io.ReadCloser, error) {
	n := c.active.Add(1)
	for {
		m := c.maxActive.Load()
		if n <= m || c.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(c.delay)
	c.active.Add(-1)
	data, ok := c.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func TestPlannerParallelCompare(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('0'+i))+".txt"), []byte("local"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	client := &slowReadClient{memClient: newMemClient(), delay: 100 * time.Millisecond}
	for i := 0; i < n; i++ {
		client.files["/srv/f"+string(rune('0'+i))+".txt"] = []byte("remote")
	}
	clients := []domain.RemoteClient{client, client, client} // 3 connections
	p := &Planner{
		Project:        config.Project{LocalRoot: dir, RemoteRoot: "/srv", DefaultDiffMode: "text"},
		Client:         client,
		Diff:           diffengine.NewEngine(),
		Scopes:         []string{""},
		CompareClients: clients,
	}
	start := time.Now()
	items, err := p.Build()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != n {
		t.Fatalf("items = %d, want %d", len(items), n)
	}
	if client.maxActive.Load() < 2 {
		t.Fatalf("expected overlapping remote reads, max concurrent = %d", client.maxActive.Load())
	}
	// Serial would take ~8*100ms = 800ms; parallel with 3 workers ~300ms.
	if elapsed > 600*time.Millisecond {
		t.Fatalf("compare not parallelized: took %v", elapsed)
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].RelativePath > items[i].RelativePath {
			t.Fatalf("items not sorted: %s after %s", items[i].RelativePath, items[i-1].RelativePath)
		}
	}
}

func TestPlannerSingleClientFallback(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte("local"), 0o644)
	client := newMemClient()
	client.files["/srv/x.txt"] = []byte("remote")
	p := &Planner{
		Project: config.Project{LocalRoot: dir, RemoteRoot: "/srv", DefaultDiffMode: "text"},
		Client:  client,
		Diff:    diffengine.NewEngine(),
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RelativePath != "x.txt" {
		t.Fatalf("items = %+v", items)
	}
}

func TestPlannerTextDiffSkipsUnsafeInputs(t *testing.T) {
	tests := []struct {
		name       string
		localName  string
		localData  []byte
		remoteData []byte
		localSize  int64
		remoteSize int64
		wantAction domain.ActionType
		wantReason string
	}{
		{
			name:       "file reported over size limit",
			localName:  "export.csv",
			localData:  []byte("local"),
			remoteData: []byte("remote"),
			localSize:  domain.MaxTextDiffBytes + 1,
			remoteSize: int64(len("remote")),
			wantAction: domain.ActionUpload,
			wantReason: textDiffSizeReason,
		},
		{
			name:       "matching file reported over size limit",
			localName:  "matching.csv",
			localData:  []byte("same"),
			remoteData: []byte("same"),
			localSize:  domain.MaxTextDiffBytes + 1,
			remoteSize: domain.MaxTextDiffBytes + 1,
			wantAction: domain.ActionSkip,
			wantReason: textDiffSizeReason,
		},
		{
			name:       "log file",
			localName:  "server.log",
			localData:  []byte("local"),
			remoteData: []byte("remote"),
			localSize:  int64(len("local")),
			remoteSize: int64(len("remote")),
			wantAction: domain.ActionUpload,
			wantReason: "text diff skipped: log file",
		},
		{
			name:       "rotated log file",
			localName:  "SERVER.LOG.1",
			localData:  []byte("local"),
			remoteData: []byte("remote"),
			localSize:  int64(len("local")),
			remoteSize: int64(len("remote")),
			wantAction: domain.ActionUpload,
			wantReason: "text diff skipped: log file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			localPath := filepath.Join(dir, tt.localName)
			if err := os.WriteFile(localPath, tt.localData, 0o644); err != nil {
				t.Fatal(err)
			}
			remotePath := "/srv/" + tt.localName
			client := newMemClient()
			client.files[remotePath] = tt.remoteData
			planner := &Planner{
				Project: config.Project{DefaultDiffMode: "text"},
				Diff:    diffengine.NewEngine(),
			}

			action, details, err := planner.compare(
				client,
				localPath,
				remotePath,
				&domain.FileMeta{Size: tt.localSize, Exists: true},
				&domain.FileMeta{Size: tt.remoteSize, Exists: true},
			)
			if err != nil {
				t.Fatal(err)
			}
			if action != tt.wantAction {
				t.Fatalf("action = %v, want %v", action, tt.wantAction)
			}
			if details.Unified != "" {
				t.Fatalf("unsafe input produced a text diff: %q", details.Unified)
			}
			if !strings.Contains(details.Summary, tt.wantReason) {
				t.Fatalf("summary = %q, want reason %q", details.Summary, tt.wantReason)
			}
		})
	}
}

func TestPlannerTextDiffEnforcesReadLimitWhenMetadataIsStale(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "growing.csv")
	localData := bytes.Repeat([]byte("local,"), int(domain.MaxTextDiffBytes)/len("local,")+1)
	if err := os.WriteFile(localPath, localData, 0o644); err != nil {
		t.Fatal(err)
	}
	remotePath := "/srv/growing.csv"
	client := newMemClient()
	client.files[remotePath] = bytes.Repeat([]byte("remote,"), int(domain.MaxTextDiffBytes)/len("remote,")+1)
	planner := &Planner{
		Project: config.Project{DefaultDiffMode: "text"},
		Diff:    diffengine.NewEngine(),
	}

	action, details, err := planner.compare(
		client,
		localPath,
		remotePath,
		&domain.FileMeta{Size: 1, Exists: true},
		&domain.FileMeta{Size: 1, Exists: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if action != domain.ActionUpload {
		t.Fatalf("action = %v, want upload", action)
	}
	if details.Unified != "" {
		t.Fatalf("oversized input produced a text diff: %q", details.Unified)
	}
	if !strings.Contains(details.Summary, textDiffSizeReason) {
		t.Fatalf("summary = %q, want reason %q", details.Summary, textDiffSizeReason)
	}
	if !details.TooLarge {
		t.Fatal("oversized text input should advertise the on-demand diff")
	}
}

func TestPlannerLoadTextDiffBypassesLineGuard(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "large.csv")
	localData := []byte(strings.Repeat("local\n", domain.MaxDiffLines+1))
	if err := os.WriteFile(localPath, localData, 0o644); err != nil {
		t.Fatal(err)
	}
	remotePath := "/srv/large.csv"
	client := newMemClient()
	client.files[remotePath] = []byte(strings.Repeat("remote\n", domain.MaxDiffLines+1))
	planner := &Planner{
		Project: config.Project{DefaultDiffMode: "text"},
		Client:  client,
		Diff:    diffengine.NewEngine(),
	}

	details, err := planner.LoadTextDiff(&domain.SyncFileItem{LocalPath: localPath, RemotePath: remotePath})
	if err != nil {
		t.Fatal(err)
	}
	if details.TooLarge || details.Unified == "" {
		t.Fatalf("on-demand diff was not generated: %+v", details)
	}
}

func TestPlannerFileScopeIdentical(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "public", "img"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "public", "img", "logo.png"), []byte("same"), 0o644)
	client := newMemClient()
	client.files["/srv/img/logo.png"] = []byte("same")
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"public/img/logo.png"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly the file scope", items)
	}
	item := items[0]
	if item.Action != domain.ActionSkip {
		t.Fatalf("action = %v, want skip (files identical)", item.Action)
	}
	wantLocal := filepath.Join(dir, "public", "img", "logo.png")
	if item.LocalPath != wantLocal {
		t.Fatalf("LocalPath = %q, want %q", item.LocalPath, wantLocal)
	}
	if item.RemotePath != "/srv/img/logo.png" {
		t.Fatalf("RemotePath = %q, want /srv/img/logo.png", item.RemotePath)
	}
}

func TestPlannerFileScopeNewRemote(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "public", "new.txt"), []byte("fresh"), 0o644)
	client := newMemClient() // remote has nothing
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"public/new.txt"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Action != domain.ActionUpload {
		t.Fatalf("items = %+v, want one upload", items)
	}
	if items[0].RemotePath != "/srv/new.txt" {
		t.Fatalf("RemotePath = %q, want /srv/new.txt", items[0].RemotePath)
	}
	if items[0].LocalPath != filepath.Join(dir, "public", "new.txt") {
		t.Fatalf("LocalPath = %q", items[0].LocalPath)
	}
}

func TestPlannerFileScopeDeletedLocal(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0o755)
	// file does not exist locally (deleted)
	client := newMemClient()
	client.files["/srv/deleted.txt"] = []byte("still on remote")
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"public/deleted.txt"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Action != domain.ActionDownload {
		t.Fatalf("items = %+v, want one download (remote only)", items)
	}
	if items[0].RemotePath != "/srv/deleted.txt" {
		t.Fatalf("RemotePath = %q, want /srv/deleted.txt", items[0].RemotePath)
	}
}

func TestPlannerMultiScopeSkipsUnmapped(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "public"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "unmapped"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "public", "app.js"), []byte("js"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "unmapped", "skip.txt"), []byte("skip"), 0o644)

	client := newMemClient()
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"public/app.js", "unmapped/skip.txt"},
	}
	items, err := p.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	wantLocal := filepath.Join(dir, "public", "app.js")
	if items[0].LocalPath != wantLocal {
		t.Fatalf("LocalPath = %q, want %q", items[0].LocalPath, wantLocal)
	}
	if items[0].RemotePath != "/srv/app.js" {
		t.Fatalf("RemotePath = %q, want /srv/app.js", items[0].RemotePath)
	}
}

func TestPlannerSingleScopeUnmappedErrors(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "unmapped"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "unmapped", "skip.txt"), []byte("skip"), 0o644)

	client := newMemClient()
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"unmapped/skip.txt"},
	}
	_, err := p.Build()
	if err == nil || !strings.Contains(err.Error(), "not covered by a path_mapping") {
		t.Fatalf("expected error containing 'not covered by a path_mapping', got %v", err)
	}
}

func TestPlannerMultiScopeAllUnmappedErrors(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "unmapped"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "unmapped", "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "unmapped", "b.txt"), []byte("b"), 0o644)

	client := newMemClient()
	p := &Planner{
		Project: config.Project{
			LocalRoot:       dir,
			DefaultDiffMode: "text",
			PathMappings:    []config.PathMapping{{LocalPath: "public", RemotePath: "/srv"}},
		},
		Client: client,
		Diff:   diffengine.NewEngine(),
		Scopes: []string{"unmapped/a.txt", "unmapped/b.txt"},
	}
	_, err := p.Build()
	if err == nil || !strings.Contains(err.Error(), "no selected paths are covered by a path_mapping") {
		t.Fatalf("expected error containing 'no selected paths are covered by a path_mapping', got %v", err)
	}
}

