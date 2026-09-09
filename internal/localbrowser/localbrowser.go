package localbrowser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"preflight/internal/config"
	ignoreengine "preflight/internal/ignore"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	headerStyle   = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
)

// UploadRoot is one selected upload target: an absolute local path plus
// the remote path it resolves to. RemotePath is empty when the local
// path is not covered by a path mapping and the caller must ask for a
// destination (then the path relative to the project root is preserved
// under the picked base).
type UploadRoot struct {
	LocalPath  string
	RemotePath string
}

type entry struct {
	name     string
	dir      bool
	excluded bool // matches a project exclusion pattern
}

// Model browses the local file system for upload. Unlike the sync folder
// browser it shows every file (ignore rules never hide anything) and
// marks both files and folders; marking a folder means uploading its
// whole subtree. .gitignore never applies here. Project exclusions are
// shown dimmed with a ⊘ marker; the e key toggles whether matching
// entries are skipped when building the upload list (default: everything
// uploads).
//
// Navigation starts at a virtual root listing the project's mapping
// local paths (each labeled with its remote target). ".." from there
// goes to the project root, so unmapped project content stays reachable.
// Browsing never leaves the project.
type Model struct {
	localRoot string
	mappings  []config.PathMapping
	ignores   *ignoreengine.Engine

	// current is the absolute local path being viewed; "" is the virtual
	// root showing the mapping local paths.
	current  string
	entries  []entry
	cursor   int
	offset   int // first visible row (scrolling window)
	width    int
	height   int
	status   string
	marked   map[string]bool // absolute local paths to upload
	chosen   bool
	uploads  []UploadRoot
	filterOn bool // e toggles exclusion filtering during the build

	// searchOn is the "/" name filter: typing narrows the current
	// folder's listing (files and folders, never recursive); Enter acts
	// on the highlighted entry, Esc restores the full listing.
	searchOn bool
	search   textinput.Model

	// editorCmd is the configured editor command line ("" disables the
	// edit keys, which then only show a status hint). editPath is set
	// when the user presses alt+e: the caller runs the editor on it
	// (the file is local — no transfer needed) and re-opens the browser
	// with the marks restored.
	editorCmd string
	editPath  string
}

func newSearch() textinput.Model {
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "filter names"
	in.CharLimit = 256
	return in
}

func New(localRoot string, mappings []config.PathMapping, ignores *ignoreengine.Engine, editorCmd string) Model {
	m := Model{
		localRoot: filepath.Clean(localRoot),
		mappings:  mappings,
		ignores:   ignores,
		marked:    map[string]bool{},
		search:    newSearch(),
	}
	m.load()
	return m
}

// Selected returns the resolved upload roots, whether the exclusion
// filter is on, and whether the user confirmed a selection.
func (m Model) Selected() ([]UploadRoot, bool, bool) { return m.uploads, m.filterOn, m.chosen }

// Mark selects an absolute local path for upload. It exists for tests
// and programmatic use; the TUI marks via Space.
func (m *Model) Mark(abs string) { m.marked[abs] = true }

// Finish resolves the marked paths into upload roots (same as pressing
// s in the TUI).
func (m *Model) Finish() { m.finish() }

// EditRequest returns the absolute local path the user asked to edit
// (alt+e), or "". The caller runs the editor on it and re-opens the
// browser with the marks restored.
func (m Model) EditRequest() string { return m.editPath }

