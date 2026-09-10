package taskrun

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"preflight/internal/config"
	"preflight/internal/domain"
)

// fakeClient records exec commands and streams scripted output. A nil
// execErr means success.
type fakeClient struct {
	commands []string
	output   string
	execErr  error
}

func (f *fakeClient) Connect() error { return nil }
func (f *fakeClient) Close() error   { return nil }
func (f *fakeClient) Stat(string) (*domain.FileMeta, error) {
	return &domain.FileMeta{Exists: true}, nil
}
func (f *fakeClient) ReadDir(string) ([]fs.FileInfo, error) { return nil, nil }
func (f *fakeClient) OpenReader(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeClient) OpenWriter(string) (io.WriteCloser, error) {
	return nopWriteCloser{&bytes.Buffer{}}, nil
}
func (f *fakeClient) Remove(string) error   { return nil }
func (f *fakeClient) MkdirAll(string) error { return nil }
func (f *fakeClient) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}
func (f *fakeClient) Exec(command string, stdout, stderr io.Writer) error {
	f.commands = append(f.commands, command)
	if f.output != "" {
		_, _ = io.WriteString(stdout, f.output)
	}
	return f.execErr
}

type nopWriteCloser struct{ *bytes.Buffer }

func (n nopWriteCloser) Close() error { return nil }

// scriptClient fails any command containing a key in errs.
type scriptClient struct {
	fakeClient
	errs map[string]error
}

func (s *scriptClient) Exec(command string, stdout, stderr io.Writer) error {
	s.commands = append(s.commands, command)
	for substr, err := range s.errs {
		if strings.Contains(command, substr) {
			return err
		}
	}
	return nil
}

// shellLess implements RemoteClient without Execer.
type shellLess struct{}

