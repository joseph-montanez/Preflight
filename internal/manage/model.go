package manage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"godeploy/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle      = lipgloss.NewStyle().Bold(true)
	selectedStyle    = lipgloss.NewStyle().Bold(true).Reverse(true)
	titleStyle       = lipgloss.NewStyle().Bold(true)
	accentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	errorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	faintStyle       = lipgloss.NewStyle().Faint(true)
	mutedStyle       = lipgloss.NewStyle().Faint(true)
	activeTabStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12")).Padding(0, 1)
	inactiveTabStyle = lipgloss.NewStyle().Padding(0, 1)
)

// Model is the config manager TUI: a tabbed list of accounts and
// projects with a modal form editor for each entry.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmQuit
	confirmDeleteAccount
	confirmDeleteProject
)

type Model struct {
	cfg            *config.Config
	secrets        *config.Secrets
	path           string
	tab            int // 0 accounts, 1 projects
	cursor         int
	form           *Form
	mappings       *mappingEditor
	confirm        string
	confirmAct     confirmAction
	status         string
	dirty          bool
	width          int
	height         int
	shellAccountID string

	completer *remoteCompleter
}

func New(cfg *config.Config, path string) Model {
	secrets, err := config.LoadSecrets(config.SecretsPath(path))
	if err != nil {
		secrets = &config.Secrets{}
	}
	m := Model{
		cfg:       cfg,
		secrets:   secrets,
		path:      path,
		completer: &remoteCompleter{secrets: secrets},
	}
	if len(cfg.Accounts) == 0 && len(cfg.Projects) > 0 {
		m.tab = 1
	}
	return m
}

func (m Model) Init() tea.Cmd { return nil }

// ShellAccountID is set when the user presses T on an SFTP account. The
// manager exits so the caller can restore the real terminal for OpenSSH.
func (m Model) ShellAccountID() string { return m.shellAccountID }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		if m.mappings != nil {
			cmd, err := m.mappings.Update(msg)
			_ = err // async completion results never close the editor
			return m, cmd
		}
		if m.form != nil {
			cmd, err := m.form.Update(msg)
			_ = err // async results never close the form
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.mappings != nil {
		cmd, err := m.mappings.Update(msg)
		switch {
		case errors.Is(err, errMappingsSaved):
			m.form.setValue(m.form.byTagIdx(tagMappings), formatMappings(m.mappings.result))
			m.form.status = mappingSummary(m.form.byTag(tagMappings).Value)
			m.mappings = nil
		case errors.Is(err, errMappingsCancel):
			m.form.status = "mapping changes cancelled"
			m.mappings = nil
		case err != nil:
			m.status = err.Error()
			m.mappings = nil
		}
		return *m, cmd
	}

	if m.form != nil {
		if key == "enter" && m.form.popup == nil && m.form.busy == "" && m.form.Fields[m.form.Cursor].Kind == KindEditor {
			m.openMappingEditor()
			return *m, nil
		}
		cmd, err := m.form.Update(msg)
		switch {
		case errors.Is(err, errFormSaved):
			title := m.form.Title
			m.form = nil
			if errStr := m.persist(); errStr != "" {
				m.status = errStr
			} else {
				m.status = title + " saved — written to " + filepath.Dir(m.path)
			}
		case errors.Is(err, errFormCancel):
			m.status = "edit cancelled"
			m.form = nil
		case err != nil:
			m.status = err.Error()
			m.form = nil
		}
		return *m, cmd
	}

	if m.confirm != "" {
		switch key {
		case "y", "Y":
			act := m.confirmAct
			m.confirm, m.confirmAct = "", confirmNone
			switch act {
			case confirmQuit:
				// y = save then quit; if the save fails, stay.
				if errStr := m.persist(); errStr != "" {
					m.status = errStr
					return *m, nil
				}
				m.completer.close()
				return *m, tea.Quit
			case confirmDeleteAccount:
				m.doDeleteAccount()
			case confirmDeleteProject:
				m.doDeleteProject()
			}
		case "n", "N":
			if m.confirmAct == confirmQuit {
				// n = quit without saving
				m.confirm, m.confirmAct = "", confirmNone
				m.completer.close()
				return *m, tea.Quit
			}
			m.confirm, m.confirmAct = "", confirmNone
			m.status = "cancelled"
		case "esc":
			m.confirm, m.confirmAct = "", confirmNone
			m.status = "cancelled"
		}
		return *m, nil
	}

	switch key {
	case "q", "ctrl+c":
		if m.dirty {
			m.confirm = "save changes before quitting? [y]es / [n]o / [esc] stay"
			m.confirmAct = confirmQuit
		} else {
			m.completer.close()
			return *m, tea.Quit
		}
	case "s":
		if errStr := m.persist(); errStr != "" {
			m.status = errStr
		} else {
			m.status = "config + secrets written to " + filepath.Dir(m.path)
		}

	case "n":
		m.addNew()
	case "d":
		m.deleteCurrent()
	case "t":
		if m.tab != 0 {
			m.status = "switch to the Accounts tab to open an SSH shell"
			break
		}
		if m.cursor < 0 || m.cursor >= len(m.cfg.Accounts) {
			m.status = "no account selected"
			break
		}
		account := m.cfg.Accounts[m.cursor]
		if !strings.EqualFold(account.Protocol, "sftp") {
			m.status = "SSH shell is only available for SFTP accounts"
			break
		}
		if m.dirty {
			m.status = "save changes before opening a shell"
			break
		}
		m.shellAccountID = account.ID
		m.completer.close()
		return *m, tea.Quit
	case "enter":
		m.editCurrent()
	case "tab", "1", "2":
		switch key {
		case "1":
			m.tab = 0
		case "2":
			m.tab = 1
		default:
			m.tab = 1 - m.tab
		}
		m.cursor = 0
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
		m.cursor = m.listLen() - 1
	}
	return *m, nil
}

