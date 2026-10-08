package manage

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"preflight/internal/config"
	"preflight/internal/domain"
	"preflight/internal/remote"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(m tea.Model, msg tea.Msg) tea.Model {
	updated, _ := m.Update(msg)
	return updated
}

func sendKey(m tea.Model, s string) tea.Model {
	return send(m, key(s))
}

func sized(t *testing.T) tea.Model {
	t.Helper()
	m := New(&config.Config{}, t.TempDir()+"/config.yaml")
	return send(m, tea.WindowSizeMsg{Width: 100, Height: 40})
}

func TestAddAccountAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{}
	m := send(New(cfg, path), tea.WindowSizeMsg{Width: 100, Height: 40})

	m = sendKey(m, "n")
	if m.(Model).form == nil {
		t.Fatal("expected account form to open")
	}

	m = sendKey(m, "prod2") // id
	m = sendKey(m, "down")  // name
	m = sendKey(m, "Prod Two")
	m = sendKey(m, "down") // protocol (leave sftp)
	m = sendKey(m, "down") // host
	m = sendKey(m, "example.org")
	m = sendKey(m, "down") // port (leave 22)
	m = sendKey(m, "down") // tls
	m = sendKey(m, "down") // auth (leave key)
	m = sendKey(m, "down") // username
	m = sendKey(m, "deploy")
	m = sendKey(m, "down") // key path
	m = sendKey(m, "~/.ssh/id_ed25519")
	m = sendKey(m, "enter")

	mm := m.(Model)
	if mm.form != nil {
		t.Fatalf("form still open after save: status=%q", mm.status)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].ID != "prod2" || cfg.Accounts[0].Host != "example.org" {
		t.Fatalf("account not saved: %+v", cfg.Accounts)
	}
	if cfg.Accounts[0].Auth.Username != "deploy" || cfg.Accounts[0].Auth.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("auth not saved: %+v", cfg.Accounts[0].Auth)
	}
	if cfg.Accounts[0].Port != 22 || cfg.Accounts[0].Protocol != "sftp" {
		t.Fatalf("protocol defaults wrong: %+v", cfg.Accounts[0])
	}

	// Enter must already have written the file (auto-save); the manual
	// 's' key is a re-write.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written after form save: %v", err)
	}
	if !strings.Contains(string(data), "prod2") || !strings.Contains(string(data), "id_ed25519") {
		t.Fatalf("config file missing account data:\n%s", data)
	}
	if mm.dirty {
		t.Fatal("dirty flag should be cleared after auto-save")
	}

	m = sendKey(m, "s")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "prod2") {
		t.Fatalf("config file missing account after manual save:\n%s", data)
	}
}

func TestDeleteAccountWithRefsRequiresConfirm(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{ID: "a1", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "key", Username: "u"}}},
		Projects: []config.Project{{ID: "p1", AccountID: "a1", LocalRoot: "/x", RemoteRoot: "/y"}},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := send(New(cfg, path), tea.WindowSizeMsg{Width: 100, Height: 40})

	m = sendKey(m, "d")
	if mm := m.(Model); mm.confirm == "" || !strings.Contains(mm.confirm, "p1") {
		t.Fatalf("expected reference warning, got %q", mm.confirm)
	}
	m = sendKey(m, "n") // cancel
	if len(cfg.Accounts) != 1 {
		t.Fatal("account should not be deleted after cancel")
	}
	m = sendKey(m, "d")
	m = sendKey(m, "y")
	mm := m.(Model)
	if len(cfg.Accounts) != 0 {
		t.Fatal("account should be deleted after confirm")
	}
	// The deletion is persisted immediately: reloading the file must
	// show zero accounts.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Accounts) != 0 {
		t.Fatalf("deleted account still on disk: %+v", reloaded.Accounts)
	}
	if mm.dirty {
		t.Fatal("dirty flag should be cleared after persisted delete")
	}
}

