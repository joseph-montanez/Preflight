package remotebrowser

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"godeploy/internal/config"
	"godeploy/internal/domain"

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

// DownloadRoot is one selected download target: an absolute remote path
// plus the local path it resolves to. LocalPath is empty when the remote
// path is not covered by a path mapping and the caller must ask for a
// destination (then the full remote path is preserved under the picked
// base).
type DownloadRoot struct {
	RemotePath string
	LocalPath  string
}

type entry struct {
	name string
	dir  bool
}

// Model browses the remote file system through a RemoteClient. Unlike the
// sync folder browser it shows every file (ignore rules never apply here)
// and marks both files and folders; marking a folder means downloading its
// whole subtree.
//
// Navigation starts at a virtual root listing the project's mapping roots
// (each labeled with its local mapping). ".." from there climbs to "/",
// so anything on the server can be reached and downloaded even when it is
// outside every mapping.
type Model struct {
	client    domain.RemoteClient
	mappings  []config.PathMapping
	localRoot string

	// current is the absolute remote path being viewed; "" is the virtual
	// root showing the mapping roots.
	current   string
	entries   []entry
	cursor    int
	offset    int // first visible row (scrolling window)
	width     int
	height    int
	status    string
	marked    map[string]bool // absolute remote paths to download
	chosen    bool
	downloads []DownloadRoot

	// pickDir is the single-select destination mode used by the local
	// upload flow: finishing returns the absolute current dir (Space
	// marking disabled, no virtual root).
	pickDir bool
	picked  string

	// filterOn is the "/" name filter: typing narrows the current
	// folder's listing (files and folders, never recursive); Enter acts
	// on the highlighted entry, Esc restores the full listing.
	filterOn bool
	input    textinput.Model

	// editorCmd is the configured editor command line ("" disables the
	// edit keys, which then only show a status hint). editPath is set
	// when the user presses e/alt+e on a remote file: the caller
	// downloads it, runs the editor, and re-opens the browser.
	editorCmd string
	editPath  string
}

func newInput() textinput.Model {
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "filter names"
	in.CharLimit = 256
	return in
}

func New(client domain.RemoteClient, mappings []config.PathMapping, localRoot string, editorCmd string) Model {
	m := Model{
		client:    client,
		mappings:  mappings,
		localRoot: localRoot,
		marked:    map[string]bool{},
		input:     newInput(),
		editorCmd: editorCmd,
	}
	m.load()
	return m
}

// PickRemoteDir opens the same browser in single-select destination
// mode: finishing returns the absolute path of the folder under the
// cursor (or the current folder), used as an upload destination. Space
// marking is disabled and the view starts at start (""/"/" for the
// remote root).
func PickRemoteDir(client domain.RemoteClient, start string) Model {
	m := Model{
		client:  client,
		marked:  map[string]bool{},
		pickDir: true,
		current: cleanRemote(start),
		input:   newInput(),
	}
	m.load()
	return m
}

// Selected returns the resolved download roots and whether the user
// confirmed a selection.
func (m Model) Selected() ([]DownloadRoot, bool) { return m.downloads, m.chosen }

// PickedDir returns the destination folder chosen in pick mode and
// whether the user confirmed.
func (m Model) PickedDir() (string, bool) { return m.picked, m.chosen }

// EditRequest returns the remote path the user asked to edit (e / alt+e
// on a remote file), or "". The caller downloads the file, runs the
// editor, and re-opens the browser with the marks restored.
func (m Model) EditRequest() string { return m.editPath }

