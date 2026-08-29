package browser

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"godeploy/internal/pathcomp"

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

type entry struct {
	name string
	dir  bool
}

// searchResult is one live-search match: a root-relative path plus
// whether it is a folder (folders render with a trailing "/" and rank
// before files within the same score tier).
type searchResult struct {
	path string
	dir  bool
}

type searchIndexMsg struct {
	dirs  []string
	files []string
}

type searchDebounceMsg struct{ version uint64 }

type searchResultsMsg struct {
	version uint64
	results []searchResult
	err     error
}

const contentsSearchDebounce = 120 * time.Millisecond

// Model is a local directory browser. It starts at the project's local
// root and lets the user descend into subfolders, then confirm one or
// more paths as sync scopes. Space marks folders and individual files;
// the chosen scopes are paths relative to the project root ("" = the
// root itself), matching the -path flag semantics.
//
// Two variants share the same navigation:
//   - scope mode (New): Space marks folders, finishing returns relative
//     scopes; the r key switches to the remote source browser instead.
//   - directory mode (PickDir): Space is disabled, finishing returns the
//     absolute path of the current folder (used as a download destination).
type Model struct {
	root     string
	current  string
	entries  []entry
	cursor   int
	offset   int // first visible row (scrolling window)
	width    int
	height   int
	selected []string
	chosen   bool
	status   string

	marked map[string]bool // folders to sync, keyed by relative path

	pickDir bool // directory mode: finish returns the absolute current dir

	remoteBrowse bool // user asked to browse the remote source instead
	localUpload  bool // user asked to browse local files for upload instead
	shellMode    bool // user asked to open an ssh shell into the account

	inputMode     bool
	input         textinput.Model
	cands         []string // Tab-completion candidates (relative to root)
	candAt        int
	dirIndex      []string       // every folder under root (search index)
	fileIndex     []string       // every file under root (search index)
	indexing      bool           // recursive index command is still running
	results       []searchResult // live search matches for the typed query
	resultAt      int
	resultOffset  int // first visible result row (scrolling window)
	searchVersion uint64
	searchPending bool
	searching     bool
	searchCancel  context.CancelFunc

	editorCmd  string // config editor command line ("" = editor disabled)
	editorMode bool   // user pressed e: open the editor on editTarget
	editTarget string // absolute local path for the editor
}

// New opens the sync scope browser at the project root. editorCmd is
// the configured editor command line (from config.yaml); empty disables
// the [E] editor keys (they only show a status hint).
func New(root string, editorCmd string) Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "path or filters (%ext, %type, %contents)"
	in.CharLimit = 1024
	m := Model{root: filepath.Clean(root), current: filepath.Clean(root), input: in, marked: map[string]bool{}, editorCmd: editorCmd}
	m.refresh()
	return m
}

// PickDir opens the same browser in directory mode: finishing returns the
// absolute path of the folder under the cursor (or the current folder),
// used as a download destination. Space marking is disabled.
func PickDir(root string) Model {
	m := New(root, "")
	m.pickDir = true
	m.input.Placeholder = "destination path or search filters"
	return m
}

// Selected returns the chosen sync scopes (relative to the project root;
// an empty list means the full project) and whether the user confirmed.
// In directory mode it returns the absolute path of the chosen folder.
func (m Model) Selected() ([]string, bool) { return m.selected, m.chosen }

// RemoteBrowse reports whether the user pressed r to browse the remote
// source instead of picking a sync scope.
func (m Model) RemoteBrowse() bool { return m.remoteBrowse }

// LocalUpload reports whether the user pressed u to browse local files
// for upload instead of picking a sync scope.
func (m Model) LocalUpload() bool { return m.localUpload }

// ShellMode reports whether the user pressed t to open an ssh shell into
// the project's account instead of picking a sync scope.
func (m Model) ShellMode() bool { return m.shellMode }

