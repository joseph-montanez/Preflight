package picker

import (
	"fmt"
	"strings"
	"testing"

	"godeploy/internal/config"

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

func TestSelectWithArrows(t *testing.T) {
	m := sendKey(New(projects()), "down")
	m = sendKey(m, "down") // cursor 2
	m = sendKey(m, "enter")
	if got := m.(Model).Selected(); got != "gamma" {
		t.Fatalf("Selected() = %q, want gamma", got)
	}
}

func TestSelectFirstWithEnter(t *testing.T) {
	m := sendKey(New(projects()), "enter")
	if got := m.(Model).Selected(); got != "alpha" {
		t.Fatalf("Selected() = %q, want alpha", got)
	}
}

func TestNumberJump(t *testing.T) {
	m := sendKey(New(projects()), "2")
	m = sendKey(m, "enter")
	if got := m.(Model).Selected(); got != "beta" {
		t.Fatalf("Selected() = %q, want beta", got)
	}
}

func TestQuitWithoutSelection(t *testing.T) {
	m := sendKey(New(projects()), "q")
	if got := m.(Model).Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty after quit", got)
	}
	m = sendKey(New(projects()), "esc")
	if got := m.(Model).Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty after esc", got)
	}
}

func TestEmptyProjectList(t *testing.T) {
	m := sendKey(New(nil), "enter")
	if got := m.(Model).Selected(); got != "" {
		t.Fatalf("Selected() = %q, want empty", got)
	}
}

func TestAccountsViewSelectsUnlinkedAccountForShell(t *testing.T) {
	m := sendKey(New(projects(), accounts()), "a")
	m = sendKey(m, "down") // orphan has no linked project
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
}

func TestAccountsRemainUsableWithoutProjects(t *testing.T) {
	m := New(nil, accounts())
	if !m.accountsView {
		t.Fatal("picker should start on Accounts when no projects exist")
	}
	m.width, m.height = 120, 20
	view := m.View()
	if !strings.Contains(view, "Unlinked") || !strings.Contains(view, "unlinked") {
		t.Fatalf("accounts view should identify the unlinked account:\n%s", view)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || updated.(Model).SelectedAccount() != "acc1" {
		t.Fatal("account-only config should open the selected account shell")
	}
}

func TestProjectShellShortcutSelectsLinkedAccount(t *testing.T) {
	m := New(projects(), accounts()) // alpha -> acc1
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if cmd == nil {
		t.Fatal("t on a project should request a direct shell")
	}
	if got := updated.(Model).SelectedAccount(); got != "acc1" {
		t.Fatalf("SelectedAccount() = %q, want acc1", got)
	}
}

func TestAccountsViewRejectsNonSFTPAccount(t *testing.T) {
	m := sendKey(New(projects(), accounts()), "a")
	m = sendKey(m, "end") // ftp-only
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(Model)
	if cmd != nil || mm.SelectedAccount() != "" {
		t.Fatal("FTP account must stay in the picker without a shell selection")
	}
	if !strings.Contains(strings.ToLower(mm.status), "sftp") {
		t.Fatalf("status = %q, want SFTP-only hint", mm.status)
	}
}

func TestViewRendersHeaderNewlineOutsideStyle(t *testing.T) {
	m := New(projects())
	m.width = 120
	m.height = 10

	header := fmt.Sprintf(" Select project to synchronize (%d)", len(m.projects))
	if got := m.View(); !strings.HasPrefix(got, headerStyle.Render(header)+"\n") {
		t.Fatalf("View() should end the styled header before its newline; got %q", got)
	}
}
