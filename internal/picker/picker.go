package picker

import (
	"fmt"
	"strconv"
	"strings"

	"godeploy/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
)

// Model selects either a project to synchronize or an account for a direct
// SSH shell. The accounts view includes unlinked accounts, while T on a
// highlighted project shells into that project's account.
type Model struct {
	projects        []config.Project
	accounts        []config.Account
	accountsView    bool
	cursor          int
	width           int
	height          int
	selected        string
	selectedAccount string
	status          string
}

// New accepts accounts as an optional second argument for compatibility with
// callers that only need the original project picker.
func New(projects []config.Project, accountLists ...[]config.Account) Model {
	m := Model{projects: projects}
	if len(accountLists) > 0 {
		m.accounts = accountLists[0]
	}
	if len(projects) == 0 && len(m.accounts) > 0 {
		m.accountsView = true
	}
	return m
}

// Selected returns the chosen project ID, or "" when the user quit
// without selecting.
func (m Model) Selected() string { return m.selected }

// SelectedAccount returns the account chosen for a direct shell, or "" when
// the user chose a project or quit.
func (m Model) SelectedAccount() string { return m.selectedAccount }

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch key := msg.String(); key {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if m.accountsView {
				if m.selectAccountAtCursor() {
					return m, tea.Quit
				}
				return m, nil
			}
			if len(m.projects) > 0 && m.cursor < len(m.projects) {
				m.selected = m.projects[m.cursor].ID
			}
			return m, tea.Quit
		case "t":
			if m.accountsView {
				if m.selectAccountAtCursor() {
					return m, tea.Quit
				}
				return m, nil
			}
			if m.cursor >= 0 && m.cursor < len(m.projects) {
				m.selectAccount(m.projects[m.cursor].AccountID)
				if m.selectedAccount != "" {
					return m, tea.Quit
				}
			}
		case "a":
			m.accountsView = true
			m.cursor = 0
			m.status = ""
		case "p":
			m.accountsView = false
			m.cursor = 0
			m.status = ""
		case "tab":
			m.accountsView = !m.accountsView
			m.cursor = 0
			m.status = ""
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = max(0, m.listLen()-1)
		default:
			// 1..9 jump within whichever list is visible.
			if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= m.listLen() {
				m.cursor = n - 1
			}
		}
	}
	return m, nil
}

func (m Model) listLen() int {
	if m.accountsView {
		return len(m.accounts)
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
		return "godeploy: initializing..."
	}
	var b strings.Builder
	if m.accountsView {
		b.WriteString(headerStyle.Render(fmt.Sprintf(" Select account for SSH shell (%d)", len(m.accounts))) + "\n")
	} else {
		b.WriteString(headerStyle.Render(fmt.Sprintf(" Select project to synchronize (%d)", len(m.projects))) + "\n")
	}
	maxRows := max(3, m.height-3)
	if m.accountsView {
		for i, account := range m.accounts {
			if i >= maxRows {
				b.WriteString(faintStyle.Render(" …") + "\n")
				break
			}
			b.WriteString(m.renderAccount(i, account) + "\n")
		}
		if len(m.accounts) == 0 {
			b.WriteString(faintStyle.Render("  no accounts — run godeploy --manage to add one") + "\n")
		}
	} else {
		for i, project := range m.projects {
			if i >= maxRows {
				b.WriteString(faintStyle.Render(" …") + "\n")
				break
			}
			b.WriteString(m.renderProject(i, project) + "\n")
		}
		if len(m.projects) == 0 {
			b.WriteString(faintStyle.Render("  no projects — switch to Accounts for a direct shell") + "\n")
		}
	}
	hints := "[↑/↓] move  [Enter] synchronize  [T] shell account  [A/Tab] accounts  [Q] quit"
	if m.accountsView {
		hints = "[↑/↓] move  [Enter/T] open shell  [P/Tab] projects  [Q] quit"
	}
	if m.listLen() <= 9 {
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