func (m *Model) listLen() int {
	if m.tab == 0 {
		return len(m.cfg.Accounts)
	}
	return len(m.cfg.Projects)
}

// persist writes config.yaml and secrets.yaml to disk. It returns an
// error message (or "") and clears the dirty flag on success.
func (m *Model) persist() string {
	if err := m.cfg.Save(m.path); err != nil {
		return "save failed: " + err.Error()
	}
	if err := m.secrets.Save(config.SecretsPath(m.path)); err != nil {
		return "config saved but secrets failed: " + err.Error()
	}
	m.dirty = false
	return ""
}

func (m *Model) addNew() {
	if m.tab == 0 {
		m.openAccountForm(-1)
	} else {
		m.openProjectForm(-1)
	}
}

func (m *Model) editCurrent() {
	if m.tab == 0 {
		if m.cursor < len(m.cfg.Accounts) {
			m.openAccountForm(m.cursor)
		}
	} else if m.cursor < len(m.cfg.Projects) {
		m.openProjectForm(m.cursor)
	}
}

func (m *Model) deleteCurrent() {
	if m.tab == 0 {
		if m.cursor >= len(m.cfg.Accounts) {
			return
		}
		acc := m.cfg.Accounts[m.cursor]
		var refs []string
		for _, p := range m.cfg.Projects {
			if p.AccountID == acc.ID {
				refs = append(refs, p.ID)
			}
		}
		m.confirm = fmt.Sprintf("delete account %s? [y/N]", acc.ID)
		if len(refs) > 0 {
			m.confirm = fmt.Sprintf("account %s is used by %s — delete anyway? [y/N]", acc.ID, strings.Join(refs, ", "))
		}
		m.confirmAct = confirmDeleteAccount
		return
	}
	if m.cursor >= len(m.cfg.Projects) {
		return
	}
	m.confirm = fmt.Sprintf("delete project %s? [y/N]", m.cfg.Projects[m.cursor].ID)
	m.confirmAct = confirmDeleteProject
}

func (m *Model) doDeleteAccount() {
	accID := m.cfg.Accounts[m.cursor].ID
	m.cfg.Accounts = append(m.cfg.Accounts[:m.cursor], m.cfg.Accounts[m.cursor+1:]...)
	if m.secrets != nil {
		delete(m.secrets.Accounts, accID)
	}
	m.clampCursor()
	if errStr := m.persist(); errStr != "" {
		m.status = errStr
	} else {
		m.status = "account deleted — written to " + filepath.Dir(m.path)
	}
}