// MarkedPaths returns the marked absolute remote paths (sorted), used
// to preserve the selection across edit sessions.
func (m Model) MarkedPaths() []string {
	out := make([]string, 0, len(m.marked))
	for s := range m.marked {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// RestoreMarks pre-marks the given absolute remote paths (from an
// earlier browser session) so an editor detour does not lose the
// selection.
func (m *Model) RestoreMarks(paths []string) {
	for _, p := range paths {
		m.marked[p] = true
	}
}

// Mark selects an absolute remote path for download. It exists for tests
// and programmatic use; the TUI marks via Space.
func (m *Model) Mark(abs string) { m.marked[abs] = true }

// Finish resolves the marked paths into download roots (same as pressing
// s in the TUI).
func (m *Model) Finish() { m.finish() }

func (m Model) Init() tea.Cmd { return nil }

// atRoot reports whether the browser shows the virtual mapping-root view.
func (m Model) atRoot() bool { return m.current == "" }

// up navigates one level: the virtual root and "/" link to each
// other, so every level (mapping roots, filesystem root, anything below)
// is reachable from any other. Pick mode has no virtual root: "/" is
// the top.
func (m *Model) up() {
	switch {
	case m.current == "":
		m.current = "/"
	case m.current == "/":
		if m.pickDir {
			return
		}
		m.current = ""
	default:
		m.current = path.Dir(m.current)
	}
	m.load()
}

// load refreshes the current view from the server. At the virtual root it
// synthesizes the mapping roots; anywhere else it lists the directory.
func (m *Model) load() {
	m.exitFilter()
	if m.current == "" {
		m.entries = nil
		for _, mapping := range m.mappings {
			m.entries = append(m.entries, entry{name: cleanRemote(mapping.RemotePath), dir: true})
		}
		m.status = ""
		m.cursor = 0
		m.offset = 0
		return
	}
	infos, err := m.client.ReadDir(m.current)
	if err != nil {
		m.entries = nil
		m.status = "cannot read " + m.current + ": " + err.Error()
		return
	}
	m.status = ""
	var dirs, files []entry
	for _, info := range infos {
		if info.Name() == "." || info.Name() == ".." {
			continue
		}
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
	m.entries = append(dirs, files...)
	m.cursor = 0
	m.offset = 0
}

func (m *Model) descend(name string) {
	if m.atRoot() {
		m.current = name
	} else {
		m.current = path.Join(m.current, name)
	}
	m.load()
}

// abs returns the absolute remote path of the entry at the cursor (or the
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
		return e.name
	}
	return path.Join(m.current, e.name)
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
	if !m.filterOn || m.input.Value() == "" {
		return m.entries
	}
	q := strings.ToLower(m.input.Value())
	out := make([]entry, 0, len(m.entries))
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.name), q) {
			out = append(out, e)
		}
	}
	return out
}

// exitFilter closes the "/" filter; the full listing reappears and the
// cursor keeps its position.
func (m *Model) exitFilter() {
	m.filterOn = false
	m.input.Blur()
}

// handleFilterKey routes keys while the "/" name filter is open. Typing
// narrows the current folder's listing; ↑/↓ move through the matches;
// Enter acts on the highlighted entry exactly as in the full listing
// (descend into a folder, toggle a file mark, finish in pick mode);
// Esc closes the filter. Other keys (including q and ctrl+c) are query
// text while the filter is open.
// requestEdit asks the caller to open the editor on a remote file. Only
// files can be edited remotely (a folder would mean a full subtree
// download); with no editor configured it only shows a status hint.
func (m *Model) requestEdit() {
	if m.pickDir {
		m.status = "pick a single destination folder"
		return
	}
	if m.editorCmd == "" {
		m.status = "no editor configured — add 'editor' to config.yaml"
		return
	}
	e, ok := m.entryAtCursor()
	if !ok {
		m.status = "edit a file, not a folder"
		return
	}
	if e.dir {
		m.status = "edit a file, not a folder"
		return
	}
	m.editPath = m.absAtCursor()
}

