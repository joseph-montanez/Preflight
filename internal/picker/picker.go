package picker

import (
	"fmt"
	"strconv"
	"strings"

	"preflight/internal/config"
	"preflight/internal/listwin"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
)

// Action is the first-step choice in the startup launcher. The order is
// the menu order: sync, upload, download, ssh, manage, run.
type Action string

const (
	ActionSync     Action = "sync"
	ActionUpload   Action = "upload"
	ActionDownload Action = "download"
	ActionSSH      Action = "ssh"
	ActionManage   Action = "manage"
	ActionRun      Action = "run"
)

// Actions lists the launcher menu in display order.
var Actions = []Action{ActionSync, ActionUpload, ActionDownload, ActionSSH, ActionManage, ActionRun}

func actionLabel(a Action) string {
	switch a {
	case ActionSync:
		return "Sync"
	case ActionUpload:
		return "Upload"
	case ActionDownload:
		return "Download"
	case ActionSSH:
		return "SSH"
	case ActionManage:
		return "Manage"
	case ActionRun:
		return "Run"
	default:
		return string(a)
	}
}

func actionHint(a Action) string {
	switch a {
	case ActionSync:
		return "compare and deploy a project"
	case ActionUpload:
		return "browse local files and upload"
	case ActionDownload:
		return "browse the server and download"
	case ActionSSH:
		return "open a shell into an account"
	case ActionManage:
		return "edit accounts and projects"
	case ActionRun:
		return "run a saved task"
	default:
		return ""
	}
}

const (
	stageAction = iota
	stageTarget
)

// Model is the startup launcher. Step 1 picks an action; step 2 picks the
// filtered target: a project for sync/upload/download, an account for ssh,
// a task for run, and no second step for manage.
type Model struct {
	projects []config.Project
	accounts []config.Account
	tasks    []config.Task

	stage        int
	actionCursor int
	action       Action
	done         bool

	cursor          int
	width           int
	height          int
	selected        string
	selectedAccount string
	selectedTask    string
	status          string
}

// New builds a launcher over projects, accounts, and tasks. The launcher
// always starts on the action menu.
func New(projects []config.Project, accounts []config.Account, tasks []config.Task) Model {
	return Model{projects: projects, accounts: accounts, tasks: tasks}
}

// NewForAction starts the launcher directly on the filtered target step for
// the given action, skipping the action menu. It backs CLI mode flags given
// without -p (e.g. preflight -remote picks a project for download).
// Manage needs no target, so it starts on the action menu like New.
func NewForAction(projects []config.Project, accounts []config.Account, tasks []config.Task, action Action) Model {
	m := Model{projects: projects, accounts: accounts, tasks: tasks}
	if action == ActionManage || action == "" {
		return m
	}
	m.action = action
	m.stage = stageTarget
	return m
}

// Selected returns the chosen project ID, or "" when the user quit or
// chose something other than a project.
func (m Model) Selected() string { return m.selected }

// SelectedAccount returns the account chosen for a direct shell, or "" when
// the user chose something else or quit.
func (m Model) SelectedAccount() string { return m.selectedAccount }

// SelectedTask returns the task chosen for a run, or "" when the user
// chose something else or quit.
func (m Model) SelectedTask() string { return m.selectedTask }

// SelectedAction returns the confirmed action, or "" when the user quit
// without confirming a choice.
func (m Model) SelectedAction() Action {
	if !m.done {
		return ""
	}
	return m.action
}

// ManagePicked reports whether the user chose the Manage action.
func (m Model) ManagePicked() bool { return m.done && m.action == ActionManage }

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch key := msg.String(); key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.stage == stageTarget {
				m.stage = stageAction
				m.cursor = 0
				m.status = ""
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			if m.stage == stageAction {
				return m.selectActionAtCursor()
			}
			return m.selectTargetAtCursor()
		case "up", "k":
			if m.cursorPos() > 0 {
				m.cursorMove(-1)
			}
		case "down", "j":
			if m.cursorPos() < m.listLen()-1 {
				m.cursorMove(1)
			}
		case "home", "g":
			m.cursorSet(0)
		case "end", "G":
			m.cursorSet(max(0, m.listLen()-1))
		default:
			// 1..9 jump within the visible list (1..5 on the action menu).
			if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= m.listLen() {
				m.cursorSet(n - 1)
			}
		}
	}
	return m, nil
}

