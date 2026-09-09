package picker

import (
	"strings"
	"testing"

	"preflight/internal/config"

	tea "github.com/charmbracelet/bubbletea"
)

func projects() []config.Project {
	return []config.Project{
		{ID: "alpha", Name: "Alpha", AccountID: "acc1", LocalRoot: "/a", RemoteRoot: "/ra"},
		{ID: "beta", Name: "Beta", AccountID: "acc2", LocalRoot: "/b", RemoteRoot: "/rb"},
		{ID: "gamma", Name: "Gamma", AccountID: "acc1", LocalRoot: "/c", RemoteRoot: "/rc"},
	}
}

func accounts() []config.Account {
	return []config.Account{
		{ID: "acc1", Name: "Primary", Protocol: "sftp", Host: "one.example.com", Port: 22, Auth: config.Auth{Username: "deploy"}},
		{ID: "orphan", Name: "Unlinked", Protocol: "sftp", Host: "orphan.example.com", Port: 2222, Auth: config.Auth{Username: "ops"}},
		{ID: "ftp-only", Name: "Legacy", Protocol: "ftps", Host: "ftp.example.com", Port: 21, Auth: config.Auth{Username: "legacy"}},
	}
}

func tasks() []config.Task {
	return []config.Task{
		{ID: "deploy", Name: "Deploy", Steps: []config.TaskStep{
			{AccountID: "acc1", Workdir: "apiv2", Run: "make build"},
			{AccountID: "orphan", Run: "supervisorctl restart apiv2"},
		}},
		{ID: "ping", Name: "Ping", Steps: []config.TaskStep{
			{AccountID: "acc1", Run: "echo hi"},
		}},
	}
}

func sendKey(m tea.Model, s string) tea.Model {
	var msg tea.KeyMsg
	switch s {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "end":
		msg = tea.KeyMsg{Type: tea.KeyEnd}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	updated, _ := m.Update(msg)
	return updated
}

// pickAction moves the action cursor to index n and confirms it.
func pickAction(m tea.Model, n int) tea.Model {
	for i := 0; i < n; i++ {
		m = sendKey(m, "down")
	}
	return sendKey(m, "enter")
}

func TestActionMenuOrder(t *testing.T) {
	want := []Action{ActionSync, ActionUpload, ActionDownload, ActionSSH, ActionManage, ActionRun}
	if len(Actions) != len(want) {
		t.Fatalf("Actions = %v, want %v", Actions, want)
	}
	for i, a := range want {
		if Actions[i] != a {
			t.Fatalf("Actions[%d] = %q, want %q", i, Actions[i], a)
		}
	}
}

func TestSyncThenSelectWithArrows(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 0) // Sync
	m = sendKey(m, "down")
	m = sendKey(m, "down") // cursor 2
	m = sendKey(m, "enter")
	mm := m.(Model)
	if got := mm.Selected(); got != "gamma" {
		t.Fatalf("Selected() = %q, want gamma", got)
	}
	if got := mm.SelectedAction(); got != ActionSync {
		t.Fatalf("SelectedAction() = %q, want sync", got)
	}
}

func TestUploadSelectsProject(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 1) // Upload
	m = sendKey(m, "enter")
	mm := m.(Model)
	if got := mm.Selected(); got != "alpha" {
		t.Fatalf("Selected() = %q, want alpha", got)
	}
	if got := mm.SelectedAction(); got != ActionUpload {
		t.Fatalf("SelectedAction() = %q, want upload", got)
	}
}

func TestDownloadSelectsProject(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 2) // Download
	m = sendKey(m, "down")                                   // beta
	m = sendKey(m, "enter")
	mm := m.(Model)
	if got := mm.Selected(); got != "beta" {
		t.Fatalf("Selected() = %q, want beta", got)
	}
	if got := mm.SelectedAction(); got != ActionDownload {
		t.Fatalf("SelectedAction() = %q, want download", got)
	}
}

func TestNumberJumpOnActionMenu(t *testing.T) {
	m := sendKey(New(projects(), accounts(), tasks()), "4") // SSH
	m = sendKey(m, "enter")
	mm := m.(Model)
	if got := mm.SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty before target confirm", got)
	}
	m = sendKey(m, "2") // orphan
	m = sendKey(m, "enter")
	mm = m.(Model)
	if got := mm.SelectedAccount(); got != "orphan" {
		t.Fatalf("SelectedAccount() = %q, want orphan", got)
	}
	if got := mm.SelectedAction(); got != ActionSSH {
		t.Fatalf("SelectedAction() = %q, want ssh", got)
	}
}