func (s *shellLess) Connect() error { return nil }
func (s *shellLess) Close() error   { return nil }
func (s *shellLess) Stat(string) (*domain.FileMeta, error) {
	return &domain.FileMeta{Exists: true}, nil
}
func (s *shellLess) ReadDir(string) ([]fs.FileInfo, error) { return nil, nil }
func (s *shellLess) OpenReader(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (s *shellLess) OpenWriter(string) (io.WriteCloser, error) {
	return nopWriteCloser{&bytes.Buffer{}}, nil
}
func (s *shellLess) Remove(string) error   { return nil }
func (s *shellLess) MkdirAll(string) error { return nil }
func (s *shellLess) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

var (
	_ domain.RemoteClient = (*fakeClient)(nil)
	_ domain.Execer       = (*fakeClient)(nil)
	_ domain.RemoteClient = (*shellLess)(nil)
	_ StepExecutor        = commandExecutor{}
	_ StepExecutor        = uploadExecutor{}
)

// memFile is an in-memory LocalFile for upload tests.
type memFile struct {
	*bytes.Reader
	info memFileInfo
}

func (m *memFile) Close() error { return nil }

func (m *memFile) Stat() (os.FileInfo, error) { return m.info, nil }

type memFileInfo struct{ modTime time.Time }

func (i memFileInfo) Name() string       { return "mem" }
func (i memFileInfo) Size() int64        { return 0 }
func (i memFileInfo) Mode() fs.FileMode  { return 0o644 }
func (i memFileInfo) ModTime() time.Time { return i.modTime }
func (i memFileInfo) IsDir() bool        { return false }
func (i memFileInfo) Sys() any           { return nil }

func openMem(content string) OpenFunc {
	return func(string) (LocalFile, error) {
		return &memFile{Reader: bytes.NewReader([]byte(content))}, nil
	}
}

// captureClient records uploads: writers per path, mkdirs, renames.
type captureClient struct {
	fakeClient
	writes  map[string]*bytes.Buffer
	mkdir   []string
	renamed [][2]string
	times   map[string]time.Time
}

func newCaptureClient() *captureClient {
	return &captureClient{writes: map[string]*bytes.Buffer{}, times: map[string]time.Time{}}
}

func (c *captureClient) OpenWriter(path string) (io.WriteCloser, error) {
	buf := &bytes.Buffer{}
	c.writes[path] = buf
	return nopWriteCloser{buf}, nil
}

func (c *captureClient) MkdirAll(path string) error {
	c.mkdir = append(c.mkdir, path)
	return nil
}

func (c *captureClient) Rename(oldPath, newPath string) error {
	c.renamed = append(c.renamed, [2]string{oldPath, newPath})
	if buf, ok := c.writes[oldPath]; ok {
		c.writes[newPath] = buf
		delete(c.writes, oldPath)
	}
	return nil
}

func (c *captureClient) Chtimes(path string, _, mtime time.Time) error {
	c.times[path] = mtime
	return nil
}

var (
	_ domain.RemoteClient   = (*captureClient)(nil)
	_ domain.RenameCapable  = (*captureClient)(nil)
	_ domain.ChtimesCapable = (*captureClient)(nil)
)

func TestComposeCommand(t *testing.T) {
	tests := []struct {
		name    string
		workdir string
		run     string
		shell   string
		want    string
	}{
		{name: "no workdir", workdir: "", run: "make build", shell: "", want: "make build"},
		{name: "blank workdir", workdir: "  ", run: "make build", shell: "", want: "make build"},
		{name: "plain workdir", workdir: "apiv2", run: "make build", shell: "", want: "cd 'apiv2' && make build"},
		{name: "spaced workdir", workdir: "my app", run: "make build", shell: "", want: "cd 'my app' && make build"},
		{name: "quoted workdir", workdir: "it's", run: "make build", shell: "", want: `cd 'it'\''s' && make build`},
		{name: "absolute workdir", workdir: "/srv/app", run: "supervisorctl restart x", shell: "",
			want: "cd '/srv/app' && supervisorctl restart x"},
		{name: "login shell", workdir: "apiv2", run: "make build", shell: "bash -lc",
			want: `bash -lc 'cd '\''apiv2'\'' && make build'`},
		{name: "login shell no workdir", workdir: "", run: "make build", shell: "bash -lc",
			want: `bash -lc 'make build'`},
		{name: "blank shell ignored", workdir: "apiv2", run: "make build", shell: "  ",
			want: "cd 'apiv2' && make build"},
		{name: "shell quotes run quotes", workdir: "", run: `echo 'hi'`, shell: "bash -lc",
			want: `bash -lc 'echo '\''hi'\'''`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComposeCommand(tt.workdir, tt.run, tt.shell); got != tt.want {
				t.Fatalf("ComposeCommand(%q, %q, %q) = %q, want %q",
					tt.workdir, tt.run, tt.shell, got, tt.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(errors.New("boom")); got != -1 {
		t.Fatalf("exitCode(generic) = %d, want -1", got)
	}
	if got := describeFailure(errors.New("boom")); got != "boom" {
		t.Fatalf("describeFailure(generic) = %q, want boom", got)
	}
}

// rig wires fake clients per account and counts dials.
type rig struct {
	byAccount map[string]domain.RemoteClient
	dials     map[string]int
	failDial  map[string]error
	lookupErr error
}

func (r *rig) lookup(id string) (config.Account, error) {
	if r.lookupErr != nil {
		return config.Account{}, r.lookupErr
	}
	return config.Account{ID: id}, nil
}

func (r *rig) connect(account config.Account) (domain.RemoteClient, error) {
	r.dials[account.ID]++
	if err := r.failDial[account.ID]; err != nil {
		return nil, err
	}
	if c, ok := r.byAccount[account.ID]; ok {
		return c, nil
	}
	c := &fakeClient{}
	r.byAccount[account.ID] = c
	return c, nil
}

func TestRunStopOnFirstFailure(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{}}
	r.byAccount["kiosks"] = &scriptClient{errs: map[string]error{"false": errors.New("exit status 1")}}
	task := config.Task{ID: "deploy", Steps: []config.TaskStep{
		{AccountID: "kiosks", Workdir: "apiv2", Run: "make build"},
		{AccountID: "kiosks", Run: "false"},
		{AccountID: "kiosks", Run: "echo never"},
	}}
	var out bytes.Buffer
	results, err := Run(task, r.lookup, r.connect, &out)
	if err == nil {
		t.Fatal("Run should return the first step error")
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want one per step", len(results))
	}
	if results[0].Err != nil || results[0].Skipped {
		t.Fatalf("step 1 should succeed: %+v", results[0])
	}
	if !results[1].Failed() {
		t.Fatalf("step 2 should fail: %+v", results[1])
	}
	if !results[2].Skipped || results[2].Err != nil {
		t.Fatalf("step 3 should be skipped without error: %+v", results[2])
	}
	if results[0].Command != "cd 'apiv2' && make build" {
		t.Fatalf("step 1 command = %q", results[0].Command)
	}
	if r.dials["kiosks"] != 1 {
		t.Fatalf("dials = %d, want one connection reused", r.dials["kiosks"])
	}
	text := out.String()
	for _, want := range []string{"▸ [kiosks] $", "✓ step 1 ok", "✗ step 2 failed", `task "deploy" failed`} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestRunContinueOnError(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{}}
	r.byAccount["a"] = &scriptClient{errs: map[string]error{"bad": errors.New("exit status 2")}}
	task := config.Task{ID: "c", OnError: "continue", Steps: []config.TaskStep{
		{AccountID: "a", Run: "good one"},
		{AccountID: "a", Run: "bad one"},
		{AccountID: "a", Run: "good two"},
	}}
	var out bytes.Buffer
	results, err := Run(task, r.lookup, r.connect, &out)
	if err == nil {
		t.Fatal("Run should return the first error even when continuing")
	}
	if len(results) != 3 || !results[1].Failed() || results[0].Err != nil || results[2].Err != nil {
		t.Fatalf("continue should run all steps with middle failing: %+v", results)
	}
	for _, result := range results {
		if result.Skipped {
			t.Fatalf("no step should be skipped: %+v", results)
		}
	}
}

func TestRunOpensOneConnectionPerAccount(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{}}
	task := config.Task{ID: "multi", Steps: []config.TaskStep{
		{AccountID: "a", Run: "one"},
		{AccountID: "b", Run: "two"},
		{AccountID: "a", Run: "three"},
	}}
	var out bytes.Buffer
	if _, err := Run(task, r.lookup, r.connect, &out); err != nil {
		t.Fatal(err)
	}
	if r.dials["a"] != 1 || r.dials["b"] != 1 {
		t.Fatalf("dials = %v, want one per account", r.dials)
	}
}

func TestRunLookupFailure(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{},
		lookupErr: errors.New(`account "ghost" not found`)}
	task := config.Task{ID: "x", Steps: []config.TaskStep{{AccountID: "ghost", Run: "y"}}}
	var out bytes.Buffer
	results, err := Run(task, r.lookup, r.connect, &out)
	if err == nil || len(results) != 1 || results[0].Err == nil {
		t.Fatalf("lookup failure should fail the step: %+v, %v", results, err)
	}
	if len(r.dials) != 0 {
		t.Fatalf("no dials should happen: %v", r.dials)
	}
}

func TestRunDialFailure(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{},
		failDial: map[string]error{"a": errors.New("connection refused")}}
	task := config.Task{ID: "x", Steps: []config.TaskStep{{AccountID: "a", Run: "y"}}}
	var out bytes.Buffer
	if _, err := Run(task, r.lookup, r.connect, &out); err == nil {
		t.Fatal("dial failure should fail the run")
	}
}

func TestRunRejectsShellLessClient(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{"ftp-ish": &shellLess{}}, dials: map[string]int{}}
	task := config.Task{ID: "x", Steps: []config.TaskStep{{AccountID: "ftp-ish", Run: "y"}}}
	var out bytes.Buffer
	results, err := Run(task, r.lookup, r.connect, &out)
	if err == nil || !strings.Contains(err.Error(), "no shell") {
		t.Fatalf("shell-less client should error clearly: %+v, %v", results, err)
	}
}