func TestAccountShellShortcutReturnsSelectedAccount(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{
		{ID: "one", Protocol: "sftp", Host: "one.example.com", Port: 22, Auth: config.Auth{Username: "deploy"}},
		{ID: "orphan", Protocol: "sftp", Host: "orphan.example.com", Port: 22, Auth: config.Auth{Username: "ops"}},
	}}
	m := sendKey(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), "down")
	updated, cmd := m.Update(key("t"))
	if cmd == nil {
		t.Fatal("t on an SFTP account should quit to the shell runner")
	}
	if got := updated.(Model).ShellAccountID(); got != "orphan" {
		t.Fatalf("ShellAccountID() = %q, want orphan", got)
	}
}

func TestAccountShellShortcutRejectsFTP(t *testing.T) {
	cfg := &config.Config{Accounts: []config.Account{{
		ID: "legacy", Protocol: "ftp", Host: "ftp.example.com", Port: 21, Auth: config.Auth{Username: "legacy"},
	}}}
	m := New(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	updated, cmd := m.Update(key("t"))
	mm := updated.(Model)
	if cmd != nil || mm.ShellAccountID() != "" {
		t.Fatal("FTP account must not request an SSH shell")
	}
	if !strings.Contains(strings.ToLower(mm.status), "sftp") {
		t.Fatalf("status = %q, want SFTP-only hint", mm.status)
	}
}

// TestReopenPersistsAccount simulates the exact app flow: add an account,
// save the form, quit, and reopen (as main.go does with LoadOrEmpty).
func TestReopenPersistsAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	// Session 1: add an account and quit.
	m := send(New(&config.Config{}, path), tea.WindowSizeMsg{Width: 100, Height: 40})
	m = sendKey(m, "n")
	m = sendKey(m, "persist-test")
	m = sendKey(m, "down")
	m = sendKey(m, "Persist")
	m = sendKey(m, "down")
	m = sendKey(m, "down")
	m = sendKey(m, "example.org")
	m = sendKey(m, "down")
	m = sendKey(m, "down")
	m = sendKey(m, "down")
	m = sendKey(m, "down")
	m = sendKey(m, "deploy")
	m = sendKey(m, "down")
	m = sendKey(m, "~/.ssh/id_ed25519")
	m = sendKey(m, "enter")

	// Quit: dirty is already cleared by auto-save, so this quits directly.
	m = sendKey(m, "q")

	// Session 2: reopen exactly like the app does.
	cfg2, err := config.LoadOrEmpty(path)
	if err != nil {
		t.Fatal(err)
	}
	m2 := send(New(cfg2, path), tea.WindowSizeMsg{Width: 100, Height: 40})
	if len(m2.(Model).cfg.Accounts) != 1 || m2.(Model).cfg.Accounts[0].ID != "persist-test" {
		t.Fatalf("account lost after reopen: %+v", cfg2.Accounts)
	}
}

// TestQuitConfirmSave tests the dirty-quit path: y saves and quits,
// n quits without saving.
func TestQuitConfirmSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := send(New(&config.Config{}, path), tea.WindowSizeMsg{Width: 100, Height: 40})

	// Force a dirty state (e.g. a failed write earlier).
	mm := m.(Model)
	mm.dirty = true
	m = sendKey(mm, "q")
	mm = m.(Model)
	if !strings.Contains(mm.confirm, "save changes") {
		t.Fatalf("expected save-before-quit prompt, got %q", mm.confirm)
	}

	// y: save (writes empty config) then quit.
	m2, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg, got %T", cmd())
	}
	if m2.(Model).dirty {
		t.Fatal("dirty should be cleared after save-and-quit")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("save-and-quit did not write config: %v", err)
	}

	// n: quits without saving.
	m = send(New(&config.Config{}, path), tea.WindowSizeMsg{Width: 100, Height: 40})
	mm = m.(Model)
	mm.dirty = true
	m = sendKey(mm, "q")
	_, cmd = m.Update(key("n"))
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg after n, got %T", cmd())
	}
}

