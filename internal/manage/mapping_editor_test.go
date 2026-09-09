package manage

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"preflight/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func projectModelAtMappings(t *testing.T, localRoot string) Model {
	t.Helper()
	cfg := &config.Config{Accounts: []config.Account{{
		ID: "prod", Protocol: "sftp", Host: "example.test", Port: 22,
		Auth: config.Auth{Type: "key", Username: "deploy"},
	}}}
	m := New(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	m.width, m.height = 120, 40
	m.openProjectForm(-1)
	m.form.setValue(m.form.byTagIdx(tagID), "site")
	m.form.setValue(m.form.byTagIdx(tagLocalRoot), localRoot)
	m.form.setValue(m.form.byTagIdx(tagRemoteRoot), "/var/www/default")
	m.form.Cursor = m.form.byTagIdx(tagMappings)
	m.form.focus()
	return m
}

func TestEnterOnPathMappingsOpensDedicatedEditor(t *testing.T) {
	m := projectModelAtMappings(t, t.TempDir())
	updated, _ := m.Update(key("enter"))
	m = updated.(Model)
	if m.mappings == nil || m.form == nil {
		t.Fatal("Enter should open the mapping editor without closing the project form")
	}
	view := m.View()
	for _, expected := range []string{"Project path mappings", "LOCAL", "REMOTE", "Tab complete"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("mapping view missing %q:\n%s", expected, view)
		}
	}
}

func TestMappingLocalCompletionReturnsProjectRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "public", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := projectModelAtMappings(t, root)
	m.openMappingEditor()
	m.mappings.setCurrentValue("pub")

	updated := sendExec(m, key("tab")).(Model)
	if updated.mappings.currentValue() != "public/" {
		t.Fatalf("completed local mapping = %q, want public/", updated.mappings.currentValue())
	}
	if strings.Contains(updated.mappings.currentValue(), filepath.ToSlash(root)) {
		t.Fatal("local completion must remain relative to Local root")
	}
}

func TestMappingEditorAddsMultipleMappingsAndSavesProject(t *testing.T) {
	root := t.TempDir()
	m := projectModelAtMappings(t, root)
	m.openMappingEditor()

	m.mappings.setCurrentValue("public")
	m.mappings.setColumn(1)
	m.mappings.setCurrentValue("/var/www/site")
	m.mappings.addRow()
	m.mappings.setCurrentValue("deploy/config")
	m.mappings.setColumn(1)
	m.mappings.setCurrentValue("/etc/site")

	updated, _ := m.Update(key("enter"))
	m = updated.(Model)
	if m.mappings != nil {
		t.Fatalf("mapping editor should close after apply: %q", m.mappings.status)
	}
	parsed, err := parseMappings(m.form.byTag(tagMappings).Value)
	if err != nil || len(parsed) != 2 {
		t.Fatalf("project form mappings = %+v, err=%v", parsed, err)
	}
	if got := mappingSummary(m.form.byTag(tagMappings).Value); got != "2 mappings" {
		t.Fatalf("mapping summary = %q", got)
	}

	// Move off the editor field and save the parent project form.
	m.form.move(-1)
	updated, _ = m.Update(key("enter"))
	m = updated.(Model)
	if m.form != nil {
		t.Fatalf("project form still open: %q", m.form.status)
	}
	if len(m.cfg.Projects) != 1 || len(m.cfg.Projects[0].PathMappings) != 2 {
		t.Fatalf("saved project mappings = %+v", m.cfg.Projects)
	}
	if m.cfg.Projects[0].RemoteRoot != "" {
		t.Fatalf("default remote should be cleared when mappings exist: %+v", m.cfg.Projects[0])
	}
}

func TestMappingRemoteCompletionUsesLiveAccountCompleter(t *testing.T) {
	m := projectModelAtMappings(t, t.TempDir())
	client := &fakeClient{dirs: map[string][]fs.FileInfo{
		"/var/www": {
			fakeInfo{name: "api-backend", dir: true},
			fakeInfo{name: "api-frontend", dir: true},
		},
	}}
	m.completer.client = client
	m.completer.account = &m.cfg.Accounts[0]
	m.openMappingEditor()
	m.mappings.setColumn(1)
	m.mappings.setCurrentValue("/var/www/api")

	updated := sendExec(m, key("tab")).(Model)
	if len(updated.mappings.popup) != 2 {
		t.Fatalf("remote completion popup = %v", updated.mappings.popup)
	}
	if updated.mappings.popup[0] != "/var/www/api-backend/" {
		t.Fatalf("unexpected remote completion: %v", updated.mappings.popup)
	}
}

func TestMappingEditorCanDeleteLastRow(t *testing.T) {
	editor := newMappingEditor("C:/project", []config.PathMapping{{LocalPath: "public", RemotePath: "/var/www"}}, nil)
	if _, err := editor.Update(tea.KeyMsg{Type: tea.KeyCtrlD}); err != nil {
		t.Fatal(err)
	}
	if len(editor.rows) != 0 {
		t.Fatalf("rows = %d, want zero after deleting the last mapping", len(editor.rows))
	}
	if !strings.Contains(editor.View(100, 30), "No path mappings") {
		t.Fatalf("empty-state message missing:\n%s", editor.View(100, 30))
	}
	if _, err := editor.Update(key("enter")); err != errMappingsSaved {
		t.Fatalf("saving zero mappings returned %v, want errMappingsSaved", err)
	}
	if editor.result != nil {
		t.Fatalf("zero-mapping result = %+v, want nil", editor.result)
	}
}

func TestMappingCompletionWindowFollowsSelectionAndUsesWidth(t *testing.T) {
	editor := newMappingEditor("C:/project", []config.PathMapping{{LocalPath: "public"}}, nil)
	for i := 0; i < 18; i++ {
		editor.popup = append(editor.popup, fmt.Sprintf("folder-%02d/", i))
	}
	editor.popupAt = 14
	popup := editor.renderPopup(72, 7)
	if !strings.Contains(popup, "▸ folder-14/") {
		t.Fatalf("selected item should remain visible:\n%s", popup)
	}
	if !strings.Contains(popup, "of 18") {
		t.Fatalf("popup should report its visible range:\n%s", popup)
	}
	if lipgloss.Width(popup) < 72 {
		t.Fatalf("popup width = %d, want at least 72", lipgloss.Width(popup))
	}
}

func TestMappingViewFillsHeightAndSeparatesRowNumber(t *testing.T) {
	editor := newMappingEditor("C:/project", []config.PathMapping{{LocalPath: "kiosks/public_html", RemotePath: "/home/kiosks/public_html"}}, nil)
	view := editor.View(120, 40)
	if got := lipgloss.Height(view); got < 38 {
		t.Fatalf("mapping view height = %d, want at least 38", got)
	}
	if !strings.Contains(view, "1 kiosks/public_html") {
		t.Fatalf("row number is not separated from local path:\n%s", view)
	}
}