func (m Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Modifier keys act on the highlighted entry while the filter is
	// open (plain keys keep editing the query): alt+m toggles its mark
	// (same as Space in the full listing), alt+e opens the editor on a
	// file. (bubbletea parses ESC+letter reliably; alt+space and
	// shift+space do not arrive as key events.)
	if msg.Alt {
		switch strings.ToLower(string(msg.Runes)) {
		case "m":
			m.toggleMark()
			return m, nil
		case "e":
			m.requestEdit()
			if m.editPath != "" {
				return m, tea.Quit
			}
			return m, nil
		}
	}
	switch msg.String() {
	case "esc":
		m.exitFilter()
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
		updated, cmd := m.input.Update(msg)
		m.input = updated
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
// selection, ".." goes up, a directory descends, a file toggles its mark
// (or is a hint in pick mode).
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
	} else if m.pickDir {
		m.status = "pick a folder, not a file"
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
		m.status = "select a remote file or folder"
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
// current directory). Both files and folders can be marked. Pick mode
// does not support marking.
func (m *Model) toggleMark() {
	if m.pickDir {
		m.status = "pick a single destination folder"
		return
	}
	if m.cursor == 0 {
		abs := m.current
		if abs == "" {
			m.status = "select a remote file or folder"
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
		m.status = "select a remote file or folder"
		return
	}
	m.toggleAt(m.cursor - 2)
}

// finish resolves every marked remote path to a download root: mapped
// paths get their project-relative local path, unmapped paths keep an
// empty LocalPath for the caller to fill in via a destination picker.
// In pick mode it returns the absolute current directory instead.
func (m *Model) finish() {
	if m.pickDir {
		if m.current == "" {
			m.status = "select a destination folder first"
			return
		}
		m.picked = m.current
		m.chosen = true
		return
	}
	if len(m.marked) == 0 {
		m.status = "mark at least one file or folder first"
		return
	}
	roots := make([]DownloadRoot, 0, len(m.marked))
	for abs := range m.marked {
		local, ok := ResolveLocalPath(abs, m.mappings, m.localRoot)
		if !ok {
			local = ""
		}
		roots = append(roots, DownloadRoot{RemotePath: abs, LocalPath: local})
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].RemotePath < roots[j].RemotePath })
	m.downloads = roots
	m.chosen = true
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.filterOn {
			return m.handleFilterKey(msg)
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
			m.requestEdit()
			if m.editPath != "" {
				return m, tea.Quit
			}
		case "/":
			m.filterOn = true
			m.input.SetValue("") // fresh filter each time
			m.input.Focus()
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
		return "godeploy: initializing..."
	}
	var b strings.Builder
	header := " Browse remote source"
	if m.pickDir {
		header = " Select upload destination"
	} else if m.atRoot() {
		header += " — pick a mapped root"
	}
	b.WriteString(headerStyle.Render(header) + "\n")
	if m.atRoot() {
		b.WriteString(faintStyle.Render(" remote roots of this project") + "\n")
	} else {
		b.WriteString(faintStyle.Render(" "+m.current) + "\n")
	}
	if m.filterOn {
		m.input.Width = max(20, m.width-4)
		b.WriteString(" " + m.input.View() + "\n")
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
	hints := "[↑/↓] move  [Enter] open  [Space] mark file/folder  [S] download marked  [←] up  [/] filter  [Q] quit"
	if m.pickDir {
		hints = "[Enter] select this folder  [↑/↓] move  [←] up  [/] filter  [Q] quit"
	} else if m.editorCmd != "" {
		hints += "  [E] edit file"
	}
	if m.filterOn {
		hints = "[↑/↓] move  [Enter] select  [Esc] exit filter"
		if !m.pickDir {
			hints = "[↑/↓] move  [Enter] select  [Alt+M] mark  [Esc] exit filter"
			if m.editorCmd != "" {
				hints = "[↑/↓] move  [Enter] select  [Alt+M] mark  [Alt+E] edit  [Esc] exit filter"
			}
		}
	}
	b.WriteString(faintStyle.Render(hints))
	if m.filterOn {
		n := len(m.visible())
		msg := fmt.Sprintf("Filter: %s — %d match", m.input.Value(), n)
		if n != 1 {
			msg += "es"
		}
		if n == 0 {
			msg = fmt.Sprintf("Filter: %s — no matches", m.input.Value())
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
	if m.pickDir {
		label = "(select this folder)"
	} else if len(m.marked) == 0 {
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
			abs = path.Join(m.current, e.name)
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
			// Label each mapping root with its local destination.
			name = mappingLabel(name, m.mappings)
		}
		if m.cursor == i+2 {
			rows = append(rows, selectedStyle.Render(truncate("▸ "+mark+name, m.width)))
			continue
		}
		label := "  " + mark + name
		if !e.dir {
			label = faintStyle.Render(label)
		}
		rows = append(rows, truncate(label, m.width))
	}
	return rows
}

// mappingLabel renders a mapping root with its local mapping, e.g.
// "/var/www/api-backend/ → public/".
func mappingLabel(remoteRoot string, mappings []config.PathMapping) string {
	for _, mapping := range mappings {
		if cleanRemote(mapping.RemotePath) == cleanRemote(remoteRoot) {
			localPath := strings.Trim(strings.ReplaceAll(mapping.LocalPath, "\\", "/"), "/")
			if localPath == "" || localPath == "." {
				return remoteRoot + "  →  <project root>"
			}
			return remoteRoot + "  →  " + localPath + "/"
		}
	}
	return remoteRoot
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