// MarkedPaths returns the marked absolute local paths (sorted), used to
// preserve the selection across editor sessions.
func (m Model) MarkedPaths() []string {
	out := make([]string, 0, len(m.marked))
	for s := range m.marked {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// RestoreMarks pre-marks the given absolute local paths (from an
// earlier browser session) so an editor detour does not lose the
// selection.
func (m *Model) RestoreMarks(paths []string) {
	for _, p := range paths {
		m.marked[p] = true
	}
}

// requestEdit asks the caller to open the editor on the entry under the
// cursor. Local files and folders are both fine (the editor opens
// either); with no editor configured it only shows a status hint.
func (m *Model) requestEdit() {
	if m.editorCmd == "" {
		m.status = "no editor configured — add 'editor' to config.yaml"
		return
	}
	abs := m.absAtCursor()
	if abs == "" {
		m.status = "select a local file or folder"
		return
	}
	m.editPath = abs
}

func (m Model) Init() tea.Cmd { return nil }

// atRoot reports whether the browser shows the virtual mapping-root view.
func (m Model) atRoot() bool { return m.current == "" }

// up navigates one level: the virtual root and the project root link to
// each other, so the mapping roots and any project content are reachable
// from either.
func (m *Model) up() {
	switch {
	case m.current == "":
		m.current = m.localRoot
	case m.current == m.localRoot:
		m.current = ""
	default:
		m.current = filepath.Dir(m.current)
	}
	m.load()
}

// load refreshes the current view from disk. At the virtual root it
// synthesizes the mapping local paths; anywhere else it lists the
// directory.
func (m *Model) load() {
	m.exitSearch()
	if m.atRoot() {
		m.entries = nil
		for _, mapping := range m.mappings {
			m.entries = append(m.entries, entry{name: mappingEntryName(mapping), dir: true})
		}
		m.status = ""
		m.cursor = 0
		m.offset = 0
		return
	}
	infos, err := os.ReadDir(m.current)
	if err != nil {
		m.entries = nil
		m.status = "cannot read " + m.current + ": " + err.Error()
		return
	}
	m.status = ""
	var dirs, files []entry
	for _, info := range infos {
		en := entry{name: info.Name(), dir: info.IsDir()}
		if en.dir {
			dirs = append(dirs, en)
		} else {
			files = append(files, en)
		}
	}
	less := func(a, b string) bool { return strings.ToLower(a) < strings.ToLower(b) }
	sort.Slice(dirs, func(i, j int) bool { return less(dirs[i].name, dirs[j].name) })
	sort.Slice(files, func(i, j int) bool { return less(files[i].name, files[j].name) })
	entries := append(dirs, files...)
	for i := range entries {
		entries[i].excluded = m.entryExcluded(entries[i].name, entries[i].dir)
	}
	m.entries = entries
	m.cursor = 0
	m.offset = 0
}

// entryExcluded reports whether an entry in the current directory
// matches a project exclusion (root-match semantics: project-relative
// path AND bare name, so "dist/" dims a dist folder at any depth).
func (m Model) entryExcluded(name string, isDir bool) bool {
	if m.ignores == nil {
		return false
	}
	projectRel, err := filepath.Rel(m.localRoot, filepath.Join(m.current, name))
	if err != nil {
		return false
	}
	return m.ignores.RootIgnored(projectRel, isDir)
}

func (m *Model) descend(name string) {
	if m.atRoot() {
		m.current = m.localRoot
		for _, mapping := range m.mappings {
			if mappingEntryName(mapping) == name {
				if base := localBaseAbs(m.localRoot, mapping); base != "" {
					m.current = base
				}
				break
			}
		}
	} else {
		m.current = filepath.Join(m.current, name)
	}
	m.load()
}

// virtualEntryAbs resolves a virtual-root entry (a mapping) to its
// absolute local path; the project root for a root mapping.
func (m Model) virtualEntryAbs(name string) string {
	for _, mapping := range m.mappings {
		if mappingEntryName(mapping) == name {
			if base := localBaseAbs(m.localRoot, mapping); base != "" {
				return base
			}
			return m.localRoot
		}
	}
	return ""
}

// abs returns the absolute local path of the entry at the cursor (or the
// current directory when the cursor is on row 0). Row 1 is always "..".
func (m Model) absAtCursor() string {
	if m.cursor == 0 {
		if m.atRoot() {
			return ""
		}
		return m.current
	}
	idx := m.cursor - 2
	vis := m.visible()
	if idx < 0 || idx >= len(vis) {
		return ""
	}
	e := vis[idx]
	if m.atRoot() {
		return m.virtualEntryAbs(e.name)
	}
	return filepath.Join(m.current, e.name)
}

func (m Model) entryAtCursor() (entry, bool) {
	if m.atRoot() {
		return entry{}, false
	}
	idx := m.cursor - 2
	vis := m.visible()
	if idx < 0 || idx >= len(vis) {
		return entry{}, false
	}
	return vis[idx], true
}

// visible returns the entries shown in the list: the whole current
// directory, or with the "/" filter open and non-empty, only the names
// containing the query (case-insensitive, files and folders alike).
// Filtering is per-folder and never recursive.
func (m Model) visible() []entry {
	if !m.searchOn || m.search.Value() == "" {
		return m.entries
	}
	q := strings.ToLower(m.search.Value())
	out := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.name), q) {
			out = append(out, e)
		}
	}
	return out
}