func (m *Model) doDeleteProject() {
	m.cfg.Projects = append(m.cfg.Projects[:m.cursor], m.cfg.Projects[m.cursor+1:]...)
	m.clampCursor()
	if errStr := m.persist(); errStr != "" {
		m.status = errStr
	} else {
		m.status = "project deleted — written to " + filepath.Dir(m.path)
	}
}

func (m *Model) clampCursor() {
	if m.cursor >= m.listLen() {
		m.cursor = m.listLen() - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m Model) View() string {
	if m.width == 0 {
		return "godeploy config manager: initializing..."
	}
	if m.mappings != nil {
		return m.mappings.View(m.width, m.height)
	}
	if m.form != nil {
		return m.form.View(m.width, m.height)
	}
	return strings.Join([]string{m.renderHeader(), m.renderList(), m.renderStatus()}, "\n")
}

func (m *Model) renderHeader() string {
	tabs := make([]string, 0, 2)
	for i, name := range []string{"Accounts", "Projects"} {
		if i == m.tab {
			tabs = append(tabs, activeTabStyle.Render(name))
		} else {
			tabs = append(tabs, inactiveTabStyle.Render(name))
		}
	}
	dirty := ""
	if m.dirty {
		dirty = " " + accentStyle.Render("● unsaved")
	}
	header := headerStyle.Render(" godeploy ") + strings.Join(tabs, " ") + faintStyle.Render(" "+m.path) + dirty
	return truncate(header, m.width)
}

func (m *Model) renderList() string {
	rows := []string{}
	if m.tab == 0 {
		for i := range m.cfg.Accounts {
			rows = append(rows, m.renderAccountRow(i))
		}
		if len(rows) == 0 {
			rows = append(rows, faintStyle.Render("  no accounts — press n to add one"))
		}
	} else {
		for i := range m.cfg.Projects {
			rows = append(rows, m.renderProjectRow(i))
		}
		if len(rows) == 0 {
			rows = append(rows, faintStyle.Render("  no projects — press n to add one"))
		}
	}
	maxRows := max(3, m.height-3)
	if len(rows) > maxRows {
		rows = append(rows[:maxRows-1], faintStyle.Render(" …"))
	}
	return strings.Join(rows, "\n")
}

func (m *Model) renderAccountRow(i int) string {
	a := m.cfg.Accounts[i]
	marker := "  "
	if i == m.cursor {
		marker = "▸ "
	}
	addr := fmt.Sprintf("%s@%s:%d", a.Auth.Username, a.Host, a.Port)
	line := fmt.Sprintf("%s%-16s %-22s %-5s %-30s %s",
		marker, truncate(a.ID, 16), truncate(a.Name, 22), a.Protocol, truncate(addr, 30), a.Auth.Type)
	if i == m.cursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func (m *Model) renderProjectRow(i int) string {
	p := m.cfg.Projects[i]
	marker := "  "
	if i == m.cursor {
		marker = "▸ "
	}
	line := fmt.Sprintf("%s%-16s %-22s %-12s %s → %s",
		marker, truncate(p.ID, 16), truncate(p.Name, 22), p.AccountID, truncate(p.LocalRoot, 20), truncate(p.RemoteSummary(), 20))
	if i == m.cursor {
		return selectedStyle.Render(truncate(line, m.width))
	}
	return truncate(line, m.width)
}

func (m *Model) renderStatus() string {
	if m.confirm != "" {
		return accentStyle.Render(m.confirm)
	}
	hints := "[Tab] Accounts/Projects  [Enter] Edit  [N] New  [D] Delete  [S] Save  [Q] Quit"
	if m.tab == 0 {
		hints = "[Tab] Accounts/Projects  [Enter] Edit  [T] Shell  [N] New  [D] Delete  [S] Save  [Q] Quit"
	}
	if m.status != "" {
		return faintStyle.Render(hints) + "  ·  " + m.status
	}
	return faintStyle.Render(hints)
}