// MarkedPaths returns the marked sync scopes (root-relative, sorted),
// used to preserve the selection across editor sessions.
func (m Model) MarkedPaths() []string {
	out := make([]string, 0, len(m.marked))
	for s := range m.marked {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// RestoreMarks pre-marks the given root-relative paths (from an earlier
// browser session) so an editor detour does not lose the selection.
func (m *Model) RestoreMarks(paths []string) {
	for _, p := range paths {
		m.marked[p] = true
	}
}

// EditorMode reports whether the user pressed e to open the editor on
// the entry under the cursor; EditTarget is the absolute path to open.
func (m Model) EditorMode() bool { return m.editorMode }

func (m Model) EditTarget() string { return m.editTarget }

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) refresh() {
	entries, err := os.ReadDir(m.current)
	if err != nil {
		m.entries = nil
		if m.pickDir {
			m.status = "cannot read " + m.current + ": " + err.Error()
		} else {
			m.status = "cannot read " + m.current + ": " + err.Error() + " — fix local_root in godeploy -manage or pass -path"
		}
		return
	}
	m.status = ""
	var dirs, files []entry
	for _, e := range entries {
		en := entry{name: e.Name(), dir: e.IsDir()}
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

func (m Model) canUp() bool { return m.current != m.root }

func (m Model) rel() string {
	rel, err := filepath.Rel(m.root, m.current)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (m Model) rowCount() int {
	n := 1 + len(m.entries) // "select this folder" + entries
	if m.canUp() {
		n++
	}
	return n
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

func (m *Model) up() {
	if !m.canUp() {
		return
	}
	m.current = filepath.Dir(m.current)
	m.refresh()
}

func (m *Model) descend(name string) {
	m.current = filepath.Join(m.current, name)
	m.refresh()
}

// selectRow resolves the action for the cursor row: row 0 finishes the
// selection, ".." goes up, a directory descends, a file toggles its mark.
func (m *Model) selectRow() {
	if m.cursor == 0 {
		m.finish()
		return
	}
	off := 1
	if m.canUp() {
		if m.cursor == 1 {
			m.up()
			return
		}
		off = 2
	}
	idx := m.cursor - off
	if idx < 0 || idx >= len(m.entries) {
		return
	}
	if e := m.entries[idx]; e.dir {
		m.descend(e.name)
	} else {
		m.toggleAt(idx)
	}
}

func (m *Model) descendRow() {
	off := 1
	if m.canUp() {
		off = 2
	}
	idx := m.cursor - off
	if idx >= 0 && idx < len(m.entries) && m.entries[idx].dir {
		m.descend(m.entries[idx].name)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case searchIndexMsg:
		m.dirIndex = msg.dirs
		m.fileIndex = msg.files
		m.indexing = false
		if !m.inputMode {
			return m, nil
		}
		return m, m.updateResults()
	case searchDebounceMsg:
		if msg.version != m.searchVersion || !m.inputMode || !m.searchPending {
			return m, nil
		}
		filters, err := parseSearchQuery(m.input.Value())
		if err != nil {
			m.searchPending = false
			m.status = "search: " + err.Error()
			return m, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.searchCancel = cancel
		m.searchPending = false
		m.searching = true
		m.status = "searching file contents…"
		return m, searchContentsCmd(ctx, m.root, m.dirIndex, m.fileIndex, filters, msg.version)
	case searchResultsMsg:
		if msg.version != m.searchVersion {
			return m, nil
		}
		m.searching = false
		m.searchCancel = nil
		if msg.err != nil {
			if msg.err != context.Canceled {
				m.status = "search: " + msg.err.Error()
			}
			return m, nil
		}
		m.setResults(msg.results)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if m.inputMode {
			return m.handleInputKey(msg)
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
			return m, tea.Quit
		case " ":
			m.toggleMark()
		case "r":
			if m.pickDir {
				return m, nil
			}
			m.remoteBrowse = true
			return m, tea.Quit
		case "u":
			if m.pickDir {
				return m, nil
			}
			m.localUpload = true
			return m, tea.Quit
		case "t":
			if m.pickDir {
				return m, nil
			}
			m.shellMode = true
			return m, tea.Quit
		case "e":
			if m.pickDir {
				return m, nil
			}
			switch {
			case m.cursor == 0:
				m.startEditor(m.current)
			case m.cursor == 1 && m.canUp():
				m.startEditor(filepath.Dir(m.current))
			default:
				off := 1
				if m.canUp() {
					off = 2
				}
				idx := m.cursor - off
				if idx >= 0 && idx < len(m.entries) {
					m.startEditor(filepath.Join(m.current, m.entries[idx].name))
				}
			}
			if m.editorMode {
				return m, tea.Quit
			}
		case "/":
			m.inputMode = true
			m.input.SetValue("") // fresh search each time
			m.input.Focus()
			m.resetSearch()
			if m.dirIndex == nil || m.fileIndex == nil {
				if !m.indexing {
					m.indexing = true
					m.status = "indexing project files…"
					return m, indexSearchCmd(m.root)
				}
			}
			return m, m.updateResults()
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

// handleInputKey routes keys while the path input is open. Typing filters
// a live search of every folder under the root; Tab completes bash-style
// (single match fills, several fill the common prefix, then a popup);
// Enter jumps to the selected match or typed folder; Esc returns to list.
// j/k stay query text here (filenames contain them); arrows move the
// popup highlight.
func (m *Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// Modifier keys act on the highlighted entry while a popup (search
	// results or Tab-completion candidates) is open; plain keys keep
	// editing the query. alt+m marks it for sync without leaving the
	// popup; alt+e opens the editor on it. (bubbletea parses ESC+letter
	// reliably; alt+space and shift+space do not arrive as key events.)
	if msg.Alt {
		switch strings.ToLower(string(msg.Runes)) {
		case "m":
			if m.cands != nil {
				m.toggleResultPath(m.cands[m.candAt])
			} else if len(m.results) > 0 {
				m.toggleResult(m.resultAt)
			}
			return *m, nil
		case "e":
			if len(m.results) > 0 {
				m.startEditor(filepath.Join(m.root, filepath.FromSlash(m.results[m.resultAt].path)))
			} else if m.cands != nil {
				m.startEditor(filepath.Join(m.root, filepath.FromSlash(m.cands[m.candAt])))
			}
			if m.editorMode {
				return *m, tea.Quit
			}
			return *m, nil
		}
	}
	if m.cands != nil {
		switch key {
		case "tab", "down":
			m.candAt = (m.candAt + 1) % len(m.cands)
			return *m, nil
		case "shift+tab", "up":
			m.candAt = (m.candAt - 1 + len(m.cands)) % len(m.cands)
			return *m, nil
		case "enter":
			m.input.SetValue(m.cands[m.candAt])
			m.cursorToEnd()
			m.cands = nil
			m.results = nil
			return *m, nil
		case "esc":
			m.cands = nil
			return *m, nil
		default:
			m.cands = nil
		}
	}
	switch key {
	case "esc":
		if m.results != nil {
			m.results = nil
			return *m, nil
		}
		m.exitInput()
		return *m, nil
	case "enter":
		if m.indexing || m.searchPending || m.searching {
			m.status = "search is still running…"
			return *m, nil
		}
		if len(m.results) > 0 {
			m.jump(m.results[m.resultAt].path)
		} else {
			m.jump(m.input.Value())
		}
		return *m, nil
	case "tab":
		m.complete()
		return *m, nil
	case "up":
		if len(m.results) > 0 {
			m.resultAt--
			if m.resultAt < 0 {
				m.resultAt = 0
			}
			m.clampResultOffset()
		}
		return *m, nil
	case "down":
		if len(m.results) > 0 {
			m.resultAt++
			if m.resultAt >= len(m.results) {
				m.resultAt = len(m.results) - 1
			}
			m.clampResultOffset()
		}
		return *m, nil
	default:
		updated, cmd := m.input.Update(msg)
		m.input = updated
		m.status = ""
		searchCmd := m.updateResults()
		if searchCmd != nil {
			return *m, searchCmd
		}
		return *m, cmd
	}
}

// cursorToEnd moves the text cursor to the end of the value (bubbles
// SetValue keeps the old position when the value grows).
func (m *Model) cursorToEnd() {
	m.input.SetCursor(len(m.input.Value()))
}

// toggleResult marks or unmarks the search result at the given index
// (root-relative path — the same key space as list-view marking, so
// dedupeScopes at finish and the ✓ rendering both work unchanged). The
// search stays open so several results can be marked in a row.
func (m *Model) toggleResult(i int) {
	if i < 0 || i >= len(m.results) {
		return
	}
	m.toggleResultPath(m.results[i].path)
}

// toggleResultPath marks or unmarks a root-relative path — a search
// result or a Tab-completion candidate, both of which are relative to
// the project root and share the list view's key space.
func (m *Model) toggleResultPath(rel string) {
	if m.pickDir {
		m.status = "pick a single destination folder"
		return
	}
	// Completion candidates carry a trailing "/" for folders; the
	// marked map (and dedupeScopes) uses bare relative paths.
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" {
		return
	}
	if m.marked[rel] {
		delete(m.marked, rel)
		m.status = "unmarked " + rel
	} else {
		m.marked[rel] = true
		m.status = "marked " + rel
	}
}

// startEditor requests the editor on an absolute local path (file or
// folder). Without a configured editor it only shows a status hint;
// directory mode never opens the editor.
func (m *Model) startEditor(target string) {
	if m.pickDir {
		m.status = "pick a single destination folder"
		return
	}
	if m.editorCmd == "" {
		m.status = "no editor configured — add 'editor' to config.yaml"
		return
	}
	m.editTarget = target
	m.editorMode = true
}

// updateResults applies cheap filters immediately and debounces content I/O.
// The recursive filesystem index itself is always populated by a tea.Cmd, so
// neither opening search nor reading file contents blocks the UI update loop.
func (m *Model) updateResults() tea.Cmd {
	m.cands = nil
	m.cancelSearch()
	m.searchVersion++
	version := m.searchVersion
	query := strings.TrimSpace(m.input.Value())
	if query == "" {
		m.results = nil
		if m.indexing {
			m.status = "indexing project files…"
		} else {
			m.status = ""
		}
		return nil
	}
	if m.dirIndex == nil || m.fileIndex == nil {
		m.results = nil
		m.status = "indexing project files…"
		return nil
	}
	filters, err := parseSearchQuery(query)
	if err != nil {
		m.results = nil
		m.status = "search: " + err.Error()
		return nil
	}
	if len(filters) == 0 {
		m.results = nil
		return nil
	}
	if hasContentsFilter(filters) {
		m.results = nil
		m.searchPending = true
		m.status = "waiting to search file contents…"
		return tea.Tick(contentsSearchDebounce, func(time.Time) tea.Msg {
			return searchDebounceMsg{version: version}
		})
	}
	results, err := applySearchFilters(context.Background(), m.root, m.dirIndex, m.fileIndex, filters)
	if err != nil {
		m.results = nil
		m.status = "search: " + err.Error()
		return nil
	}
	m.setResults(results)
	return nil
}

func (m *Model) setResults(results []searchResult) {
	if len(results) == 0 {
		m.results = nil
	} else {
		m.results = results
	}
	m.resultAt = 0
	m.resultOffset = 0
	if len(results) == 0 {
		m.status = "no matches"
	} else {
		m.status = fmt.Sprintf("%d matches", len(results))
	}
}

func (m *Model) cancelSearch() {
	if m.searchCancel != nil {
		m.searchCancel()
		m.searchCancel = nil
	}
	m.searchPending = false
	m.searching = false
}

func (m *Model) resetSearch() {
	m.cancelSearch()
	m.searchVersion++
	m.cands = nil
	m.results = nil
	m.resultAt = 0
	m.resultOffset = 0
	m.status = ""
}

func indexSearchCmd(root string) tea.Cmd {
	return func() tea.Msg {
		dirs, files := indexAll(root)
		return searchIndexMsg{dirs: dirs, files: files}
	}
}

func searchContentsCmd(ctx context.Context, root string, dirs, files []string, filters []searchFilter, version uint64) tea.Cmd {
	return func() tea.Msg {
		results, err := applySearchFilters(ctx, root, dirs, files, filters)
		return searchResultsMsg{version: version, results: results, err: err}
	}
}

// clampResultOffset keeps the highlighted result inside the popup window.
func (m *Model) clampResultOffset() {
	vis := m.popupLimit()
	if len(m.results) <= vis {
		m.resultOffset = 0
		return
	}
	if m.resultAt < m.resultOffset {
		m.resultOffset = m.resultAt
	}
	if m.resultAt >= m.resultOffset+vis {
		m.resultOffset = m.resultAt - vis + 1
	}
}

// indexAll collects every folder and file under root (relative,
// slash-separated), skipping dot-directories and common dependency
// trees (their files stay hidden from the search too).
func indexAll(root string) ([]string, []string) {
	var dirs, files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			dirs = append(dirs, rel)
		} else {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(dirs)
	sort.Strings(files)
	// Never return nil slices: the browser uses nil to mean "not yet
	// indexed", and an empty tree must not re-trigger indexing.
	if dirs == nil {
		dirs = []string{}
	}
	if files == nil {
		files = []string{}
	}
	return dirs, files
}

// commonPrefix returns the longest string all values share.
func commonPrefix(values []string) string {
	if len(values) == 0 {
		return ""
	}
	prefix := values[0]
	for _, v := range values[1:] {
		for len(prefix) > 0 && !strings.HasPrefix(v, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}

// complete computes Tab-completion candidates for the typed relative path.
func (m *Model) complete() {
	value := m.input.Value()
	if strings.HasPrefix(strings.TrimSpace(value), "%") || strings.Contains(value, "|") {
		m.status = "Tab completion is available for plain path searches"
		return
	}
	m.cancelSearch()
	m.searchVersion++
	rooted := value
	if filepath.IsAbs(rooted) {
		rooted = filepath.Clean(rooted)
	} else if rooted == "" {
		rooted = m.root + string(os.PathSeparator) // list root children
	} else {
		rooted = filepath.Join(m.root, filepath.FromSlash(rooted))
	}
	cands, err := pathcomp.LocalPath(rooted, true)
	if err != nil {
		m.status = "cannot complete: " + err.Error()
		m.cands = nil
		return
	}
	rel := make([]string, 0, len(cands))
	for _, c := range cands {
		r, err := filepath.Rel(m.root, c)
		if err != nil || strings.HasPrefix(r, "..") {
			continue
		}
		r = filepath.ToSlash(r)
		// filepath.Rel cleans the path, dropping the directory marker.
		if strings.HasSuffix(c, string(os.PathSeparator)) && !strings.HasSuffix(r, "/") {
			r += "/"
		}
		rel = append(rel, r)
	}
	switch len(rel) {
	case 0:
		m.status = "no matches"
		m.cands = nil
	case 1:
		m.input.SetValue(rel[0])
		m.cursorToEnd()
		m.cands = nil
		m.results = nil
		m.status = ""
	default:
		// Bash-style: fill the longest common prefix first; only open the
		// picker popup when Tab makes no further progress.
		if prefix := commonPrefix(rel); len(prefix) > len(value) {
			m.input.SetValue(prefix)
			m.cursorToEnd()
			m.cands = nil
			m.results = nil
			m.status = ""
			return
		}
		m.cands = rel
		m.candAt = 0
		m.results = nil
		m.status = ""
	}
}

// jump navigates to a folder given as a path relative to the project root
// (absolute paths inside the root are accepted too).
func (m *Model) jump(value string) {
	rel := filepath.Clean(strings.ReplaceAll(value, "\\", "/"))
	if rel == "." || rel == "/" || rel == "" {
		m.current = m.root
		m.exitInput()
		return
	}
	target := rel
	if !filepath.IsAbs(target) {
		target = filepath.Join(m.root, filepath.FromSlash(rel))
	}
	relToRoot, err := filepath.Rel(m.root, target)
	if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		m.status = "path escapes the project root"
		return
	}
	info, err := os.Stat(target)
	if err != nil {
		m.status = "no such folder: " + rel
		return
	}
	if !info.IsDir() {
		m.status = "that's a file — use Alt+M to mark it for sync"
		return
	}
	m.current = target
	m.refresh()
	m.exitInput()
}

func (m *Model) exitInput() {
	m.cancelSearch()
	m.searchVersion++
	m.inputMode = false
	m.cands = nil
	m.input.Blur()
	m.status = ""
}

// finish confirms the selection. In directory mode the absolute current
// folder is the destination. In scope mode the marked paths win
// (deduplicated; marking the root covers everything), otherwise the
// current folder is the scope.
func (m *Model) finish() {
	if m.pickDir {
		m.selected = []string{m.current}
		m.chosen = true
		return
	}
	if scopes := dedupeScopes(m.marked); len(scopes) > 0 {
		m.selected = scopes
		m.chosen = true
		return
	}
	if m.marked[""] {
		m.selected = nil // full project
		m.chosen = true
		return
	}
	if rel := m.rel(); rel != "" {
		m.selected = []string{rel}
	} else {
		m.selected = nil
	}
	m.chosen = true
}

// toggleMark marks or unmarks the path at the cursor (row 0 = the
// current folder). Both files and folders can be marked. Marking the
// root selects the full project and clears any other marks. Directory
// mode does not support marking.
func (m *Model) toggleMark() {
	if m.pickDir {
		m.status = "pick a single destination folder"
		return
	}
	if m.cursor == 0 {
		rel := m.rel()
		if rel == "" {
			m.marked = map[string]bool{"": true}
			m.status = "full project selected (root)"
			return
		}
		if m.marked[""] {
			delete(m.marked, "")
		}
		if m.marked[rel] {
			delete(m.marked, rel)
			m.status = "unmarked " + rel
		} else {
			m.marked[rel] = true
			m.status = "marked " + rel
		}
		return
	}
	if m.cursor == 1 && m.canUp() {
		m.status = "select a local file or folder"
		return
	}
	off := 1
	if m.canUp() {
		off = 2
	}
	m.toggleAt(m.cursor - off)
}

// toggleAt marks or unmarks the entry at the given list index.
func (m *Model) toggleAt(idx int) {
	if idx < 0 || idx >= len(m.entries) {
		return
	}
	e := m.entries[idx]
	rel := filepath.ToSlash(filepath.Join(m.rel(), e.name))
	if m.marked[rel] {
		delete(m.marked, rel)
		m.status = "unmarked " + rel
	} else {
		m.marked[rel] = true
		m.status = "marked " + rel
	}
}

// dedupeScopes drops scopes covered by a parent scope ("a" covers "a/b")
// and returns the sorted remainder; nil means the full project is covered.
func dedupeScopes(marked map[string]bool) []string {
	if marked[""] {
		return nil
	}
	var scopes []string
	for s := range marked {
		covered := false
		for o := range marked {
			if o != s && strings.HasPrefix(s, o+"/") {
				covered = true
				break
			}
		}
		if !covered {
			scopes = append(scopes, s)
		}
	}
	sort.Strings(scopes)
	return scopes
}

func (m Model) View() string {
	if m.width == 0 {
		return "godeploy: initializing..."
	}
	var b strings.Builder
	header := " Select files / folders to sync"
	if m.pickDir {
		header = " Select download destination"
	}
	b.WriteString(headerStyle.Render(header) + "\n")
	b.WriteString(faintStyle.Render(" "+m.current) + "\n")
	if m.inputMode {
		m.input.Width = max(20, m.width-4)
		b.WriteString(" " + m.input.View() + "\n")
		if m.cands != nil {
			b.WriteString(m.renderCands() + "\n")
		} else if len(m.results) > 0 {
			b.WriteString(m.renderResults() + "\n")
		}
	} else {
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
	}
	hints := "[↑/↓] move  [Enter] open / mark  [Space] mark file/folder  [S] deploy selected  [←] up  [/] search  [Q] quit"
	if m.pickDir {
		hints = "[↑/↓] move  [Enter] open / select  [←] up  [/] search  [Q] quit"
	} else {
		hints += "  [R] browse remote  [U] upload local files  [T] shell  [E] editor"
	}
	if m.inputMode {
		hints = "[|] next filter  [%s/%type/%ext/%contents] filter  [↑/↓] pick  [Enter] open  [Alt+M] mark  [Esc] back"
	}
	b.WriteString(faintStyle.Render(hints))
	if m.status != "" {
		b.WriteString("\n" + accentStyle.Render(truncate(m.status, m.width)))
	} else if m.pickDir {
		b.WriteString("\n" + faintStyle.Render("Destination: "+m.current))
	} else {
		scope := m.rel()
		if scope == "" {
			scope = "full project (root)"
		}
		if len(m.marked) > 0 {
			var names []string
			for s := range m.marked {
				if s == "" {
					names = []string{"full project (root)"}
					break
				}
				names = append(names, s)
			}
			sort.Strings(names)
			b.WriteString("\n" + accentStyle.Render(fmt.Sprintf("Selected: %s", strings.Join(names, ", "))))
		} else {
			b.WriteString("\n" + faintStyle.Render("Scope: "+scope))
		}
	}
	return b.String()
}

func (m *Model) renderCands() string {
	maxShow := m.popupLimit()
	rows := make([]string, 0, min(len(m.cands), maxShow)+1)
	for i, cand := range m.cands {
		if i >= maxShow {
			rows = append(rows, faintStyle.Render(fmt.Sprintf("  … %d more", len(m.cands)-maxShow)))
			break
		}
		marker := "  "
		if i == m.candAt {
			marker = "▸ "
		}
		mark := ""
		if m.marked[strings.TrimSuffix(cand, "/")] {
			mark = "✓ "
		}
		rows = append(rows, marker+mark+truncate(cand, m.width-6))
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

// popupLimit caps completion/search popups to the available screen height.
func (m Model) popupLimit() int {
	return min(8, max(3, m.height-10))
}

func (m *Model) renderResults() string {
	maxShow := m.popupLimit()
	rows := make([]string, 0, maxShow+2)
	if m.resultOffset > 0 {
		rows = append(rows, faintStyle.Render("  …"))
	}
	end := min(len(m.results), m.resultOffset+maxShow)
	for i := m.resultOffset; i < end; i++ {
		marker := "  "
		if i == m.resultAt {
			marker = "▸ "
		}
		r := m.results[i]
		name := r.path
		if r.dir {
			name += "/"
		}
		mark := ""
		if m.marked[r.path] {
			mark = "✓ "
		}
		rows = append(rows, marker+mark+truncate(name, m.width-6))
	}
	if end < len(m.results) {
		rows = append(rows, faintStyle.Render(fmt.Sprintf("  … %d more", len(m.results)-end)))
	}
	return lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (m Model) renderRows() []string {
	rows := make([]string, 0, m.rowCount())
	curRel := m.rel()
	mark := ""
	if m.marked[curRel] {
		mark = "✓ "
	}
	if m.cursor == 0 {
		rows = append(rows, accentStyle.Render("▸ "+mark+"(select this folder)"))
	} else {
		rows = append(rows, "  "+mark+"(select this folder)")
	}
	if m.canUp() {
		if m.cursor == 1 {
			rows = append(rows, "▸ ..")
		} else {
			rows = append(rows, "  ..")
		}
	}
	off := 1
	if m.canUp() {
		off = 2
	}
	for i, e := range m.entries {
		name := e.name
		if e.dir {
			name += "/"
		}
		rowRel := filepath.ToSlash(filepath.Join(m.rel(), e.name))
		mark := ""
		if m.marked[rowRel] {
			mark = "✓ "
		}
		if m.cursor == i+off {
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