func TestAddProjectWithToggles(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{ID: "a1", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "key", Username: "u"}}},
	}
	m := send(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), tea.WindowSizeMsg{Width: 100, Height: 40})

	m = sendKey(m, "tab") // projects section
	m = sendKey(m, "n")
	if m.(Model).form == nil {
		t.Fatal("expected project form to open")
	}

	m = sendKey(m, "web")  // id
	m = sendKey(m, "down") // name
	m = sendKey(m, "Web")  // name
	m = sendKey(m, "down") // account (select, 1 option)
	m = sendKey(m, "down") // local root
	m = sendKey(m, "/local/web")
	m = sendKey(m, "down") // remote root
	m = sendKey(m, "/var/www/web")
	m = sendKey(m, "down") // diff mode
	m = sendKey(m, "down") // ignore whitespace
	m = sendKey(m, " ")    // toggle on
	m = sendKey(m, "enter")

	mm := m.(Model)
	if mm.form != nil {
		t.Fatalf("form still open: %q", mm.form.status)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("project not saved: %+v", cfg.Projects)
	}
	p := cfg.Projects[0]
	if p.ID != "web" || p.AccountID != "a1" || p.LocalRoot != "/local/web" || p.RemoteRoot != "/var/www/web" {
		t.Fatalf("project mismatch: %+v", p)
	}
	if !p.TextDiffOptions.IgnoreWhitespace {
		t.Fatal("ignore whitespace toggle not saved")
	}
}

func TestParseProjectPathMappings(t *testing.T) {
	mappings, err := parseMappings("public => /var/www/site; ops/config => /etc/site")
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 2 || mappings[0].LocalPath != "public" || mappings[1].RemotePath != "/etc/site" {
		t.Fatalf("mappings = %+v", mappings)
	}
	if _, err := parseMappings("public /var/www"); err == nil {
		t.Fatal("expected malformed mapping to fail")
	}
}

// sendExec runs a message through Update and executes any returned tea.Cmd
// synchronously, delivering the result back into the model (mimics the
// bubbletea event loop for async work like path completion).
func sendExec(m tea.Model, msg tea.Msg) tea.Model {
	updated, cmd := m.Update(msg)
	for cmd != nil {
		result := cmd()
		if result == nil {
			break
		}
		updated, cmd = updated.Update(result)
	}
	return updated
}

func TestKeyPathCompletion(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{ID: "a", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "key", Username: "u"}}},
	}
	m := send(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), tea.WindowSizeMsg{Width: 110, Height: 40})
	m = sendKey(m, "enter")
	for i := 0; i < 8; i++ {
		m = sendKey(m, "down") // key path field
	}

	// Type a partial path into a temp dir with known entries.
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "keys", "alpha"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "keys", "beta"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "keys", "id_ed25519"), []byte("k"), 0o600)
	for _, r := range filepath.Join(dir, "keys", "id_") {
		m = sendKey(m, string(r))
	}

	m = sendExec(m, key("tab"))
	mm := m.(Model)
	if mm.form == nil {
		t.Fatal("form closed unexpectedly")
	}
	want := filepath.Join(dir, "keys", "id_ed25519")
	if mm.form.popup != nil {
		// several candidates: popup should contain the file
		if len(mm.form.popup) != 1 || mm.form.popup[0] != want {
			t.Fatalf("popup = %v, want [%q]", mm.form.popup, want)
		}
	} else {
		// single candidate: auto-accepted into the field
		if mm.form.Fields[8].Value != want {
			t.Fatalf("field = %q, want %q (status=%q err=%q)", mm.form.Fields[8].Value, want, mm.form.status, mm.form.popupErr)
		}
	}
}

func TestPasswordStoredInSecretsFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{}
	m := send(New(cfg, cfgPath), tea.WindowSizeMsg{Width: 100, Height: 40})

	m = sendKey(m, "n")
	m = sendKey(m, "ftp1")    // id
	m = sendKey(m, "down")    // name
	m = sendKey(m, "FTP One") // name
	m = sendKey(m, "down")    // protocol
	m = sendKey(m, "down")    // host
	m = sendKey(m, "ftp.example.com")
	m = sendKey(m, "down") // port
	m = sendKey(m, "down") // tls
	m = sendKey(m, "down") // auth (key)
	m = sendKey(m, " ")    // cycle key -> agent
	m = sendKey(m, " ")    // cycle agent -> password
	m = sendKey(m, "down") // username
	m = sendKey(m, "deploy")
	m = sendKey(m, "down") // key path (skip)
	m = sendKey(m, "down") // passphrase prompt (skip)
	m = sendKey(m, "down") // password env (skip)
	m = sendKey(m, "down") // password field
	m = sendKey(m, "S3cret!x")
	m = sendKey(m, "enter")

	mm := m.(Model)
	if mm.form != nil {
		t.Fatalf("form still open: %q", mm.form.status)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Auth.Type != "password" {
		t.Fatalf("account not saved: %+v", cfg.Accounts)
	}
	if got := mm.secrets.Accounts["ftp1"].Password; got != "S3cret!x" {
		t.Fatalf("stored password = %q, want S3cret!x", got)
	}

	m = sendKey(m, "s")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "S3cret!x") {
		t.Fatalf("config.yaml must not contain the password:\n%s", data)
	}
	secretsPath := config.SecretsPath(cfgPath)
	secretsData, err := os.ReadFile(secretsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secretsData), "S3cret!x") {
		t.Fatalf("secrets.yaml missing password:\n%s", secretsData)
	}
}