// cursorPos returns the cursor for the current stage.
func (m Model) cursorPos() int {
	if m.stage == stageAction {
		return m.actionCursor
	}
	return m.cursor
}

func (m *Model) cursorMove(delta int) {
	if m.stage == stageAction {
		m.actionCursor += delta
	} else {
		m.cursor += delta
	}
}

func (m *Model) cursorSet(pos int) {
	if m.stage == stageAction {
		m.actionCursor = pos
	} else {
		m.cursor = pos
	}
}

// selectActionAtCursor moves from the action menu to the filtered target
// list, or quits immediately for the manage action.
func (m Model) selectActionAtCursor() (tea.Model, tea.Cmd) {
	if m.actionCursor < 0 || m.actionCursor >= len(Actions) {
		return m, nil
	}
	m.action = Actions[m.actionCursor]
	m.status = ""
	if m.action == ActionManage {
		m.done = true
		return m, tea.Quit
	}
	m.stage = stageTarget
	m.cursor = 0
	return m, nil
}

// selectTargetAtCursor confirms the filtered target and quits.
func (m Model) selectTargetAtCursor() (tea.Model, tea.Cmd) {
	if m.action == ActionSSH {
		if m.selectAccountAtCursor() {
			m.done = true
			return m, tea.Quit
		}
		return m, nil
	}
	if m.action == ActionRun {
		if m.cursor >= 0 && m.cursor < len(m.tasks) {
			m.selectedTask = m.tasks[m.cursor].ID
			m.done = true
			return m, tea.Quit
		}
		return m, nil
	}
	if len(m.projects) > 0 && m.cursor >= 0 && m.cursor < len(m.projects) {
		m.selected = m.projects[m.cursor].ID
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) listLen() int {
	if m.stage == stageAction {
		return len(Actions)
	}
	if m.action == ActionSSH {
		return len(m.accounts)
	}
	if m.action == ActionRun {
		return len(m.tasks)
	}
	return len(m.projects)
}

func (m *Model) selectAccountAtCursor() bool {
	if m.cursor < 0 || m.cursor >= len(m.accounts) {
		return false
	}
	m.selectAccount(m.accounts[m.cursor].ID)
	return m.selectedAccount != ""
}

func (m *Model) selectAccount(id string) {
	for _, account := range m.accounts {
		if account.ID != id {
			continue
		}
		if !strings.EqualFold(account.Protocol, "sftp") {
			m.status = "SSH shell is only available for SFTP accounts"
			return
		}
		m.selected = ""
		m.selectedAccount = account.ID
		m.status = ""
		return
	}
	m.status = fmt.Sprintf("account %q is not configured", id)
}

func (m Model) View() string {
	if m.width == 0 {
		return "preflight: initializing..."
	}
	if m.stage == stageTarget {
		return m.viewTarget()
	}
	return m.viewAction()
}

func (m Model) viewAction() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(" What do you want to do?") + "\n")
	start, end, moreAbove, moreBelow := listwin.Window(len(Actions), m.actionCursor, m.listRows())
	if moreAbove {
		b.WriteString(m.hiddenRows("↑", start) + "\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(m.renderActionRow(i) + "\n")
	}
	if moreBelow {
		b.WriteString(m.hiddenRows("↓", len(Actions)-end) + "\n")
	}
	hints := "[↑/↓] move  [Enter] select  [Q] quit"
	if m.listLen() <= 9 {
		hints += "  [1-6] jump"
	}
	b.WriteString(faintStyle.Render(hints))
	if m.status != "" {
		b.WriteString("\n" + accentStyle.Render(m.status))
	}
	return b.String()
}

// listRows is how many list rows fit under the header before the hints line.
func (m Model) listRows() int {
	return max(3, m.height-3)
}

// hiddenRows is the marker line standing in for rows scrolled out of the
// visible window.
func (m Model) hiddenRows(arrow string, n int) string {
	return faintStyle.Render(fmt.Sprintf("  %s %d more", arrow, n))
}