// exitSearch closes the "/" filter; the full listing reappears and the
// cursor keeps its position.
func (m *Model) exitSearch() {
	m.searchOn = false
	m.search.Blur()
}

// handleSearchKey routes keys while the "/" name filter is open. Typing
// narrows the current folder's listing; ↑/↓ move through the matches;
// Enter acts on the highlighted entry exactly as in the full listing;
// Esc closes the filter. Other keys (including q and ctrl+c) are query
// text while the filter is open.
func (m Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Modifier keys act on the highlighted entry while the filter is
	// open (plain keys keep editing the query): alt+m toggles its mark
	// (same as Space in the full listing), alt+e opens the editor on it.
	key := msg.String()
	isAltM := (msg.Alt && strings.ToLower(string(msg.Runes)) == "m") || key == "alt+m" || key == "alt+M"
	isAltE := (msg.Alt && strings.ToLower(string(msg.Runes)) == "e") || key == "alt+e" || key == "alt+E"
	if isAltM {
		m.toggleMark()
		return m, nil
	}
	if isAltE {
		m.requestEdit()
		if m.editPath != "" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.exitSearch()
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		m.clampOffset()
	case "down", "j":
		if m.cursor < m.rowCount()-1 {
			m.cursor++
		}
		m.clampOffset()
	case "enter":
		m.selectRow()
		if m.chosen {
			return m, tea.Quit
		}
	default:
		updated, cmd := m.search.Update(msg)
		m.search = updated
		if m.cursor > m.rowCount()-1 {
			m.cursor = m.rowCount() - 1
		}
		m.clampOffset()
		return m, cmd
	}
	return m, nil
}

func (m Model) rowCount() int {
	return 2 + len(m.visible()) // "(select marked)" + ".." + entries
}