func TestDeleteAccountRemovesSecret(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{ID: "a1", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "password", Username: "u"}}},
	}
	m := send(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), tea.WindowSizeMsg{Width: 100, Height: 40})
	mm := m.(Model)
	mm.secrets.Accounts["a1"] = config.AccountSecret{Password: "hunter2"}
	m = mm

	m = sendKey(m, "d")
	m = sendKey(m, "y")
	mm = m.(Model)
	if len(cfg.Accounts) != 0 {
		t.Fatal("account should be deleted")
	}
	if _, ok := mm.secrets.Accounts["a1"]; ok {
		t.Fatal("stored secret should be deleted with the account")
	}
}

// --- fake remote client for remote listing tests ---

type fakeInfo struct {
	name string
	dir  bool
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

type fakeClient struct{ dirs map[string][]fs.FileInfo }

func (f *fakeClient) Connect() error                        { return nil }
func (f *fakeClient) Close() error                          { return nil }
func (f *fakeClient) Stat(string) (*domain.FileMeta, error) { return nil, nil }
func (f *fakeClient) ReadDir(path string) ([]fs.FileInfo, error) {
	if infos, ok := f.dirs[path]; ok {
		return infos, nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeClient) OpenReader(string) (io.ReadCloser, error)  { return nil, nil }
func (f *fakeClient) OpenWriter(string) (io.WriteCloser, error) { return nil, nil }
func (f *fakeClient) Remove(string) error                       { return nil }
func (f *fakeClient) MkdirAll(string) error                     { return nil }
func (f *fakeClient) FastHash(string) (string, error)           { return "", domain.ErrFastHashUnsupported }

func TestListRemoteDirs(t *testing.T) {
	client := &fakeClient{dirs: map[string][]fs.FileInfo{
		"/var/www": {
			fakeInfo{name: "api-backend", dir: true},
			fakeInfo{name: "api-frontend", dir: true},
			fakeInfo{name: "index.html"},
		},
	}}
	cands, err := listRemoteDirs(client, "/var/www/api")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0] != "/var/www/api-backend/" || cands[1] != "/var/www/api-frontend/" {
		t.Fatalf("unexpected candidates: %v", cands)
	}

	cands, err = listRemoteDirs(client, "/var/www/")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected trailing-slash listing, got %v", cands)
	}

	if _, err := listRemoteDirs(client, "/missing/"); err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestSplitRemote(t *testing.T) {
	cases := []struct{ in, base, last string }{
		{"", "/", ""},
		{"/", "/", ""},
		{"/var/www/", "/var/www", ""},
		{"/var/www/api", "/var/www", "api"},
		{"etc", "", "etc"},
	}
	for _, c := range cases {
		base, last := remote.SplitRemote(c.in)
		if base != c.base || last != c.last {
			t.Fatalf("splitRemote(%q) = (%q, %q), want (%q, %q)", c.in, base, last, c.base, c.last)
		}
	}
}

func TestTasksTabCycle(t *testing.T) {
	cfg := &config.Config{}
	m := send(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), tea.WindowSizeMsg{Width: 100, Height: 40})

	m = sendKey(m, "tab") // projects
	if m.(Model).tab != 1 {
		t.Fatalf("tab = %d, want projects", m.(Model).tab)
	}
	m = sendKey(m, "tab") // tasks
	if m.(Model).tab != 2 {
		t.Fatalf("tab = %d, want tasks", m.(Model).tab)
	}
	m = sendKey(m, "tab") // back to accounts
	if m.(Model).tab != 0 {
		t.Fatalf("tab = %d, want accounts", m.(Model).tab)
	}
	m = sendKey(m, "3") // direct jump
	if m.(Model).tab != 2 {
		t.Fatalf("tab = %d, want tasks after 3", m.(Model).tab)
	}
}

