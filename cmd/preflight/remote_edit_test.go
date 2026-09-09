package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"preflight/internal/domain"
)

// editFakeClient is a RemoteClient that serves a single remote file and
// records every upload.
type editFakeClient struct {
	mu      sync.Mutex
	content []byte // current remote content
	uploads [][]byte
	readErr error // when set, OpenReader fails (missing file)
}

func newEditFakeClient(content string) *editFakeClient {
	return &editFakeClient{content: []byte(content)}
}

func (c *editFakeClient) uploadedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.uploads)
}

func (c *editFakeClient) lastUpload() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.uploads) == 0 {
		return nil
	}
	return c.uploads[len(c.uploads)-1]
}

func (c *editFakeClient) Connect() error { return nil }
func (c *editFakeClient) Close() error   { return nil }
func (c *editFakeClient) Stat(string) (*domain.FileMeta, error) {
	return &domain.FileMeta{Exists: true}, nil
}
func (c *editFakeClient) ReadDir(string) ([]fs.FileInfo, error) { return nil, nil }
func (c *editFakeClient) OpenReader(string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return nil, c.readErr
	}
	return io.NopCloser(bytes.NewReader(c.content)), nil
}
func (c *editFakeClient) OpenWriter(string) (io.WriteCloser, error) {
	return &editFakeWriter{c: c}, nil
}
func (c *editFakeClient) Remove(string) error   { return nil }
func (c *editFakeClient) MkdirAll(string) error { return nil }
func (c *editFakeClient) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

var _ domain.RemoteClient = (*editFakeClient)(nil)

type editFakeWriter struct {
	c   *editFakeClient
	buf bytes.Buffer
}

func (w *editFakeWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *editFakeWriter) Close() error {
	w.c.mu.Lock()
	w.c.uploads = append(w.c.uploads, append([]byte{}, w.buf.Bytes()...))
	w.c.mu.Unlock()
	return nil
}

// editorHelperPath builds the command line for this test binary acting
// as the configured editor. The file to edit arrives as the last argv.
func remoteEditorHelperPath() string {
	exe, _ := os.Executable()
	return exe + " -test.run=TestRemoteEditorHelperProcess --"
}

// TestRemoteEditorHelperProcess is the editor stand-in for
// openRemoteEdit tests. Env contract:
//
//	PREFLIGHT_EDITOR_SCRIPT      content|sleepSecs|exitCode  (required)
//	PREFLIGHT_EDITOR_TARGET_FILE file to record the temp path in
//	PREFLIGHT_EDITOR_DONE_FILE   marker written just before exiting
//
// It overwrites the target file (last argv) with content, optionally
// sleeps (holding the "editor session" open so the save-watcher can
// fire), records its target, writes the done marker, then exits.
func TestRemoteEditorHelperProcess(t *testing.T) {
	script := os.Getenv("PREFLIGHT_EDITOR_SCRIPT")
	if script == "" {
		return
	}
	parts := strings.SplitN(script, "|", 3)
	content := parts[0]
	sleepSecs, _ := strconv.Atoi(parts[1])
	exitCode, _ := strconv.Atoi(parts[2])
	target := os.Args[len(os.Args)-1]
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("PREFLIGHT_EDITOR_TARGET_FILE"); out != "" {
		_ = os.WriteFile(out, []byte(target), 0o644)
	}
	if sleepSecs > 0 {
		time.Sleep(time.Duration(sleepSecs) * time.Second)
	}
	if done := os.Getenv("PREFLIGHT_EDITOR_DONE_FILE"); done != "" {
		_ = os.WriteFile(done, []byte("done"), 0o644)
	}
	os.Exit(exitCode)
}

// waitForUpload polls until the fake records an upload with the given
// content, failing after 5 s.
func waitForUpload(t *testing.T, fake *editFakeClient, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := fake.lastUpload(); got != nil && string(got) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("upload with content %q never arrived; uploads: %q", want, fake.uploads)
}