func TestNumberJumpOnProjectList(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 0) // Sync
	m = sendKey(m, "2")
	m = sendKey(m, "enter")
	if got := m.(Model).Selected(); got != "beta" {
		t.Fatalf("Selected() = %q, want beta", got)
	}
}

func TestQuitWithoutSelection(t *testing.T) {
	m := sendKey(New(projects(), accounts(), tasks()), "q")
	mm := m.(Model)
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty after quit", got)
	}
	if got := mm.SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty after quit", got)
	}
	m = sendKey(New(projects(), accounts(), tasks()), "esc")
	if got := m.(Model).SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty after esc", got)
	}
}

func TestEscFromTargetBacksToActions(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 0) // Sync -> target
	m = sendKey(m, "esc")
	mm := m.(Model)
	if got := mm.SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty after backing out", got)
	}
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty after backing out", got)
	}
	// The action menu is live again: pick Manage from here.
	for i := 0; i < 4; i++ {
		m = sendKey(m, "down")
	}
	m = sendKey(m, "enter")
	if !m.(Model).ManagePicked() {
		t.Fatal("Manage should be pickable after backing out to the action menu")
	}
}

func TestManageNeedsNoTarget(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 4) // Manage
	mm := m.(Model)
	if !mm.ManagePicked() {
		t.Fatal("ManagePicked() should be true after picking Manage")
	}
	if got := mm.SelectedAction(); got != ActionManage {
		t.Fatalf("SelectedAction() = %q, want manage", got)
	}
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want no project for manage", got)
	}
}

func TestRunSelectsTask(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 5) // Run
	mm := m.(Model)
	mm.width, mm.height = 120, 20
	view := mm.View()
	if !strings.Contains(view, "Select task to run (2)") {
		t.Fatalf("run target should list tasks:\n%s", view)
	}
	if strings.Contains(view, "Alpha") || strings.Contains(view, "Unlinked") {
		t.Fatalf("run target must list only tasks:\n%s", view)
	}
	m = sendKey(m, "down") // ping
	m = sendKey(m, "enter")
	mm = m.(Model)
	if got := mm.SelectedTask(); got != "ping" {
		t.Fatalf("SelectedTask() = %q, want ping", got)
	}
	if got := mm.SelectedAction(); got != ActionRun {
		t.Fatalf("SelectedAction() = %q, want run", got)
	}
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want no project for run", got)
	}
}

func TestRunEmptyTaskList(t *testing.T) {
	m := pickAction(New(projects(), accounts(), nil), 5) // Run, no tasks
	mm := m.(Model)
	if got := mm.SelectedTask(); got != "" {
		t.Fatalf("SelectedTask() = %q, want empty", got)
	}
	if got := mm.SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty without a confirm", got)
	}
	mm.width, mm.height = 120, 20
	if view := mm.View(); !strings.Contains(view, "no tasks") {
		t.Fatalf("target view should hint at the empty task list:\n%s", view)
	}
}

func TestNewForActionRunStartsAtTasks(t *testing.T) {
	m := sendKey(NewForAction(projects(), accounts(), tasks(), ActionRun), "enter")
	mm := m.(Model)
	if got := mm.SelectedTask(); got != "deploy" {
		t.Fatalf("SelectedTask() = %q, want deploy", got)
	}
	if got := mm.SelectedAction(); got != ActionRun {
		t.Fatalf("SelectedAction() = %q, want run", got)
	}
}

func TestEmptyProjectList(t *testing.T) {
	m := pickAction(New(nil, accounts(), tasks()), 0) // Sync, no projects
	mm := m.(Model)
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty", got)
	}
	if got := mm.SelectedAction(); got != "" {
		t.Fatalf("SelectedAction() = %q, want empty without a confirm", got)
	}
	mm.width, mm.height = 120, 20
	if view := mm.View(); !strings.Contains(view, "no projects") {
		t.Fatalf("target view should hint at the empty project list:\n%s", view)
	}
}