func taskTestConfig() *config.Config {
	return &config.Config{
		Accounts: []config.Account{
			{ID: "kiosks", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "key", Username: "u"}},
			{ID: "root", Protocol: "sftp", Host: "h", Port: 22, Auth: config.Auth{Type: "key", Username: "r"}},
		},
	}
}

func TestAddTaskAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := taskTestConfig()
	m := send(New(cfg, path), tea.WindowSizeMsg{Width: 120, Height: 40})

	m = sendKey(m, "3") // tasks tab
	m = sendKey(m, "n")
	if m.(Model).form == nil {
		t.Fatal("expected task form to open")
	}

	m = sendKey(m, "deploy") // id
	m = sendKey(m, "down")   // name
	m = sendKey(m, "Deploy stuff")
	m = sendKey(m, "down") // on error (leave stop)
	m = sendKey(m, "down") // steps
	m = sendKey(m, "enter")
	if m.(Model).steps == nil {
		t.Fatal("expected step editor to open")
	}

	m = sendKey(m, "kiosks") // account
	m = sendKey(m, "tab")    // exact match moves to workdir
	m = sendKey(m, "apiv2")
	m = sendKey(m, "tab") // run column
	m = sendKey(m, "make build")
	m = sendKey(m, "tab") // shell column
	m = sendKey(m, "bash -lc")
	m = sendKey(m, "ctrl+n") // second row
	m = sendKey(m, "root")
	m = sendKey(m, "tab") // exact match moves to workdir (leave empty)
	m = sendKey(m, "tab") // run column
	m = sendKey(m, "supervisorctl restart apiv2")
	m = sendKey(m, "enter") // apply

	mm := m.(Model)
	if mm.steps != nil {
		t.Fatalf("step editor still open: %q", mm.steps.status)
	}
	if got := stepSummary([]config.TaskStep{{}, {}}); got != "2 steps" {
		t.Fatalf("stepSummary = %q", got)
	}

	m = sendKey(m, "up") // leave the Steps row so enter saves
	m = sendKey(m, "enter")

	mm = m.(Model)
	if mm.form != nil {
		t.Fatalf("form still open: %q", mm.form.status)
	}
	if len(cfg.Tasks) != 1 {
		t.Fatalf("task not saved: %+v", cfg.Tasks)
	}
	got := cfg.Tasks[0]
	if got.ID != "deploy" || len(got.Steps) != 2 {
		t.Fatalf("task mismatch: %+v", got)
	}
	if got.Steps[0].AccountID != "kiosks" || got.Steps[0].Workdir != "apiv2" || got.Steps[0].Run != "make build" || got.Steps[0].Shell != "bash -lc" {
		t.Fatalf("step 1 mismatch: %+v", got.Steps[0])
	}
	if got.Steps[1].AccountID != "root" || got.Steps[1].Workdir != "" || got.Steps[1].Run != "supervisorctl restart apiv2" {
		t.Fatalf("step 2 mismatch: %+v", got.Steps[1])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "deploy") || !strings.Contains(string(data), "supervisorctl") {
		t.Fatalf("config file missing task data:\n%s", data)
	}
}

func TestAddTaskRejectsUnknownAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := taskTestConfig()
	m := send(New(cfg, path), tea.WindowSizeMsg{Width: 120, Height: 40})

	m = sendKey(m, "3")
	m = sendKey(m, "n")
	m = sendKey(m, "bad")
	m = sendKey(m, "down") // name
	m = sendKey(m, "down") // on error
	m = sendKey(m, "down") // steps
	m = sendKey(m, "enter")
	m = sendKey(m, "ghost") // unknown account (no completion match)
	m = sendKey(m, "tab")
	m = sendKey(m, "shift+tab") // shell column
	m = sendKey(m, "shift+tab") // run column
	m = sendKey(m, "doom")
	m = sendKey(m, "enter") // apply (editor-level fields are filled)
	m = sendKey(m, "up")
	m = sendKey(m, "enter") // save attempt

	mm := m.(Model)
	if mm.form == nil {
		t.Fatal("form should stay open when the account is unknown")
	}
	if len(cfg.Tasks) != 0 {
		t.Fatalf("invalid task must not be saved: %+v", cfg.Tasks)
	}
}

func TestDeleteTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := taskTestConfig()
	cfg.Tasks = []config.Task{{ID: "gone", Steps: []config.TaskStep{{AccountID: "kiosks", Run: "x"}}}}
	m := send(New(cfg, path), tea.WindowSizeMsg{Width: 120, Height: 40})

	m = sendKey(m, "3")
	m = sendKey(m, "d")
	mm := m.(Model)
	if mm.confirm == "" {
		t.Fatal("expected delete confirmation")
	}
	m = sendKey(m, "y")
	if len(cfg.Tasks) != 0 {
		t.Fatalf("task not deleted: %+v", cfg.Tasks)
	}
}

func TestDeleteAccountWarnsAboutTasks(t *testing.T) {
	cfg := taskTestConfig()
	cfg.Tasks = []config.Task{{ID: "deploy", Steps: []config.TaskStep{{AccountID: "kiosks", Run: "x"}}}}
	m := send(New(cfg, filepath.Join(t.TempDir(), "config.yaml")), tea.WindowSizeMsg{Width: 120, Height: 40})

	m = sendKey(m, "d") // delete kiosks (cursor 0)
	if got := m.(Model).confirm; !strings.Contains(got, "task:deploy") {
		t.Fatalf("confirm should name the task ref: %q", got)
	}
}

// manyAccounts builds an account list longer than the terminal.
func manyAccounts(n int) *config.Config {
	cfg := &config.Config{}
	for i := 0; i < n; i++ {
		cfg.Accounts = append(cfg.Accounts, config.Account{
			ID: fmt.Sprintf("acc%02d", i), Name: "Account", Protocol: "sftp",
			Host: "host.example.com", Port: 22, Auth: config.Auth{Username: "deploy"},
		})
	}
	return cfg
}

// TestListScrollsWithCursor covers the pre-existing bug where the config list
// rendered from row 0: with more entries than rows the cursor walked off the
// bottom of the screen and the selected entry was invisible.
func TestListScrollsWithCursor(t *testing.T) {
	var m tea.Model = New(manyAccounts(20), filepath.Join(t.TempDir(), "config.yaml"))
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 10})

	for i := 0; i < 19; i++ {
		mm := m.(Model)
		id := mm.cfg.Accounts[mm.cursor].ID
		if view := mm.View(); !strings.Contains(view, "▸ "+id) {
			t.Fatalf("cursor row %q not highlighted at cursor %d:\n%s", id, mm.cursor, view)
		}
		m = sendKey(m, "down")
	}

	mm := m.(Model)
	if mm.cursor != 19 {
		t.Fatalf("cursor = %d, want 19", mm.cursor)
	}
	view := mm.View()
	if !strings.Contains(view, "▸ acc19") {
		t.Fatalf("last account must be visible:\n%s", view)
	}
	if !strings.Contains(view, "↑ 15 more") {
		t.Fatalf("rows hidden above should be counted:\n%s", view)
	}

	// A shrink must re-window the list around the cursor.
	m = send(mm, tea.WindowSizeMsg{Width: 100, Height: 6})
	mm = m.(Model)
	if view := mm.View(); !strings.Contains(view, "▸ acc19") {
		t.Fatalf("cursor row missing after shrink:\n%s", view)
	}

	// ... and the top of the list counts the rows below it.
	m = sendKey(m, "home")
	view = m.(Model).View()
	if !strings.Contains(view, "▸ acc00") {
		t.Fatalf("first account must be visible after home:\n%s", view)
	}
	if !strings.Contains(view, "↓ 19 more") {
		t.Fatalf("rows hidden below should be counted:\n%s", view)
	}
}