// clampOffset keeps the cursor inside the visible scrolling window.
func (m *Model) clampOffset() {
	vis := max(3, m.height-5)
	total := m.rowCount()
	if total <= vis {
		m.offset = 0
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
}

// selectRow resolves the action for the cursor row: row 0 finishes the
// selection, ".." goes up, a directory descends, a file toggles its mark.
func (m *Model) selectRow() {
	if m.cursor == 0 {
		m.finish()
		return
	}
	if m.cursor == 1 {
		m.up()
		return
	}
	idx := m.cursor - 2
	vis := m.visible()
	if idx < 0 || idx >= len(vis) {
		return
	}
	if e := vis[idx]; e.dir {
		m.descend(e.name)
	} else {
		m.toggleAt(idx)
	}
}

func (m *Model) descendRow() {
	idx := m.cursor - 2
	vis := m.visible()
	if idx >= 0 && idx < len(vis) && vis[idx].dir {
		m.descend(vis[idx].name)
	}
}

func (m *Model) toggleAt(idx int) {
	if idx < 0 || idx >= len(m.visible()) {
		return
	}
	abs := m.absAtCursor()
	if abs == "" {
		m.status = "select a local file or folder"
		return
	}
	if m.marked[abs] {
		delete(m.marked, abs)
		m.status = "unmarked " + abs
	} else {
		m.marked[abs] = true
		m.status = "marked " + abs
	}
}

// toggleMark marks or unmarks the entry at the cursor (row 0 = the
// current directory). Both files and folders can be marked.
func (m *Model) toggleMark() {
	if m.cursor == 0 {
		abs := m.current
		if abs == "" {
			m.status = "select a local file or folder"
			return
		}
		if m.marked[abs] {
			delete(m.marked, abs)
			m.status = "unmarked " + abs
		} else {
			m.marked[abs] = true
			m.status = "marked " + abs
		}
		return
	}
	if m.cursor == 1 {
		m.status = "select a local file or folder"
		return
	}
	m.toggleAt(m.cursor - 2)
}

// finish resolves every marked local path to an upload root: mapped
// paths get their resolved remote path, unmapped paths keep an empty
// RemotePath for the caller to fill in via a remote destination picker.
func (m *Model) finish() {
	if len(m.marked) == 0 {
		m.status = "mark at least one file or folder first"
		return
	}
	roots := make([]UploadRoot, 0, len(m.marked))
	for abs := range m.marked {
		remote, ok := ResolveRemotePath(abs, m.mappings, m.localRoot)
		if !ok {
			remote = ""
		}
		roots = append(roots, UploadRoot{LocalPath: abs, RemotePath: remote})
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].LocalPath < roots[j].LocalPath })
	m.uploads = roots
	m.chosen = true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.searchOn {
			return m.handleSearchKey(msg)
		}
		// alt+e opens the editor on the entry under the cursor (plain e
		// is already the exclusion toggle).
		if msg.Alt && string(msg.Runes) == "e" {
			m.requestEdit()
			if m.editPath != "" {
				return m, tea.Quit
			}
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			m.selectRow()
			if m.chosen {
				return m, tea.Quit
			}
		case "s":
			m.finish()
			if m.chosen {
				return m, tea.Quit
			}
		case " ":
			m.toggleMark()
		case "e":
			m.filterOn = !m.filterOn
			if m.filterOn {
				m.status = "exclusions ON — ⊘ matches are skipped"
			} else {
				m.status = "exclusions OFF — everything uploads"
			}
		case "/":
			m.searchOn = true
			m.search.SetValue("") // fresh filter each time
			m.search.Focus()
			m.status = ""
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
			m.clampOffset()
		case "down", "j":
			if m.cursor < m.rowCount()-1 {
				m.cursor++
			}
			m.clampOffset()
		case "left", "backspace", "h":
			m.up()
		case "right", "l":
			m.descendRow()
		case "home", "g":
			m.cursor = 0
			m.clampOffset()
		case "end", "G":
			m.cursor = m.rowCount() - 1
			m.clampOffset()
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "preflight: initializing..."
	}
	var b strings.Builder
	header := " Browse local files to upload"
	if m.atRoot() {
		header += " — pick a mapped folder"
	}
	b.WriteString(headerStyle.Render(header) + "\n")
	if m.atRoot() {
		b.WriteString(faintStyle.Render(" local folders of this project") + "\n")
	} else {
		b.WriteString(faintStyle.Render(" "+m.current) + "\n")
	}
	if m.searchOn {
		m.search.Width = max(20, m.width-4)
		b.WriteString(" " + m.search.View() + "\n")
	}
	rows := m.renderRows()
	vis := max(3, m.height-5)
	if m.offset > 0 {
		b.WriteString(faintStyle.Render(" …") + "\n")
	}
	end := min(len(rows), m.offset+vis)
	for i := m.offset; i < end; i++ {
		b.WriteString(rows[i] + "\n")
	}
	if end < len(rows) {
		b.WriteString(faintStyle.Render(" …") + "\n")
	}
	filterHint := "[E] exclusions OFF"
	if m.filterOn {
		filterHint = "[E] exclusions ON"
	}
	hints := "[↑/↓] move  [Enter] open  [Space] mark file/folder  [S] upload marked  [←] up  " + filterHint + "  [/] filter  [Q] quit"
	if !m.searchOn && m.editorCmd != "" {
		hints += "  [Alt+E] edit"
	}
	if m.searchOn {
		hints = "[↑/↓] move  [Enter] select  [Alt+M] mark  [Esc] exit filter"
		if m.editorCmd != "" {
			hints = "[↑/↓] move  [Enter] select  [Alt+M] mark  [Alt+E] edit  [Esc] exit filter"
		}
	}
	b.WriteString(faintStyle.Render(hints))
	if m.searchOn {
		n := len(m.visible())
		msg := fmt.Sprintf("Filter: %s — %d match", m.search.Value(), n)
		if n != 1 {
			msg += "es"
		}
		if n == 0 {
			msg = fmt.Sprintf("Filter: %s — no matches", m.search.Value())
		}
		b.WriteString("\n" + accentStyle.Render(truncate(msg, m.width)))
	} else if m.status != "" {
		b.WriteString("\n" + accentStyle.Render(truncate(m.status, m.width)))
	} else if len(m.marked) > 0 {
		var names []string
		for s := range m.marked {
			names = append(names, s)
		}
		sort.Strings(names)
		b.WriteString("\n" + accentStyle.Render(fmt.Sprintf("Marked: %s", strings.Join(names, ", "))))
	} else if !m.atRoot() {
		b.WriteString("\n" + faintStyle.Render("Scope: "+m.current))
	}
	return b.String()
}