func TestOpenRemoteEditUploadsOnSave(t *testing.T) {
	oldTick := saveTick
	saveTick = 50 * time.Millisecond
	defer func() { saveTick = oldTick }()

	fake := newEditFakeClient("original content")
	dir := t.TempDir()
	doneFile := filepath.Join(dir, "done.flag")
	targetFile := filepath.Join(dir, "target.txt")
	// The editor writes the edit, holds the session open for 3 s, then
	// exits 0. The upload must arrive while the editor is still running.
	t.Setenv("PREFLIGHT_EDITOR_SCRIPT", "edited content|3|0")
	t.Setenv("PREFLIGHT_EDITOR_TARGET_FILE", targetFile)
	t.Setenv("PREFLIGHT_EDITOR_DONE_FILE", doneFile)

	factory := func() (domain.RemoteClient, error) { return fake, nil }
	err := openRemoteEdit(fake, factory, remoteEditorHelperPath(), "/remote/config.php")
	if err != nil {
		t.Fatalf("openRemoteEdit: %v", err)
	}
	// Upload-on-save proof: the edited content reached the remote while
	// the editor was still open (before the done marker appeared). Track
	// which of the two was observed first.
	var upTime, doneTime time.Time
	deadline := time.Now().Add(5 * time.Second)
	for (upTime.IsZero() || doneTime.IsZero()) && time.Now().Before(deadline) {
		if upTime.IsZero() {
			if got := fake.lastUpload(); got != nil && string(got) == "edited content" {
				upTime = time.Now()
			}
		}
		if doneTime.IsZero() {
			if _, err := os.Stat(doneFile); err == nil {
				doneTime = time.Now()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if upTime.IsZero() {
		t.Fatal("upload-on-save never happened")
	}
	if upTime.After(doneTime) {
		t.Fatalf("upload arrived after the editor exited (%v > %v) — watcher did not fire", upTime, doneTime)
	}
	// The final hash-check must not duplicate the upload.
	if fake.uploadedCount() != 1 {
		t.Fatalf("uploads = %d, want exactly one (save-triggered)", fake.uploadedCount())
	}
	// Temp file must be removed.
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal("editor never recorded its target")
	}
	if _, err := os.Stat(string(data)); !os.IsNotExist(err) {
		t.Fatalf("temp file must be removed, stat err = %v", err)
	}
}

func TestOpenRemoteEditFinalUploadOnExit(t *testing.T) {
	// saveTick far in the future: the watcher never fires, so the final
	// hash-checked upload after the editor exits must carry the edit.
	oldTick := saveTick
	saveTick = time.Hour
	defer func() { saveTick = oldTick }()

	fake := newEditFakeClient("original")
	t.Setenv("PREFLIGHT_EDITOR_SCRIPT", "final version|0|0")
	factory := func() (domain.RemoteClient, error) { return fake, nil }
	if err := openRemoteEdit(fake, factory, remoteEditorHelperPath(), "/remote/x.txt"); err != nil {
		t.Fatalf("openRemoteEdit: %v", err)
	}
	if fake.uploadedCount() != 1 || string(fake.lastUpload()) != "final version" {
		t.Fatalf("uploads = %d (%q), want exactly one final upload", fake.uploadedCount(), fake.lastUpload())
	}
}

func TestOpenRemoteEditUnchangedNoUpload(t *testing.T) {
	oldTick := saveTick
	saveTick = time.Hour
	defer func() { saveTick = oldTick }()

	fake := newEditFakeClient("same")
	t.Setenv("PREFLIGHT_EDITOR_SCRIPT", "same|0|0") // editor leaves content unchanged
	factory := func() (domain.RemoteClient, error) { return fake, nil }
	if err := openRemoteEdit(fake, factory, remoteEditorHelperPath(), "/remote/x.txt"); err != nil {
		t.Fatalf("openRemoteEdit: %v", err)
	}
	if fake.uploadedCount() != 0 {
		t.Fatalf("unchanged edit must not upload, got %d uploads", fake.uploadedCount())
	}
}

func TestOpenRemoteEditFailedEditorKeepsTemp(t *testing.T) {
	oldTick := saveTick
	saveTick = time.Hour // watcher must not fire before the failure path
	defer func() { saveTick = oldTick }()

	fake := newEditFakeClient("original")
	dir := t.TempDir()
	targetFile := filepath.Join(dir, "target.txt")
	t.Setenv("PREFLIGHT_EDITOR_SCRIPT", "broken edit|0|7") // exit code 7
	t.Setenv("PREFLIGHT_EDITOR_TARGET_FILE", targetFile)
	factory := func() (domain.RemoteClient, error) { return fake, nil }
	err := openRemoteEdit(fake, factory, remoteEditorHelperPath(), "/remote/x.txt")
	if err == nil {
		t.Fatal("failed editor must return an error")
	}
	data, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		t.Fatal("editor never recorded its target")
	}
	if _, statErr := os.Stat(string(data)); statErr != nil {
		t.Fatalf("temp file should be kept for recovery: %v", statErr)
	}
	if fake.uploadedCount() != 0 {
		t.Fatalf("failed editor must not upload the broken edit, got %d uploads", fake.uploadedCount())
	}
}

func TestOpenRemoteEditDownloadError(t *testing.T) {
	fake := newEditFakeClient("x")
	fake.readErr = errors.New("no such file")
	factory := func() (domain.RemoteClient, error) { return fake, nil }
	if err := openRemoteEdit(fake, factory, "hx", "/remote/missing.txt"); err == nil {
		t.Fatal("missing remote file must be an error")
	}
}
