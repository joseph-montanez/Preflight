package remotebrowser

import (
	"errors"
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

	tea "github.com/charmbracelet/bubbletea"
)

type fakeInfo struct {
	name string
	dir  bool
}

func (f fakeInfo) Name() string { return f.name }
func (f fakeInfo) Size() int64  { return 0 }
func (f fakeInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

// fakeRemote serves a static remote tree for browser tests.
type fakeRemote struct {
	tree map[string][]fakeInfo
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{tree: map[string][]fakeInfo{
		"/":        {{name: "var", dir: true}, {name: "etc", dir: true}},
		"/var":     {{name: "www", dir: true}},
		"/var/www": {{name: "api-backend", dir: true}},
		"/var/www/api-backend": {
			{name: "assets", dir: true},
			{name: "index.php", dir: false},
			{name: ".env", dir: false}, // a file sync would ignore
		},
		"/var/www/api-backend/assets": {{name: "logo.png", dir: false}},
		"/etc":                        {{name: "nginx", dir: true}},
		"/etc/nginx":                  {{name: "conf.d", dir: true}},
		"/etc/nginx/conf.d":           {{name: "default.conf", dir: false}},
	}}
}

func (f *fakeRemote) Connect() error { return nil }
func (f *fakeRemote) Close() error   { return nil }
func (f *fakeRemote) Stat(p string) (*domain.FileMeta, error) {
	return &domain.FileMeta{Size: 1, Exists: true}, nil
}
func (f *fakeRemote) ReadDir(p string) ([]fs.FileInfo, error) {
	entries, ok := f.tree[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	return out, nil
}
func (f *fakeRemote) OpenReader(string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRemote) OpenWriter(string) (io.WriteCloser, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRemote) Remove(string) error   { return nil }
func (f *fakeRemote) MkdirAll(string) error { return nil }
func (f *fakeRemote) FastHash(string) (string, error) {
	return "", domain.ErrFastHashUnsupported
}

var _ domain.RemoteClient = (*fakeRemote)(nil)

func testMappings() []config.PathMapping {
	return []config.PathMapping{
		{LocalPath: "public", RemotePath: "/var/www/api-backend"},
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func sendKey(m tea.Model, s string) tea.Model {
	updated, _ := m.Update(key(s))
	return updated
}

// altKey builds an alt-modified letter key (ESC+letter), the reliable
// modifier form bubbletea parses.
func altKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

func newModel() Model {
	return New(newFakeRemote(), testMappings(), filepath.FromSlash("/home/dev/code/api-backend"), "")
}

func TestVirtualRootListsMappings(t *testing.T) {
	m := newModel()
	m.width, m.height = 80, 24
	if !m.atRoot() {
		t.Fatal("browser should start at the virtual mapping root")
	}
	if len(m.entries) != 1 || m.entries[0].name != "/var/www/api-backend" {
		t.Fatalf("entries = %v, want the mapping root", m.entries)
	}
	view := m.View()
	if !strings.Contains(view, "public/") {
		t.Fatalf("virtual root should label the local mapping: %q", view)
	}
}

func TestNavigateAndMarkFile(t *testing.T) {
	// virtual root rows: [select], .., /var/www/api-backend
	m := sendKey(newModel(), "down") // .. row
	m = sendKey(m, "down")           // mapping root
	m = sendKey(m, "enter")          // into /var/www/api-backend
	if m.(Model).current != "/var/www/api-backend" {
		t.Fatalf("current = %q, want /var/www/api-backend", m.(Model).current)
	}
	// rows now: [select], .., assets/, .env, index.php (case-insensitive sort)
	m = sendKey(m, "down") // ..
	m = sendKey(m, "down") // assets/
	m = sendKey(m, "down") // .env
	m = sendKey(m, "down") // index.php
	m = sendKey(m, " ")    // mark index.php
	mm := m.(Model)
	if !mm.marked["/var/www/api-backend/index.php"] {
		t.Fatalf("marked = %v, want index.php", mm.marked)
	}
	// Files sync would ignore are visible here.
	mm.width, mm.height = 80, 24
	mm.status = ""
	if !strings.Contains(mm.View(), ".env") {
		t.Fatal("ignored files (.env) must be listed in the remote browser")
	}
}

func TestFinishResolvesMappedAndUnmapped(t *testing.T) {
	mm := newModel()
	mm.marked["/var/www/api-backend/index.php"] = true // mapped
	mm.marked["/etc/nginx/conf.d/default.conf"] = true // unmapped
	m := sendKey(mm, "s")
	roots, ok := m.(Model).Selected()
	if !ok || len(roots) != 2 {
		t.Fatalf("Selected() = (%v, %v), want 2 roots", roots, ok)
	}
	byPath := map[string]DownloadRoot{}
	for _, r := range roots {
		byPath[r.RemotePath] = r
	}
	mapped := byPath["/var/www/api-backend/index.php"]
	wantMapped := filepath.Join("/home/dev/code/api-backend", "public", "index.php")
	if mapped.LocalPath != wantMapped {
		t.Fatalf("mapped LocalPath = %q, want %q", mapped.LocalPath, wantMapped)
	}
	unmapped := byPath["/etc/nginx/conf.d/default.conf"]
	if unmapped.LocalPath != "" {
		t.Fatalf("unmapped LocalPath = %q, want empty", unmapped.LocalPath)
	}
}

func TestMarkDirRecursively(t *testing.T) {
	mm := newModel()
	mm.marked["/var/www/api-backend/assets"] = true
	m := sendKey(mm, "s")
	roots, ok := m.(Model).Selected()
	if !ok || len(roots) != 1 {
		t.Fatalf("Selected() = (%v, %v), want 1 root", roots, ok)
	}
	want := filepath.Join("/home/dev/code/api-backend", "public", "assets")
	if roots[0].LocalPath != want {
		t.Fatalf("LocalPath = %q, want %q", roots[0].LocalPath, want)
	}
}

func TestFinishWithoutMarkShowsHint(t *testing.T) {
	m := sendKey(newModel(), "s")
	mm := m.(Model)
	if mm.chosen {
		t.Fatal("finish with nothing marked must not confirm")
	}
	if !strings.Contains(mm.status, "mark at least one") {
		t.Fatalf("status = %q, want marking hint", mm.status)
	}
}

func TestEnterOnFileMarksIt(t *testing.T) {
	mm := newModel()
	mm.current = "/var/www/api-backend"
	mm.load()
	// rows: [select], .., assets/, .env, index.php → cursor 4 = index.php
	mm.cursor = 4
	m := sendKey(mm, "enter")
	if !m.(Model).marked["/var/www/api-backend/index.php"] {
		t.Fatal("Enter on a file should toggle its mark")
	}
}

func TestUpLinksRootAndVirtualRoot(t *testing.T) {
	m := sendKey(newModel(), "down") // .. row at virtual root
	m = sendKey(m, "down")           // mapping root
	m = sendKey(m, "enter")          // into /var/www/api-backend
	m = sendKey(m, "left")           // up to /var/www
	if m.(Model).current != "/var/www" {
		t.Fatalf("current = %q, want /var/www", m.(Model).current)
	}
	m = sendKey(m, "left") // up to /var
	m = sendKey(m, "left") // up to /
	m = sendKey(m, "left") // up to the virtual mapping root again
	if !m.(Model).atRoot() {
		t.Fatalf("current = %q, want the virtual root", m.(Model).current)
	}
}

func TestVirtualRootNoDoubleSlash(t *testing.T) {
	// Configs may spell remote_path with a trailing slash; the virtual
	// root must show a single slash per entry.
	mappings := []config.PathMapping{
		{LocalPath: "public", RemotePath: "/var/www/api-backend/"},
		{LocalPath: "hub", RemotePath: "/var/www/api-backend/hub"},
	}
	m := New(newFakeRemote(), mappings, filepath.FromSlash("/home/dev/code"), "")
	m.width, m.height = 100, 24
	view := m.View()
	if strings.Contains(view, "//") {
		t.Fatalf("virtual root must not render double slashes: %q", view)
	}
	if !strings.Contains(view, "/var/www/api-backend/  →  public/") {
		t.Fatalf("mapping root label wrong: %q", view)
	}
}

func TestListScrollsWithCursor(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	mm := m
	mm.width, mm.height = 80, 8 // vis = max(3, 8-5) = 3 rows
	// rows: [select, .., assets/, .env, index.php] = 5 total
	if mm.rowCount() != 5 {
		t.Fatalf("rowCount = %d, want 5", mm.rowCount())
	}
	// Move to the last row (index.php, row 4); the window must scroll.
	m2 := sendKey(mm, "end")
	mm2 := m2.(Model)
	if mm2.offset != 2 {
		t.Fatalf("offset = %d, want 2 (cursor %d at the last row)", mm2.offset, mm2.cursor)
	}
	view := mm2.View()
	if !strings.Contains(view, "index.php") {
		t.Fatalf("scrolled view must show the cursor row: %q", view)
	}
	// Going back up re-clamps the window.
	m3 := sendKey(mm2, "home")
	if m3.(Model).offset != 0 {
		t.Fatalf("offset = %d, want 0 after home", m3.(Model).offset)
	}
}

func TestPickRemoteDir(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/")
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = sized.(Model)
	if m.pickDir != true {
		t.Fatal("pick mode should be active")
	}
	if m.current != "/" {
		t.Fatalf("picker should start at /, got %q", m.current)
	}
	if !strings.Contains(m.View(), "Select upload destination") {
		t.Fatal("view header should say Select upload destination")
	}
	if !strings.Contains(m.View(), "(select this folder)") {
		t.Fatal("row 0 should be (select this folder)")
	}
	// Enter at row 0 finishes with "/".
	var updated tea.Model = m
	updated, _ = updated.Update(key("enter"))
	final := updated.(Model)
	if dir, ok := final.PickedDir(); !ok || dir != "/" {
		t.Fatalf("PickedDir = %q, %v; want /, true", dir, ok)
	}
}

func TestPickRemoteDirDescendAndPick(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/")
	// var → www → api-backend (etc sorts first at /), then pick the
	// current dir.
	for _, k := range []string{"down", "down", "down", "enter", "down", "down", "enter", "down", "down", "enter"} {
		m = sendKey(m, k).(Model)
	}
	if m.current != "/var/www/api-backend" {
		t.Fatalf("current = %q, want /var/www/api-backend", m.current)
	}
	m = sendKey(m, "enter").(Model) // row 0: select this folder
	if dir, ok := m.PickedDir(); !ok || dir != "/var/www/api-backend" {
		t.Fatalf("PickedDir = %q, %v; want /var/www/api-backend, true", dir, ok)
	}
}

func TestPickRemoteDirNoMarking(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/")
	m = sendKey(m, " ").(Model)
	if len(m.marked) != 0 {
		t.Fatal("Space must not mark in pick mode")
	}
	if !strings.Contains(m.status, "single destination") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestPickRemoteDirFileRejected(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/var/www/api-backend")
	// index.php is a file: Enter on it must not finish or mark.
	m = sendKey(m, "down").(Model) // ..
	m = sendKey(m, "down").(Model) // assets (dir)
	m = sendKey(m, "down").(Model) // index.php
	if m.current != "/var/www/api-backend" {
		t.Fatalf("current moved unexpectedly: %q", m.current)
	}
	m = sendKey(m, "enter").(Model)
	if _, ok := m.PickedDir(); ok {
		t.Fatal("picking a file must not finish")
	}
	if !strings.Contains(m.status, "folder, not a file") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestPickRemoteDirStaysAtRoot(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/")
	m = sendKey(m, "left").(Model)
	if m.current != "/" {
		t.Fatalf("up at / should stay at /, got %q", m.current)
	}
}

func TestFilterSlashOpensInput(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	m.width, m.height = 80, 24
	m = sendKey(m, "/").(Model)
	if !m.filterOn {
		t.Fatal("/ should open the name filter")
	}
	if !strings.Contains(m.View(), "filter names") {
		t.Fatalf("filter input missing from view: %q", m.View())
	}
}

func TestFilterNarrowsCurrentFolder(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	m.width, m.height = 80, 24
	m = sendKey(m, "/").(Model)
	for _, r := range []string{"i", "n", "d"} {
		m = sendKey(m, r).(Model)
	}
	// rows: [select], .., index.php
	if m.rowCount() != 3 {
		t.Fatalf("rowCount = %d, want 3 with only index.php matching", m.rowCount())
	}
	view := m.View()
	if !strings.Contains(view, "index.php") || strings.Contains(view, "assets") || strings.Contains(view, ".env") {
		t.Fatalf("filtered view wrong: %q", view)
	}
	// Move onto the filtered file and Enter marks it.
	m = sendKey(m, "down").(Model) // ..
	m = sendKey(m, "down").(Model) // index.php
	m = sendKey(m, "enter").(Model)
	if !m.marked["/var/www/api-backend/index.php"] {
		t.Fatal("Enter on the filtered file should toggle its mark")
	}
}

func TestFilterMatchesDirectoriesAndClearsOnDescend(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	for _, r := range []string{"/", "a", "s", "s"} {
		m = sendKey(m, r).(Model)
	}
	if m.rowCount() != 3 { // [select], .., assets/
		t.Fatalf("rowCount = %d, want 3", m.rowCount())
	}
	m = sendKey(m, "down").(Model) // ..
	m = sendKey(m, "down").(Model) // assets/
	m = sendKey(m, "enter").(Model)
	if m.current != "/var/www/api-backend/assets" {
		t.Fatalf("current = %q, want assets", m.current)
	}
	if m.filterOn {
		t.Fatal("descending should clear the filter")
	}
}

func TestFilterEscRestoresListing(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	for _, r := range []string{"/", "i", "n", "d"} {
		m = sendKey(m, r).(Model)
	}
	if m.rowCount() != 3 {
		t.Fatalf("filtered rowCount = %d, want 3", m.rowCount())
	}
	m = sendKey(m, "esc").(Model)
	if m.filterOn {
		t.Fatal("esc should close the filter")
	}
	if m.rowCount() != 5 {
		t.Fatalf("rowCount = %d, want the full 5 rows back", m.rowCount())
	}
}

func TestFilterNoMatches(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	m.width, m.height = 80, 24
	for _, r := range []string{"/", "z", "z", "z"} {
		m = sendKey(m, r).(Model)
	}
	if m.rowCount() != 2 { // just [select] and ..
		t.Fatalf("rowCount = %d, want 2", m.rowCount())
	}
	if !strings.Contains(m.View(), "no matches") {
		t.Fatalf("view should report no matches: %q", m.View())
	}
	m = sendKey(m, "enter").(Model) // row 0: finish with nothing marked
	if m.chosen {
		t.Fatal("enter with no matches must not finish the flow")
	}
	if !strings.Contains(m.status, "mark at least one") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestFilterQTypesInsteadOfQuitting(t *testing.T) {
	m := newModel()
	m.current = "/var/www/api-backend"
	m.load()
	m = sendKey(m, "/").(Model)
	m = sendKey(m, "q").(Model)
	if !m.filterOn || m.input.Value() != "q" {
		t.Fatalf("q should type into the filter (filterOn=%v, value=%q)", m.filterOn, m.input.Value())
	}
	if m.rowCount() != 2 { // nothing in this folder matches "q"
		t.Fatalf("rowCount = %d, want 2", m.rowCount())
	}
}

func TestFilterEmptyQueryShowsEverything(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/var/www/api-backend")
	m.width, m.height = 80, 24
	m = sendKey(m, "/").(Model)
	if m.rowCount() != 5 {
		t.Fatalf("empty filter should show the full listing (rowCount = %d)", m.rowCount())
	}
	m = sendKey(m, "enter").(Model) // row 0: select this folder
	if dir, ok := m.PickedDir(); !ok || dir != "/var/www/api-backend" {
		t.Fatalf("PickedDir = %q, %v; want the current folder", dir, ok)
	}
}

func TestFilterPickModeFileRejected(t *testing.T) {
	m := PickRemoteDir(newFakeRemote(), "/var/www/api-backend")
	m.width, m.height = 80, 24
	for _, r := range []string{"/", "i", "n", "d", "e", "x"} {
		m = sendKey(m, r).(Model)
	}
	m = sendKey(m, "down").(Model) // ..
	m = sendKey(m, "down").(Model) // index.php
	m = sendKey(m, "enter").(Model)
	if _, ok := m.PickedDir(); ok {
		t.Fatal("picking a file must not finish")
	}
	if !strings.Contains(m.status, "folder, not a file") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestFilterAtVirtualRoot(t *testing.T) {
	m := newModel()
	m.width, m.height = 80, 24
	for _, r := range []string{"/", "v", "a", "r"} {
		m = sendKey(m, r).(Model)
	}
	if m.rowCount() != 3 { // [select], .., mapping root
		t.Fatalf("rowCount = %d, want 3", m.rowCount())
	}
	m = sendKey(m, "down").(Model) // ..
	m = sendKey(m, "down").(Model) // mapping root
	m = sendKey(m, "enter").(Model)
	if m.current != "/var/www/api-backend" {
		t.Fatalf("current = %q, want /var/www/api-backend", m.current)
	}
}

// TestWindowShrinkKeepsCursorVisible covers the resize path: a shorter window
// must scroll the listing so the cursor row stays on screen.
func TestWindowShrinkKeepsCursorVisible(t *testing.T) {
	fake := newFakeRemote()
	entries := make([]fakeInfo, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, fakeInfo{name: fmt.Sprintf("f%02d.txt", i)})
	}
	fake.tree["/var/www/api-backend"] = entries

	m := New(fake, testMappings(), filepath.FromSlash("/home/dev/code/api-backend"), "")
	m.width, m.height = 100, 24
	m.current = "/var/www/api-backend"
	m.load()
	for i := 0; i < 15; i++ {
		m = sendKey(m, "down").(Model)
	}

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
	shrunk := updated.(Model)
	vis := max(3, shrunk.height-5)
	if shrunk.cursor < shrunk.offset || shrunk.cursor >= shrunk.offset+vis {
		t.Fatalf("cursor %d outside window [%d,%d) after shrink", shrunk.cursor, shrunk.offset, shrunk.offset+vis)
	}
	name := shrunk.entries[shrunk.cursor-2].name
	if !strings.Contains(shrunk.View(), name) {
		t.Fatalf("cursor row missing from the shrunk view:\n%s", shrunk.View())
	}
}