func (m Model) renderRows() []string {
	rows := make([]string, 0, m.rowCount())
	label := "(select marked)"
	if len(m.marked) == 0 {
		label = "(select marked — nothing marked)"
	}
	if m.cursor == 0 {
		rows = append(rows, accentStyle.Render("▸ "+label))
	} else {
		rows = append(rows, "  "+label)
	}
	if m.cursor == 1 {
		rows = append(rows, "▸ ..")
	} else {
		rows = append(rows, "  ..")
	}
	for i, e := range m.visible() {
		abs := e.name
		if !m.atRoot() {
			abs = filepath.Join(m.current, e.name)
		} else {
			abs = m.virtualEntryAbs(e.name)
		}
		mark := ""
		if m.marked[abs] {
			mark = "✓ "
		}
		name := e.name
		if e.dir {
			name += "/"
		}
		if m.atRoot() {
			// Label each mapping root with its remote target.
			name = mappingLabel(name, m.mappings)
		}
		if m.cursor == i+2 {
			row := "▸ " + mark + name
			if e.excluded {
				row = selectedStyle.Render("▸ ⊘ " + name)
			} else {
				row = selectedStyle.Render(row)
			}
			rows = append(rows, truncate(row, m.width))
			continue
		}
		row := "  " + mark + name
		if e.excluded {
			// Dimmed with a ⊘ marker: matches a project exclusion. Whether
			// it is skipped depends on the e-toggle state.
			row = "  ⊘ " + name
			if !e.dir {
				row = "  ⊘ " + name
			}
			row = faintStyle.Render(row)
		} else if !e.dir {
			row = faintStyle.Render(row)
		}
		rows = append(rows, truncate(row, m.width))
	}
	return rows
}

// mappingEntryName is the display name of a mapping at the virtual root:
// its cleaned local path, or the project-root placeholder.
func mappingEntryName(mapping config.PathMapping) string {
	local := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
	if local == "" || local == "." {
		return "<project root>"
	}
	return local
}

// localBaseAbs is the absolute local path a mapping points at ("" for a
// root mapping, which means the project root itself).
func localBaseAbs(localRoot string, mapping config.PathMapping) string {
	local := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
	if local == "" || local == "." {
		return ""
	}
	return filepath.Join(localRoot, filepath.FromSlash(local))
}

// mappingLabel renders a mapping root with its remote target, e.g.
// "public/  →  /var/www/site/". name carries the trailing slash.
func mappingLabel(name string, mappings []config.PathMapping) string {
	cleaned := cleanName(name)
	for _, mapping := range mappings {
		if mappingEntryName(mapping) == cleaned {
			remotePath := strings.TrimSpace(mapping.RemotePath)
			if remotePath == "" || remotePath == "/" {
				return name
			}
			return name + "  →  " + strings.TrimSuffix(remotePath, "/") + "/"
		}
	}
	return name
}

// cleanName strips the trailing slash used by labels.
func cleanName(name string) string { return strings.TrimSuffix(name, "/") }

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
