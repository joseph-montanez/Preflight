package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"preflight/internal/domain"
	"preflight/internal/syncer"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func item(path string, action domain.ActionType) *domain.SyncFileItem {
	return &domain.SyncFileItem{RelativePath: path, Action: action, DiffDetails: &domain.DiffResult{Summary: "differs"}}
}

func newModel(items []*domain.SyncFileItem) Model {
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, nil, nil)
	m.items = items
	m.rebuildFiltered()
	m.refreshDiff()
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func sendKey(m tea.Model, s string) tea.Model {
	updated, _ := m.Update(key(s))
	return updated
}

func mixedItems() []*domain.SyncFileItem {
	return []*domain.SyncFileItem{
		item("same.txt", domain.ActionSkip),
		item("new.txt", domain.ActionUpload),
		item("gone.txt", domain.ActionDownload),
	}
}

func TestFilterDefaultPendingOnly(t *testing.T) {
	m := newModel(mixedItems())
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 (pending only)", len(m.filtered))
	}
	if m.items[m.filtered[0]].RelativePath != "new.txt" {
		t.Fatalf("first filtered = %q, want new.txt", m.items[m.filtered[0]].RelativePath)
	}
}

func TestFilterToggleShowsAll(t *testing.T) {
	m := newModel(mixedItems())
	m = sendKey(m, "f").(Model)
	if len(m.filtered) != 3 {
		t.Fatalf("filtered after f = %d, want 3", len(m.filtered))
	}
	m = sendKey(m, "f").(Model)
	if len(m.filtered) != 2 {
		t.Fatalf("filtered after second f = %d, want 2", len(m.filtered))
	}
}

func TestSkipStaysVisibleAndCanBeReversed(t *testing.T) {
	m := newModel(mixedItems())
	// cursor 0 = new.txt (upload); pressing s keeps it selected and visible.
	m = sendKey(m, "s").(Model)
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(m.filtered))
	}
	if current := m.currentItem(); current == nil || current.RelativePath != "new.txt" || current.Action != domain.ActionSkip {
		t.Fatalf("skipped item is no longer selected: %+v", current)
	}

	m = sendKey(m, "u").(Model)
	if current := m.currentItem(); current == nil || current.RelativePath != "new.txt" || current.Action != domain.ActionUpload {
		t.Fatalf("skip was not reversed: %+v", current)
	}
	if len(m.filtered) != 2 {
		t.Fatalf("filtered after reversal = %d, want 2", len(m.filtered))
	}
}

func TestSkipAllMarksOnlyChangedFiles(t *testing.T) {
	m := newModel(mixedItems())
	// Before skip all: 2 items pending (new.txt, gone.txt), same.txt is skip
	// because it is identical.
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(m.filtered))
	}
	// Press 'S' (Shift+S)
	m = sendKey(m, "S").(Model)
	for _, it := range m.items {
		if it.Action != domain.ActionSkip {
			t.Fatalf("item %s action = %v, want ActionSkip", it.RelativePath, it.Action)
		}
	}
	for _, name := range []string{"new.txt", "gone.txt"} {
		if _, ok := m.retainedSkips[itemNamed(m, name)]; !ok {
			t.Fatalf("changed item %s should be in retainedSkips", name)
		}
	}
	if _, ok := m.retainedSkips[itemNamed(m, "same.txt")]; ok {
		t.Fatalf("identical item same.txt must not be retained")
	}
	// The skipped diffs stay visible; the identical file stays hidden.
	if len(m.filtered) != 2 {
		t.Fatalf("filtered after skipAll = %d, want 2", len(m.filtered))
	}
	for _, idx := range m.filtered {
		if m.items[idx].RelativePath == "same.txt" {
			t.Fatalf("unchanged file became visible after skipAll")
		}
	}
}