func TestExecutorFor(t *testing.T) {
	tests := []struct {
		name string
		step config.TaskStep
		want string
	}{
		{name: "run step", step: config.TaskStep{AccountID: "a", Run: "make"}, want: "taskrun.commandExecutor"},
		{name: "upload step", step: config.TaskStep{AccountID: "a", Upload: "f", Remote: "/srv/f"}, want: "taskrun.uploadExecutor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := executorFor(tt.step, nil)
			if typ := fmt.Sprintf("%T", got); typ != tt.want {
				t.Fatalf("executorFor(%+v) = %s, want %s", tt.step, typ, tt.want)
			}
		})
	}
}

func TestExecutorCommand(t *testing.T) {
	run := config.TaskStep{AccountID: "a", Workdir: "apiv2", Run: "make build"}
	if got := (commandExecutor{}).Command(run); got != "cd 'apiv2' && make build" {
		t.Fatalf("commandExecutor.Command = %q", got)
	}
	up := config.TaskStep{AccountID: "a", Upload: "dist/a", Remote: "/srv/a"}
	if got := (uploadExecutor{}).Command(up); got != "↑ dist/a → /srv/a" {
		t.Fatalf("uploadExecutor.Command = %q", got)
	}
}

func TestRunUploadInterleaved(t *testing.T) {
	cap := newCaptureClient()
	r := &rig{byAccount: map[string]domain.RemoteClient{"k": cap}, dials: map[string]int{}}
	task := config.Task{ID: "deploy", Steps: []config.TaskStep{
		{AccountID: "k", Run: "stop"},
		{AccountID: "k", Upload: "dist/app.bin", Remote: "/opt/app/app.bin"},
		{AccountID: "k", Run: "start"},
	}}
	runner := &Runner{Lookup: r.lookup, Connect: r.connect, Open: openMem("binary-bytes")}
	var out bytes.Buffer
	results, err := runner.Run(task, &out)
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	for i, result := range results {
		if result.Err != nil || result.Skipped {
			t.Fatalf("step %d should succeed: %+v", i+1, result)
		}
	}
	if r.dials["k"] != 1 {
		t.Fatalf("dials = %d, want one connection reused across run and upload", r.dials["k"])
	}
	got := ""
	if buf, ok := cap.writes["/opt/app/app.bin"]; ok {
		got = buf.String()
	}
	if got != "binary-bytes" {
		t.Fatalf("remote content = %q, want %q", got, "binary-bytes")
	}
	if len(cap.mkdir) != 1 || cap.mkdir[0] != "/opt/app" {
		t.Fatalf("mkdirs = %v, want [/opt/app]", cap.mkdir)
	}
	if len(cap.renamed) != 1 || cap.renamed[0] != [2]string{"/opt/app/app.bin.preflight-tmp", "/opt/app/app.bin"} {
		t.Fatalf("renames = %v, want tmp rename", cap.renamed)
	}
	text := out.String()
	for _, want := range []string{"▸ [k] $ stop", "▸ [k] ↑ dist/app.bin → /opt/app/app.bin", "✓ step 2 ok", `task "deploy" ok (3/3 steps)`} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestRunUploadMissingLocalStops(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{}}
	task := config.Task{ID: "deploy", Steps: []config.TaskStep{
		{AccountID: "k", Upload: "dist/missing.bin", Remote: "/srv/missing.bin"},
		{AccountID: "k", Run: "echo never"},
	}}
	runner := &Runner{Lookup: r.lookup, Connect: r.connect, Open: func(string) (LocalFile, error) {
		return nil, os.ErrNotExist
	}}
	var out bytes.Buffer
	results, err := runner.Run(task, &out)
	if err == nil {
		t.Fatal("Run should return the upload error")
	}
	if !results[0].Failed() || !strings.Contains(results[0].Err.Error(), "open local") {
		t.Fatalf("step 1 should fail opening the local file: %+v", results[0])
	}
	if !results[1].Skipped || results[1].Err != nil {
		t.Fatalf("step 2 should be skipped without error: %+v", results[1])
	}
}

func TestRunUploadFailureContinues(t *testing.T) {
	r := &rig{byAccount: map[string]domain.RemoteClient{}, dials: map[string]int{}}
	r.byAccount["k"] = &failWriter{err: errors.New("disk full")}
	task := config.Task{ID: "c", OnError: "continue", Steps: []config.TaskStep{
		{AccountID: "k", Run: "good one"},
		{AccountID: "k", Upload: "dist/a", Remote: "/srv/a"},
		{AccountID: "k", Run: "good two"},
	}}
	runner := &Runner{Lookup: r.lookup, Connect: r.connect, Open: openMem("x")}
	var out bytes.Buffer
	results, err := runner.Run(task, &out)
	if err == nil {
		t.Fatal("Run should return the first error even when continuing")
	}
	if len(results) != 3 || !results[1].Failed() || results[0].Err != nil || results[2].Err != nil {
		t.Fatalf("continue should run all steps with middle failing: %+v", results)
	}
}

// failWriter fails OpenWriter while allowing Exec to succeed.
type failWriter struct {
	fakeClient
	err error
}

func (f *failWriter) OpenWriter(string) (io.WriteCloser, error) { return nil, f.err }