func TestSSHSelectsAccountForShell(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 3) // SSH
	m = sendKey(m, "down")                                   // orphan has no linked project
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("selecting an SFTP account should quit the picker")
	}
	mm := updated.(Model)
	if got := mm.SelectedAccount(); got != "orphan" {
		t.Fatalf("SelectedAccount() = %q, want orphan", got)
	}
	if got := mm.Selected(); got != "" {
		t.Fatalf("Selected() = %q, want no project for direct account shell", got)
	}
	if got := mm.SelectedAction(); got != ActionSSH {
		t.Fatalf("SelectedAction() = %q, want ssh", got)
	}
}

func TestSSHRemainsUsableWithoutProjects(t *testing.T) {
	m := pickAction(New(nil, accounts(), tasks()), 3) // SSH, account-only config
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || updated.(Model).SelectedAccount() != "acc1" {
		t.Fatal("account-only config should open the selected account shell")
	}
}

func TestSSHShowsAccountsNotProjects(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 3) // SSH
	mm := m.(Model)
	mm.width, mm.height = 120, 20
	view := mm.View()
	if !strings.Contains(view, "Unlinked") || !strings.Contains(view, "unlinked") {
		t.Fatalf("SSH target view should list accounts:\n%s", view)
	}
	if strings.Contains(view, "Alpha") {
		t.Fatalf("SSH target view must not list projects:\n%s", view)
	}
}

func TestProjectTargetShowsProjectsNotAccounts(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 0) // Sync
	mm := m.(Model)
	mm.width, mm.height = 120, 20
	view := mm.View()
	if !strings.Contains(view, "Alpha") {
		t.Fatalf("project target view should list projects:\n%s", view)
	}
	if strings.Contains(view, "Unlinked") {
		t.Fatalf("project target view must not list accounts:\n%s", view)
	}
}

func TestSSHRejectsNonSFTPAccount(t *testing.T) {
	m := pickAction(New(projects(), accounts(), tasks()), 3) // SSH
	m = sendKey(m, "end")                                    // ftp-only
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(Model)
	if cmd != nil || mm.SelectedAccount() != "" {
		t.Fatal("FTP account must stay in the picker without a shell selection")
	}
	if !strings.Contains(strings.ToLower(mm.status), "sftp") {
		t.Fatalf("status = %q, want SFTP-only hint", mm.status)
	}
}

func TestLegacyPickerKeysAreInert(t *testing.T) {
	for _, key := range []string{"a", "p", "t", "tab"} {
		m := sendKey(New(projects(), accounts(), tasks()), key)
		mm := m.(Model)
		if got := mm.Selected(); got != "" {
			t.Fatalf("key %q selected %q, want no selection", key, got)
		}
		if got := mm.SelectedAction(); got != "" {
			t.Fatalf("key %q confirmed action %q, want no confirm", key, got)
		}
	}
}

func TestNewForActionStartsAtTarget(t *testing.T) {
	mm := NewForAction(projects(), accounts(), tasks(), ActionDownload)
	mm.width, mm.height = 120, 20
	if view := mm.View(); !strings.Contains(view, "Alpha") {
		t.Fatalf("forced download should start on the project list:\n%s", view)
	}
	m := sendKey(mm, "enter")
	got := m.(Model)
	if got.Selected() != "alpha" {
		t.Fatalf("Selected() = %q, want alpha", got.Selected())
	}
	if got.SelectedAction() != ActionDownload {
		t.Fatalf("SelectedAction() = %q, want download", got.SelectedAction())
	}
}

func TestNewForActionSSHStartsAtAccounts(t *testing.T) {
	m := sendKey(NewForAction(projects(), accounts(), tasks(), ActionSSH), "enter")
	mm := m.(Model)
	if got := mm.SelectedAccount(); got != "acc1" {
		t.Fatalf("SelectedAccount() = %q, want acc1", got)
	}
}

func TestNewForActionManageFallsBackToMenu(t *testing.T) {
	mm := NewForAction(projects(), accounts(), tasks(), ActionManage)
	mm.width, mm.height = 120, 20
	if view := mm.View(); !strings.Contains(view, "Manage") {
		t.Fatalf("forced manage should fall back to the action menu:\n%s", view)
	}
}

func TestViewRendersHeaderNewlineOutsideStyle(t *testing.T) {
	m := New(projects(), accounts(), tasks())
	m.width = 120
	m.height = 10

	header := " What do you want to do?"
	if got := m.View(); !strings.HasPrefix(got, headerStyle.Render(header)+"\n") {
		t.Fatalf("View() should end the styled header before its newline; got %q", got)
	}
}