func TestSkipAllWithNoDiffs(t *testing.T) {
	m := newModel([]*domain.SyncFileItem{item("same.txt", domain.ActionSkip)})
	m = sendKey(m, "S").(Model)
	if len(m.retainedSkips) != 0 {
		t.Fatalf("retainedSkips = %d, want 0", len(m.retainedSkips))
	}
	if len(m.filtered) != 0 {
		t.Fatalf("filtered = %d, want 0", len(m.filtered))
	}
}

func itemNamed(m Model, path string) *domain.SyncFileItem {
	for _, it := range m.items {
		if it.RelativePath == path {
			return it
		}
	}
	return nil
}

func TestDisplayPathNoTrailingSlash(t *testing.T) {
	tests := []struct {
		name     string
		scope    string
		relPath  string
		expected string
	}{
		{
			name:     "single file with empty relPath",
			scope:    "api.php",
			relPath:  "",
			expected: "api.php",
		},
		{
			name:     "single file with trailing slash in scope and empty relPath",
			scope:    "api.php/",
			relPath:  "",
			expected: "api.php",
		},
		{
			name:     "folder scope with file relPath",
			scope:    "public",
			relPath:  "api.php",
			expected: "public/api.php",
		},
		{
			name:     "folder scope with trailing slash and relPath with leading/trailing slash",
			scope:    "public/",
			relPath:  "/api.php/",
			expected: "public/api.php",
		},
		{
			name:     "empty scope with file relPath",
			scope:    "",
			relPath:  "api.php",
			expected: "api.php",
		},
		{
			name:     "empty scope with trailing slash relPath",
			scope:    "",
			relPath:  "api.php/",
			expected: "api.php",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := &domain.SyncFileItem{
				Scope:        tt.scope,
				RelativePath: tt.relPath,
			}
			got := displayPath(it)
			if got != tt.expected {
				t.Fatalf("displayPath() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestScrollClampsOffset(t *testing.T) {
	var items []*domain.SyncFileItem
	for i := 0; i < 60; i++ {
		items = append(items, item(fmt.Sprintf("f%02d.txt", i), domain.ActionUpload))
	}
	m := newModel(items)
	m.width, m.height = 100, 20 // listHeight = max(5, 12) = 12
	for i := 0; i < 55; i++ {
		m = sendKey(m, "down").(Model)
	}
	if m.cursor != 55 {
		t.Fatalf("cursor = %d, want 55", m.cursor)
	}
	if m.offset != 44 {
		t.Fatalf("offset = %d, want 44 (55-12+1)", m.offset)
	}
	// Back to the top: offset must shrink with the cursor.
	for i := 0; i < 55; i++ {
		m = sendKey(m, "up").(Model)
	}
	if m.cursor != 0 || m.offset != 0 {
		t.Fatalf("cursor=%d offset=%d, want 0/0", m.cursor, m.offset)
	}
}

func TestPageDownMovesByPage(t *testing.T) {
	var items []*domain.SyncFileItem
	for i := 0; i < 60; i++ {
		items = append(items, item(fmt.Sprintf("f%02d.txt", i), domain.ActionUpload))
	}
	m := newModel(items)
	m.width, m.height = 100, 20 // listHeight = 12
	for i := 0; i < 11; i++ {
		m = sendKey(m, "down").(Model) // cursor 11
	}
	m = sendKey(m, "pgdown").(Model) // cursor 23
	if m.cursor != 23 {
		t.Fatalf("cursor = %d, want 23", m.cursor)
	}
	if m.offset != 12 {
		t.Fatalf("offset = %d, want 12", m.offset)
	}
	m = sendKey(m, "pgup").(Model)
	if m.cursor != 11 {
		t.Fatalf("cursor after pgup = %d, want 11", m.cursor)
	}
}

func TestScanPhaseDeliversItems(t *testing.T) {
	items := mixedItems()
	build := func(onProgress func(string, int, int)) ([]*domain.SyncFileItem, error) {
		onProgress("comparing files", 1, 3)
		return items, nil
	}
	m := New("p", "/r", "sftp", domain.DiffModeText, build, nil, nil, nil)
	if !m.scanning {
		t.Fatal("expected scanning state")
	}
	result := m.scanCmd()().(scanResultMsg)
	m2, _ := m.Update(result)
	mm := m2.(Model)
	if mm.scanning {
		t.Fatal("scanning should be done")
	}
	if len(mm.items) != 3 || len(mm.filtered) != 2 {
		t.Fatalf("items=%d filtered=%d, want 3/2", len(mm.items), len(mm.filtered))
	}
}

func TestProgressMsgUpdatesAndRearms(t *testing.T) {
	m := New("p", "/r", "sftp", domain.DiffModeText, func(func(string, int, int)) ([]*domain.SyncFileItem, error) {
		return nil, nil
	}, nil, nil, nil)
	m2, cmd := m.Update(progressMsg{stage: "scanning remote files", done: 42, total: 0})
	mm := m2.(Model)
	if mm.scanStage != "scanning remote files" || mm.scanDone != 42 {
		t.Fatalf("progress not stored: %q %d", mm.scanStage, mm.scanDone)
	}
	if cmd == nil {
		t.Fatal("expected re-armed progress tick")
	}
}

func TestScanErrorView(t *testing.T) {
	build := func(func(string, int, int)) ([]*domain.SyncFileItem, error) {
		return nil, fmt.Errorf("boom")
	}
	m := New("p", "/r", "sftp", domain.DiffModeText, build, nil, nil, nil)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = m2.(Model)
	result := m.scanCmd()().(scanResultMsg)
	m2, _ = m.Update(result)
	mm := m2.(Model)
	if mm.scanErr == nil || mm.scanning {
		t.Fatalf("expected scan error state, got err=%v scanning=%v", mm.scanErr, mm.scanning)
	}
	if !contains(mm.View(), "Scan failed: boom") {
		t.Fatalf("view missing error:\n%s", mm.View())
	}
}

func TestDeployProgressStatus(t *testing.T) {
	items := []*domain.SyncFileItem{item("a.txt", domain.ActionUpload), item("b.txt", domain.ActionUpload)}
	sink := syncer.NewTransferSink()
	execute := func(context.Context, []*domain.SyncFileItem) error {
		sink.Record(syncer.Event{Path: "a.txt", Action: domain.ActionUpload})
		sink.Record(syncer.Event{Path: "b.txt", Action: domain.ActionUpload})
		return nil
	}
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, execute, sink, nil)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24

	m2, _ := m.Update(key("enter")) // deploy (no deletes → no confirm)
	mm := m2.(Model)
	if !mm.running {
		t.Fatal("expected running state")
	}
	if total := sink.Snapshot().Total; total != 2 {
		t.Fatalf("sink total = %d, want 2", total)
	}
	// The progress tick delivers the sink snapshot.
	m2, _ = mm.Update(transferMsg{snapshot: syncer.TransferSnapshot{Path: "b.txt", Done: 2, Total: 2}})
	mm = m2.(Model)
	if !strings.Contains(mm.View(), "Deploying… b.txt (2/2)") {
		t.Fatalf("status missing deploy progress:\n%s", mm.View())
	}
	// Deploy finishes.
	m2, _ = mm.Update(deployResultMsg{})
	if m2.(Model).running {
		t.Fatal("should not be running after deploy result")
	}
}

func TestTransferProgressShowsBytesAndSpeedOnRight(t *testing.T) {
	items := []*domain.SyncFileItem{
		{RelativePath: "a.txt", Action: domain.ActionDownload, RemoteStat: &domain.FileMeta{Size: 4096}},
	}
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, nil, nil, TransferDownload)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24
	m.running = true

	updated, _ := m.Update(transferMsg{snapshot: syncer.TransferSnapshot{
		Path:       "a.txt",
		Done:       0,
		Total:      1,
		BytesDone:  1536,
		BytesTotal: 4096,
		Elapsed:    2 * time.Second,
	}})
	status := strings.Split(updated.(Model).View(), "\n")
	last := status[len(status)-1]
	if !strings.Contains(last, "Downloading… a.txt (0/1)") {
		t.Fatalf("status missing download progress: %q", last)
	}
	if !strings.Contains(last, "1.5 KiB / 4.0 KiB · 768 B/s") {
		t.Fatalf("status missing byte progress and speed: %q", last)
	}
	if lipgloss.Width(last) != 100 {
		t.Fatalf("status width = %d, want 100: %q", lipgloss.Width(last), last)
	}

	completed, _ := updated.(Model).Update(deployResultMsg{})
	completedView := completed.(Model).View()
	if !strings.Contains(completedView, "download complete · 1.5 KiB / 4.0 KiB · 768 B/s") {
		t.Fatalf("completion status missing final transfer stats:\n%s", completedView)
	}
}

func TestDeployStateRendering(t *testing.T) {
	items := []*domain.SyncFileItem{
		item("a.txt", domain.ActionUpload),
		item("b.txt", domain.ActionDownload),
		item("c.txt", domain.ActionDeleteRemote),
	}
	sink := syncer.NewTransferSink()
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, sink, nil)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24

	// a.txt and c.txt completed; b.txt still pending.
	m2, _ := m.Update(transferMsg{
		states: map[string]syncer.Event{
			"a.txt": {Path: "a.txt", Action: domain.ActionUpload},
			"c.txt": {Path: "c.txt", Action: domain.ActionDeleteRemote},
		},
	})
	view := m2.(Model).View()
	if !strings.Contains(view, "[✓ Uploaded]") {
		t.Fatalf("uploaded state missing:\n%s", view)
	}
	if !strings.Contains(view, "[✓ Deleted]") {
		t.Fatalf("deleted state missing:\n%s", view)
	}
	if !strings.Contains(view, "[← Download]") {
		t.Fatalf("pending download should still show its action:\n%s", view)
	}
}

func TestGoBackAndReBrowseAfterDeploy(t *testing.T) {
	items := []*domain.SyncFileItem{item("a.txt", domain.ActionUpload)}
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, nil, nil)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24

	// Before any deploy, r/b must do nothing.
	m2, cmd := m.Update(key("r"))
	if cmd != nil || m2.(Model).GoBack() {
		t.Fatal("r before deploy should be ignored")
	}

	// Deploy completes.
	m2, _ = m.Update(deployResultMsg{})
	m = m2.(Model)
	if !m.deployDone {
		t.Fatal("deployDone should be set")
	}
	if !strings.Contains(m.View(), "[r] another project") {
		t.Fatalf("status missing navigation hints:\n%s", m.View())
	}

	m2, cmd = m.Update(key("r"))
	if !m2.(Model).GoBack() || cmd == nil {
		t.Fatal("r should request go-back and quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("expected tea.QuitMsg")
	}

	m2, _ = m.Update(deployResultMsg{}) // deploy again to re-arm
	m = m2.(Model)
	m2, cmd = m.Update(key("b"))
	if !m2.(Model).ReBrowse() || cmd == nil {
		t.Fatal("b should request re-browse and quit")
	}
}

func TestIgnoreKeyRemovesFileAndPersists(t *testing.T) {
	items := []*domain.SyncFileItem{item("a.php", domain.ActionUpload), item("b.php", domain.ActionUpload)}
	var ignored []string
	var ignoreErr error
	ignore := func(p string) error {
		ignored = append(ignored, p)
		return ignoreErr
	}
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, nil, ignore)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24

	m2, _ := m.Update(key("i"))
	mm := m2.(Model)
	if len(ignored) != 1 || ignored[0] != "a.php" {
		t.Fatalf("ignore callback got %v, want [a.php]", ignored)
	}
	if len(mm.items) != 1 || mm.items[0].RelativePath != "b.php" {
		t.Fatalf("ignored file should be dropped from the queue, got %v", mm.items)
	}
	if !strings.Contains(mm.View(), "ignored a.php") {
		t.Fatalf("status missing confirmation:\n%s", mm.View())
	}
}