func (m Model) renderActionRow(i int) string {
	a := Actions[i]
	marker := "  "
	if i == m.actionCursor {
		marker = "▸ "
	}
	line := fmt.Sprintf("%s%-10s %s", marker, actionLabel(a), actionHint(a))
	if i == m.actionCursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func (m Model) viewTarget() string {
	var b strings.Builder
	switch m.action {
	case ActionSSH:
		b.WriteString(headerStyle.Render(fmt.Sprintf(" Select account for SSH shell (%d)", len(m.accounts))) + "\n")
	case ActionRun:
		b.WriteString(headerStyle.Render(fmt.Sprintf(" Select task to run (%d)", len(m.tasks))) + "\n")
	default:
		b.WriteString(headerStyle.Render(fmt.Sprintf(" Select project to %s (%d)", strings.ToLower(actionLabel(m.action)), len(m.projects))) + "\n")
	}
	maxRows := m.listRows()
	total := m.listLen()
	start, end, moreAbove, moreBelow := listwin.Window(total, m.cursor, maxRows)
	if moreAbove {
		b.WriteString(m.hiddenRows("↑", start) + "\n")
	}
	if m.action == ActionSSH {
		for i := start; i < end; i++ {
			b.WriteString(m.renderAccount(i, m.accounts[i]) + "\n")
		}
		if total == 0 {
			b.WriteString(faintStyle.Render("  no accounts — pick Manage to add one") + "\n")
		}
	} else if m.action == ActionRun {
		for i := start; i < end; i++ {
			b.WriteString(m.renderTask(i, m.tasks[i]) + "\n")
		}
		if total == 0 {
			b.WriteString(faintStyle.Render("  no tasks — pick Manage to add one") + "\n")
		}
	} else {
		for i := start; i < end; i++ {
			b.WriteString(m.renderProject(i, m.projects[i]) + "\n")
		}
		if total == 0 {
			b.WriteString(faintStyle.Render("  no projects — pick Manage to add one") + "\n")
		}
	}
	if moreBelow {
		b.WriteString(m.hiddenRows("↓", total-end) + "\n")
	}
	hints := "[↑/↓] move  [Enter] select  [Esc] back  [Q] quit"
	if m.listLen() <= 9 && m.listLen() > 0 {
		hints += "  [1-9] jump"
	}
	b.WriteString(faintStyle.Render(hints))
	if m.status != "" {
		b.WriteString("\n" + accentStyle.Render(m.status))
	}
	return b.String()
}

func (m Model) renderProject(i int, project config.Project) string {
	marker := "  "
	if i == m.cursor {
		marker = "▸ "
	}
	line := fmt.Sprintf("%s%-20s %-24s %-12s %s → %s",
		marker, truncate(project.ID, 20), truncate(project.Name, 24), project.AccountID,
		truncate(project.LocalRoot, 24), truncate(project.RemoteSummary(), 24))
	if i == m.cursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func (m Model) renderTask(i int, task config.Task) string {
	marker := "  "
	if i == m.cursor {
		marker = "▸ "
	}
	steps := "1 step"
	if len(task.Steps) != 1 {
		steps = fmt.Sprintf("%d steps", len(task.Steps))
	}
	line := fmt.Sprintf("%s%-20s %-24s %s",
		marker, truncate(task.ID, 20), truncate(task.Name, 24), steps)
	if i == m.cursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func (m Model) renderAccount(i int, account config.Account) string {
	marker := "  "
	if i == m.cursor {
		marker = "▸ "
	}
	linked := 0
	for _, project := range m.projects {
		if project.AccountID == account.ID {
			linked++
		}
	}
	linkLabel := "unlinked"
	if linked == 1 {
		linkLabel = "1 project"
	} else if linked > 1 {
		linkLabel = fmt.Sprintf("%d projects", linked)
	}
	address := fmt.Sprintf("%s@%s:%d", account.Auth.Username, account.Host, account.Port)
	line := fmt.Sprintf("%s%-20s %-24s %-6s %-32s %s",
		marker, truncate(account.ID, 20), truncate(account.Name, 24), account.Protocol,
		truncate(address, 32), linkLabel)
	if i == m.cursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