func TestIgnoreKeyErrorKeepsFile(t *testing.T) {
	items := []*domain.SyncFileItem{item("a.php", domain.ActionUpload)}
	ignore := func(string) error { return fmt.Errorf("disk full") }
	m := New("p", "/r", "sftp", domain.DiffModeText, nil, nil, nil, ignore)
	m.items = items
	m.rebuildFiltered()
	m.width, m.height = 100, 24

	m2, _ := m.Update(key("i"))
	mm := m2.(Model)
	if len(mm.items) != 1 {
		t.Fatal("file must stay when the ignore save fails")
	}
	if !strings.Contains(mm.status, "ignore failed") {
		t.Fatalf("status = %q", mm.status)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func downloadModel() Model {
	m := New("p", "/var/www/api-backend", "sftp", "", nil, nil, nil, nil, TransferDownload)
	m.items = []*domain.SyncFileItem{
		{RelativePath: "/var/www/api-backend/index.php", LocalPath: "/home/dev/code/public/index.php", Action: domain.ActionDownload, DiffDetails: &domain.DiffResult{Summary: "→ /home/dev/code/public/index.php"}},
		{RelativePath: "/var/www/api-backend/secret.env", LocalPath: "/home/dev/code/public/secret.env", Action: domain.ActionDownload, DiffDetails: &domain.DiffResult{Summary: "→ /home/dev/code/public/secret.env"}},
	}
	m.showAll = true
	m.rebuildFiltered()
	m.refreshDiff()
	return m
}

func TestDownloadModeHeaderAndToolbar(t *testing.T) {
	m := downloadModel()
	m.width, m.height = 100, 24
	view := m.View()
	if !strings.Contains(view, "Downloading from: /var/www/api-backend") {
		t.Fatalf("view missing download header: %q", view)
	}
	if !strings.Contains(view, "[Enter] Download") {
		t.Fatalf("view missing download toolbar: %q", view)
	}
	if strings.Contains(view, "[U]pload") || strings.Contains(view, "[I]gnore") {
		t.Fatalf("sync-only keys must not be advertised in download mode: %q", view)
	}
}

func TestDownloadModeBlocksSyncKeys(t *testing.T) {
	m := sendKey(downloadModel(), "u")
	if !strings.Contains(m.(Model).status, "download-only") {
		t.Fatalf("u should be blocked in download mode, status = %q", m.(Model).status)
	}
	m = sendKey(downloadModel(), "x")
	if !strings.Contains(m.(Model).status, "download-only") {
		t.Fatalf("x should be blocked in download mode, status = %q", m.(Model).status)
	}
	m = sendKey(downloadModel(), "i")
	if !strings.Contains(m.(Model).status, "download-only") {
		t.Fatalf("i should be blocked in download mode, status = %q", m.(Model).status)
	}
}

func TestDownloadModeSpaceTogglesSkipOnly(t *testing.T) {
	m := sendKey(downloadModel(), " ")
	mm := m.(Model)
	if mm.items[0].Action != domain.ActionSkip {
		t.Fatalf("first download should toggle to skip, got %v", mm.items[0].Action)
	}
	m = sendKey(mm, " ")
	mm = m.(Model)
	if mm.items[0].Action != domain.ActionDownload {
		t.Fatalf("skip should toggle back to download, got %v", mm.items[0].Action)
	}
	if mm.items[1].Action != domain.ActionDownload {
		t.Fatal("second item must be untouched by cycling the first")
	}
}

func TestDownloadModeDestinationPane(t *testing.T) {
	m := downloadModel()
	m.width, m.height = 100, 24
	m.focusDiff = true
	m.resize()
	view := m.View()
	if !strings.Contains(view, "Destination") {
		t.Fatalf("focused pane should describe destinations, got %q", view)
	}
	if !strings.Contains(view, "/home/dev/code/public/index.php") {
		t.Fatalf("pane should show the resolved local path, got %q", view)
	}
}

func TestDownloadModeDeployHints(t *testing.T) {
	m := downloadModel()
	mm := m
	mm.running = false
	mm.deployDone = true
	mm.status = ""
	// Simulate the deployResultMsg path by calling the update directly.
	updated := mm
	_ = updated
	m2, _ := m.Update(deployResultMsg{err: nil})
	mm2 := m2.(Model)
	if !strings.Contains(mm2.status, "browse remote again") {
		t.Fatalf("download mode hints should mention browsing remote again, got %q", mm2.status)
	}
}

func uploadModel() Model {
	m := New("p", "/var/www/api-backend", "sftp", "", nil, nil, nil, nil, TransferUpload)
	m.items = []*domain.SyncFileItem{
		{RelativePath: "/home/dev/code/public/index.php", LocalPath: "/home/dev/code/public/index.php", RemotePath: "/var/www/api-backend/index.php", Action: domain.ActionUpload, DiffDetails: &domain.DiffResult{Summary: "→ /var/www/api-backend/index.php"}},
		{RelativePath: "/home/dev/code/public/secret.env", LocalPath: "/home/dev/code/public/secret.env", RemotePath: "/var/www/api-backend/secret.env", Action: domain.ActionUpload, DiffDetails: &domain.DiffResult{Summary: "→ /var/www/api-backend/secret.env"}},
	}
	m.showAll = true
	m.rebuildFiltered()
	m.refreshDiff()
	return m
}

func TestUploadModeHeaderAndToolbar(t *testing.T) {
	m := uploadModel()
	m.width, m.height = 100, 24
	view := m.View()
	if !strings.Contains(view, "Uploading to: /var/www/api-backend") {
		t.Fatalf("view missing upload header: %q", view)
	}
	if !strings.Contains(view, "[Enter] Upload") {
		t.Fatalf("view missing upload toolbar: %q", view)
	}
	if strings.Contains(view, "[U]pload") || strings.Contains(view, "[I]gnore") {
		t.Fatalf("sync-only keys must not be advertised in upload mode: %q", view)
	}
}

func TestUploadModeBlocksSyncKeys(t *testing.T) {
	m := sendKey(uploadModel(), "u")
	if !strings.Contains(m.(Model).status, "upload-only") {
		t.Fatalf("u should be blocked in upload mode, status = %q", m.(Model).status)
	}
	m = sendKey(uploadModel(), "d")
	if !strings.Contains(m.(Model).status, "upload-only") {
		t.Fatalf("d should be blocked in upload mode, status = %q", m.(Model).status)
	}
	m = sendKey(uploadModel(), "x")
	if !strings.Contains(m.(Model).status, "upload-only") {
		t.Fatalf("x should be blocked in upload mode, status = %q", m.(Model).status)
	}
	m = sendKey(uploadModel(), "i")
	if !strings.Contains(m.(Model).status, "upload-only") {
		t.Fatalf("i should be blocked in upload mode, status = %q", m.(Model).status)
	}
}

func TestUploadModeSpaceTogglesSkipOnly(t *testing.T) {
	m := sendKey(uploadModel(), " ")
	mm := m.(Model)
	if mm.items[0].Action != domain.ActionSkip {
		t.Fatalf("first upload should toggle to skip, got %v", mm.items[0].Action)
	}
	m = sendKey(mm, " ")
	mm = m.(Model)
	if mm.items[0].Action != domain.ActionUpload {
		t.Fatalf("second toggle should restore upload, got %v", mm.items[0].Action)
	}
	if mm.items[1].Action != domain.ActionUpload {
		t.Fatalf("other files must be untouched, got %v", mm.items[1].Action)
	}
}

func TestUploadModeDiffPaneShowsRemoteTarget(t *testing.T) {
	m := uploadModel()
	m.width, m.height = 100, 24
	m.focusDiff = true
	m.refreshDiff()
	view := m.View()
	if !strings.Contains(view, "→ /var/www/api-backend/index.php") {
		t.Fatalf("diff pane should show the remote destination: %q", view)
	}
	if !strings.Contains(view, "local file → remote path") {
		t.Fatalf("focused toolbar should describe the upload direction: %q", view)
	}
}

func TestUploadModeDeployStatus(t *testing.T) {
	m := uploadModel()
	m.width, m.height = 100, 24
	updated, _ := m.Update(deployResultMsg{})
	mm := updated.(Model)
	if !strings.Contains(mm.status, "upload complete") {
		t.Fatalf("status = %q, want upload complete", mm.status)
	}
	if !strings.Contains(mm.status, "[b] browse local again") {
		t.Fatalf("status = %q, want browse-local hint", mm.status)
	}
}

func TestPullSectionKeyMergesSectionAndReloadsDiff(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(localFile, []byte("package main\nfunc existing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diffText := `--- /remote/main.go
+++ /local/main.go
@@ -1,3 +1,2 @@
 package main
-func liveOnly() {}
 func existing() {}
`

	testItem := &domain.SyncFileItem{
		RelativePath: "main.go",
		LocalPath:    localFile,
		Action:       domain.ActionUpload,
		DiffDetails: &domain.DiffResult{
			Summary: "1 edit detected",
			Unified: diffText,
		},
	}

	loaderCalled := false
	loader := func(it *domain.SyncFileItem) (*domain.DiffResult, error) {
		loaderCalled = true
		return &domain.DiffResult{Identical: true, Summary: "identical after normalization"}, nil
	}

	m := newModel([]*domain.SyncFileItem{testItem}).WithDiffLoader(loader)
	m.width, m.height = 100, 24
	m.focusDiff = true
	m.refreshDiff()

	// Press 'p' to pull active section
	updated, cmd := m.Update(key("p"))
	m = updated.(Model)

	// Verify local file now contains liveOnly
	content, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "func liveOnly() {}") {
		t.Fatalf("local file was not updated with live function:\n%s", string(content))
	}

	if cmd == nil {
		t.Fatal("expected diff reload command, got nil")
	}

	// Execute cmd to produce diffReloadedMsg
	msg := cmd()
	reloaded, _ := m.Update(msg)
	mm := reloaded.(Model)

	if !loaderCalled {
		t.Fatal("expected diff loader to be called")
	}

	if testItem.Action != domain.ActionSkip {
		t.Fatalf("item action should be ActionSkip when diff becomes identical, got %v", testItem.Action)
	}

	if !strings.Contains(mm.status, "file now matches remote (skipped)") {
		t.Fatalf("unexpected status: %q", mm.status)
	}
}

func TestPullSectionMouseClickOnZone(t *testing.T) {
	dir := t.TempDir()
	localFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(localFile, []byte("package main\nfunc existing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diffText := `--- /remote/main.go
+++ /local/main.go
@@ -1,3 +1,2 @@
 package main
-func liveOnly() {}
 func existing() {}
`

	testItem := &domain.SyncFileItem{
		RelativePath: "main.go",
		LocalPath:    localFile,
		Action:       domain.ActionUpload,
		DiffDetails: &domain.DiffResult{
			Summary: "1 edit detected",
			Unified: diffText,
		},
	}

	m := newModel([]*domain.SyncFileItem{testItem})
	m.width, m.height = 120, 30
	m.focusDiff = true
	m.refreshDiff()

	// Render view so zones are scanned
	view := m.View()
	if !strings.Contains(view, "[→]") {
		t.Fatalf("view should contain [→] button:\n%s", view)
	}

	// Wait for bubblezone scanner worker to process setChan
	time.Sleep(20 * time.Millisecond)

	zone := m.zones.Get("pull-hunk-0")
	if zone == nil {
		t.Fatal("zone pull-hunk-0 should be registered")
	}

	// Click on the zone
	mouseMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      zone.StartX,
		Y:      zone.StartY,
	}
	updated, _ := m.Update(mouseMsg)
	m = updated.(Model)

	// Verify local file now contains liveOnly
	content, err := os.ReadFile(localFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "func liveOnly() {}") {
		t.Fatalf("local file was not updated after mouse click on pull button:\n%s", string(content))
	}
}

func TestPullSectionPreservesScrollOffset(t *testing.T) {
	testItem := &domain.SyncFileItem{
		RelativePath: "main.go",
		Action:       domain.ActionUpload,
		DiffDetails: &domain.DiffResult{
			Summary: "1 edit detected",
			Unified: sampleUnifiedDiff,
		},
	}
	m := newModel([]*domain.SyncFileItem{testItem})
	m.width, m.height = 100, 24
	m.viewport.Height = 3
	m.refreshDiff()

	m.viewport.SetYOffset(2)
	msg := diffReloadedMsg{
		item:    testItem,
		details: testItem.DiffDetails,
		yOffset: 2,
	}
	updated, _ := m.Update(msg)
	mm := updated.(Model)
	if mm.viewport.YOffset != 2 {
		t.Fatalf("viewport YOffset = %d, want 2", mm.viewport.YOffset)
	}
}

func TestWordWrapToggle(t *testing.T) {
	testItem := &domain.SyncFileItem{
		RelativePath: "main.go",
		Action:       domain.ActionUpload,
		DiffDetails: &domain.DiffResult{
			Summary: "1 edit detected",
			Unified: sampleUnifiedDiff,
		},
	}
	m := newModel([]*domain.SyncFileItem{testItem})
	if !m.wordWrap {
		t.Fatal("wordWrap should default to true")
	}

	updated := sendKey(m, "w")
	mm := updated.(Model)
	if mm.wordWrap {
		t.Fatal("wordWrap should be toggled off")
	}
	if !strings.Contains(mm.status, "word wrap disabled") {
		t.Fatalf("status = %q, want disabled message", mm.status)
	}

	updated = sendKey(mm, "w")
	mm = updated.(Model)
	if !mm.wordWrap {
		t.Fatal("wordWrap should be toggled back on")
	}
	if !strings.Contains(mm.status, "word wrap enabled") {
		t.Fatalf("status = %q, want enabled message", mm.status)
	}
}

// TestWindowShrinkKeepsCursorVisible covers the resize path: a shorter window
// must scroll the file list so the cursor row stays on screen.
func TestWindowShrinkKeepsCursorVisible(t *testing.T) {
	items := make([]*domain.SyncFileItem, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, item(fmt.Sprintf("file%02d.txt", i), domain.ActionUpload))
	}
	m := newModel(items)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for i := 0; i < 25; i++ {
		updated = sendKey(updated, "down")
	}
	grown := updated.(Model)
	if grown.cursor != 25 {
		t.Fatalf("cursor = %d, want 25", grown.cursor)
	}

	updated, _ = grown.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	shrunk := updated.(Model)
	vis := shrunk.listHeight()
	if shrunk.cursor < shrunk.offset || shrunk.cursor >= shrunk.offset+vis {
		t.Fatalf("cursor %d outside window [%d,%d) after shrink", shrunk.cursor, shrunk.offset, shrunk.offset+vis)
	}
	if !strings.Contains(shrunk.View(), "file25.txt") {
		t.Fatalf("cursor row missing from the shrunk view:\n%s", shrunk.View())
	}
}
