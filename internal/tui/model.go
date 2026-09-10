package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"preflight/internal/diff"
	"preflight/internal/domain"
	"preflight/internal/syncer"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

type ExecuteFunc func(context.Context, []*domain.SyncFileItem) error

// BuildFunc produces the sync plan; the progress callback reports scan
// stages (total <= 0 means the stage has no known end).
type BuildFunc func(onProgress func(stage string, done, total int)) ([]*domain.SyncFileItem, error)

// DiffLoaderFunc generates a diff that was omitted by the normal size guard.
type DiffLoaderFunc func(*domain.SyncFileItem) (*domain.DiffResult, error)

// IgnoreFunc adds a project-relative path to the project's exclusions in
// config.yaml (and to the running ignore engine).
type IgnoreFunc func(projectPath string) error

type diffReloadedMsg struct {
	item    *domain.SyncFileItem
	details *domain.DiffResult
	yOffset int
	err     error
}

type progressMsg struct {
	stage string
	done  int
	total int
}

type scanResultMsg struct {
	items []*domain.SyncFileItem
	err   error
}

type deployResultMsg struct{ err error }

type diffLoadedMsg struct {
	item    *domain.SyncFileItem
	details *domain.DiffResult
	err     error
}

type transferMsg struct {
	snapshot syncer.TransferSnapshot
	states   map[string]syncer.Event
}

type progressSnapshot struct {
	mu    sync.Mutex
	stage string
	done  int
	total int
}

func (s *progressSnapshot) update(stage string, done, total int) {
	s.mu.Lock()
	s.stage, s.done, s.total = stage, done, total
	s.mu.Unlock()
}

func (s *progressSnapshot) get() (string, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stage, s.done, s.total
}

type Model struct {
	projectName string
	remoteRoot  string
	connection  string
	mode        domain.DiffMode

	build    BuildFunc
	execute  ExecuteFunc
	ignore   IgnoreFunc
	loadDiff DiffLoaderFunc
	sink     *syncer.TransferSink

	transferPath  string
	transferDone  int
	transferTotal int
	transferBytes int64
	transferSize  int64
	transferTime  time.Duration

	progressSnap *progressSnapshot
	scanning     bool
	scanErr      error
	scanStage    string
	scanDone     int
	scanTotal    int
	spinner      spinner.Model

	items    []*domain.SyncFileItem
	filtered []int // indices into items (pending-only unless showAll)
	// retainedSkips distinguishes files the user explicitly skipped from
	// unchanged files that started as Skip. Explicit skips stay in the list so
	// an accidental selection can be reversed without rescanning.
	retainedSkips map[*domain.SyncFileItem]struct{}
	showAll       bool
	cursor        int
	offset        int // first visible row

	width        int
	height       int
	focusDiff    bool
	viewport     viewport.Model
	zones        *zone.Manager
	forcedDiffs         map[*domain.SyncFileItem]struct{}
	loadingDiffs        map[*domain.SyncFileItem]struct{}
	currentSections     []diff.ChangeSection
	currentSectionsFile *domain.SyncFileItem
	activeSection       int
	wordWrap            bool

	confirming bool
	running    bool
	deployDone bool
	status     string

	// transferMode runs the review in a single-direction bulk mode: every
	// item is a pre-resolved transfer, sync-only keys (upload/delete/
	// ignore/diff-mode) are disabled, and Space cycles the transfer
	// action with Skip only.
	transferMode TransferMode

	goBack   bool // quit with a request to re-run the project flow
	reBrowse bool // quit with a request to re-browse folders

	transferStates map[string]syncer.Event
}

var (
	headerStyle        = lipgloss.NewStyle().Bold(true)
	selectedStyle      = lipgloss.NewStyle().Bold(true).Reverse(true)
	mutedStyle         = lipgloss.NewStyle().Faint(true)
	errorStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	borderStyle        = lipgloss.NewStyle().Border(lipgloss.NormalBorder())
	focusedBorderStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("12"))
)

// TransferMode selects the review mode: full sync, or a single-direction
// bulk transfer (download-only / upload-only).
type TransferMode int

const (
	TransferSync TransferMode = iota
	TransferDownload
	TransferUpload
)

func New(projectName, remoteRoot, connection string, mode domain.DiffMode, build BuildFunc, execute ExecuteFunc, sink *syncer.TransferSink, ignore IgnoreFunc, transferModes ...TransferMode) Model {
	tm := TransferSync
	if len(transferModes) > 0 {
		tm = transferModes[0]
	}
	m := Model{
		projectName:   projectName,
		remoteRoot:    remoteRoot,
		connection:    connection,
		mode:          mode,
		build:         build,
		execute:       execute,
		ignore:        ignore,
		sink:          sink,
		viewport:      viewport.New(40, 10),
		zones:         zone.New(),
		spinner:       spinner.New(spinner.WithSpinner(spinner.Dot)),
		transferMode:  tm,
		retainedSkips: make(map[*domain.SyncFileItem]struct{}),
		forcedDiffs:   make(map[*domain.SyncFileItem]struct{}),
		loadingDiffs:  make(map[*domain.SyncFileItem]struct{}),
		wordWrap:      true,
	}
	if build != nil {
		m.scanning = true
		m.progressSnap = &progressSnapshot{}
	} else {
		m.rebuildFiltered()
		m.refreshDiff()
	}
	return m
}

// WithDiffLoader enables on-demand generation for diffs omitted by the normal
// size guard.
func (m Model) WithDiffLoader(loader DiffLoaderFunc) Model {
	m.loadDiff = loader
	return m
}

func (m Model) Init() tea.Cmd {
	if m.build == nil {
		return nil
	}
	return tea.Batch(m.spinner.Tick, m.scanCmd(), m.progressTick())
}

func (m Model) scanCmd() tea.Cmd {
	build := m.build
	snap := m.progressSnap
	return func() tea.Msg {
		items, err := build(func(stage string, done, total int) {
			snap.update(stage, done, total)
		})
		return scanResultMsg{items: items, err: err}
	}
}

// progressTick polls the shared snapshot so the UI sees live counts even
// though the scan runs in another goroutine.
func (m Model) progressTick() tea.Cmd {
	snap := m.progressSnap
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		stage, done, total := snap.get()
		return progressMsg{stage: stage, done: done, total: total}
	})
}

// transferTick polls the executor's transfer sink so the status line shows
// which file is being transferred and how many are done.
func (m Model) transferTick() tea.Cmd {
	if m.sink == nil {
		return nil
	}
	sink := m.sink
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return transferMsg{snapshot: sink.Snapshot(), states: sink.States()}
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case progressMsg:
		m.scanStage, m.scanDone, m.scanTotal = msg.stage, msg.done, msg.total
		if m.scanning {
			return m, m.progressTick()
		}
		return m, nil
	case scanResultMsg:
		m.scanning = false
		if msg.err != nil {
			m.scanErr = msg.err
			m.status = "scan failed: " + msg.err.Error()
			return m, nil
		}
		m.items = msg.items
		clear(m.retainedSkips)
		m.rebuildFiltered()
		m.refreshDiff()
		return m, nil
	case transferMsg:
		m.applyTransferSnapshot(msg.snapshot)
		m.transferStates = msg.states
		if m.running {
			return m, m.transferTick()
		}
		return m, nil
	case deployResultMsg:
		m.running = false
		m.deployDone = true
		if m.sink != nil {
			m.applyTransferSnapshot(m.sink.Snapshot())
			m.transferStates = m.sink.States()
		}
		verb := m.verb()
		if msg.err != nil {
			m.status = verb + " failed: " + msg.err.Error()
		} else {
			m.status = verb + " complete"
		}
		if stats := m.transferStats(); stats != "" {
			m.status += " · " + stats
		}
		switch m.transferMode {
		case TransferDownload:
			m.status += " — [r] another project  [b] browse remote again  [q] quit"
		case TransferUpload:
			m.status += " — [r] another project  [b] browse local again  [q] quit"
		default:
			m.status += " — [r] another project  [b] more folders  [q] quit"
		}
	case diffLoadedMsg:
		delete(m.loadingDiffs, msg.item)
		if msg.err != nil {
			m.status = "diff load failed: " + msg.err.Error()
			return m, nil
		}
		msg.item.DiffDetails = msg.details
		m.forcedDiffs[msg.item] = struct{}{}
		m.status = "showing full diff"
		m.refreshDiff()
		return m, nil
	case diffReloadedMsg:
		if msg.err != nil {
			m.status = "diff reload failed: " + msg.err.Error()
			return m, nil
		}
		msg.item.DiffDetails = msg.details
		if msg.details != nil && msg.details.Identical {
			msg.item.Action = domain.ActionSkip
			m.status = fmt.Sprintf("pulled section into %s — file now matches remote (skipped)", displayPath(msg.item))
		} else if msg.details != nil {
			m.status = fmt.Sprintf("pulled section into %s — diff reloaded (%s)", displayPath(msg.item), msg.details.Summary)
		} else {
			m.status = fmt.Sprintf("pulled section into %s", displayPath(msg.item))
		}
		m.rebuildFiltered()
		m.refreshDiffPreserveOffset(msg.yOffset)
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
	case tea.KeyMsg:
		if m.scanning {
			if k := msg.String(); k == "q" || k == "ctrl+c" {
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m.handleKey(msg)
	case tea.MouseMsg:
		if msg.Button == tea.MouseButtonLeft && (msg.Action == tea.MouseActionPress || msg.Action == 0) {
			for i := 0; i < len(m.currentSections); i++ {
				zone := m.zones.Get(fmt.Sprintf("pull-hunk-%d", i))
				if zone != nil && zone.InBounds(msg) {
					return m.pullSection(i)
				}
			}
		}
		if !m.focusDiff {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.moveCursor(-1)
				return m, nil
			case tea.MouseButtonWheelDown:
				m.moveCursor(1)
				return m, nil
			}
			for i := m.offset; i < min(len(m.filtered), m.offset+m.listHeight()); i++ {
				itemIdx := m.filtered[i]
				rowZone := m.zones.Get(fmt.Sprintf("file-%d", itemIdx))
				if rowZone != nil && rowZone.InBounds(msg) {
					m.cursor = i
					m.focusDiff = false
					m.refreshDiff()
					break
				}
			}
		}
	}
	if m.scanning {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	if m.focusDiff {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.confirming {
		switch strings.ToLower(key) {
		case "y":
			m.confirming = false
			return m.startDeploy()
		case "n", "esc":
			m.confirming = false
			m.status = "deployment cancelled"
		}
		return *m, nil
	}
	if m.running {
		if key == "ctrl+c" {
			return *m, tea.Quit
		}
		return *m, nil
	}
	if m.scanErr != nil {
		if key == "q" || key == "ctrl+c" {
			return *m, tea.Quit
		}
		return *m, nil
	}
	switch key {
	case "q", "ctrl+c":
		return *m, tea.Quit
	case "tab":
		m.focusDiff = !m.focusDiff
		m.resize()
	case "v":
		return m.forceCurrentDiff()
	case "p", "P":
		return m.pullActiveSection()
	case "w", "W":
		m.wordWrap = !m.wordWrap
		if m.wordWrap {
			m.status = "word wrap enabled"
		} else {
			m.status = "word wrap disabled (truncated)"
		}
		m.refreshDiff()
		return *m, nil
	case "]":
		if m.focusDiff {
			return m.nextSection()
		}
	case "[":
		if m.focusDiff {
			return m.prevSection()
		}
	case "up", "k":
		if !m.focusDiff {
			m.moveCursor(-1)
		}
	case "down", "j":
		if !m.focusDiff {
			m.moveCursor(1)
		}
	case "pgup":
		if m.focusDiff {
			m.viewport.PageUp()
		} else {
			m.moveCursor(-m.listHeight())
		}
	case "pgdown":
		if m.focusDiff {
			m.viewport.PageDown()
		} else {
			m.moveCursor(m.listHeight())
		}
	case "f":
		m.showAll = !m.showAll
		m.rebuildFiltered()
		m.refreshDiff()
		if m.showAll {
			m.status = "showing all files (including no-diff)"
		} else {
			m.status = "showing pending files and manual skips"
		}
	case " ":
		m.cycleAction()
	case "u":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — every file is " + m.verb() + "ed"
			return *m, nil
		}
		m.setAction(domain.ActionUpload)
	case "d":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — every file is " + m.verb() + "ed"
			return *m, nil
		}
		m.setAction(domain.ActionDownload)
	case "x":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — deletions are not available"
			return *m, nil
		}
		m.cycleDelete()
	case "s":
		m.setAction(domain.ActionSkip)
	case "S":
		m.skipAll()
	case "c":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — nothing to configure"
			return *m, nil
		}
		m.status = "quit and re-run to pick a different subfolder (folder browser)"
	case "m":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — nothing to configure"
			return *m, nil
		}
		m.status = "diff mode is configured per project in config.yaml"
	case "r":
		if m.deployDone {
			m.goBack = true
			return *m, tea.Quit
		}
	case "b":
		if m.deployDone {
			m.reBrowse = true
			return *m, tea.Quit
		}
	case "i":
		if m.transferMode != TransferSync {
			m.status = m.onlyLabel() + " — ignore is not available"
			return *m, nil
		}
		if item := m.currentItem(); item != nil && m.ignore != nil {
			path := displayPath(item)
			if err := m.ignore(path); err != nil {
				m.status = "ignore failed: " + err.Error()
				return *m, nil
			}
			// Drop the file from the review queue; it stays ignored on the
			// next scan because the exclusion was persisted.
			for idx, it := range m.items {
				if it == item {
					delete(m.retainedSkips, item)
					m.items = append(m.items[:idx], m.items[idx+1:]...)
					break
				}
			}
			m.rebuildFiltered()
			m.refreshDiff()
			m.status = "ignored " + path + " — saved to the project's exclusions"
		} else if item != nil {
			m.status = "no ignore callback configured"
		}
	case "enter":
		if m.hasDeletes() {
			m.confirming = true
			return m, nil
		}
		return m.startDeploy()
	}
	return *m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "preflight: initializing..."
	}
	if m.scanning || m.scanErr != nil {
		return m.scanView()
	}
	header := headerStyle.Render(fmt.Sprintf("Project: %s | Target: %s | Mode: %s | Connection: %s", m.projectName, m.remoteRoot, m.mode, m.connection))
	switch m.transferMode {
	case TransferDownload:
		header = headerStyle.Render(fmt.Sprintf("Project: %s | Downloading from: %s | Connection: %s", m.projectName, m.remoteRoot, m.connection))
	case TransferUpload:
		header = headerStyle.Render(fmt.Sprintf("Project: %s | Uploading to: %s | Connection: %s", m.projectName, m.remoteRoot, m.connection))
	}
	toolbar := "Actions: [U]pload [D]ownload [X] Delete [S]kip (Shift+S all) [I]gnore | [P]ull | [W] Wrap | [F]ilter: pending/all | [Tab] Pane | [Space] Cycle | [Enter] Deploy"
	switch m.transferMode {
	case TransferDownload:
		toolbar = "Downloads: [Space] skip file | [Shift+S] skip all | [Enter] Download | [Q] quit"
	case TransferUpload:
		toolbar = "Uploads: [Space] skip file | [Shift+S] skip all | [Enter] Upload | [Q] quit"
	}
	bodyHeight := max(8, m.height-6)
	var body string
	if m.focusDiff {
		switch m.transferMode {
		case TransferDownload:
			toolbar = "Destination: remote file → local path | [Tab] Back to files | [Up/Down] Scroll | [PgUp/PgDown] Page"
		case TransferUpload:
			toolbar = "Destination: local file → remote path | [Tab] Back to files | [Up/Down] Scroll | [PgUp/PgDown] Page"
		default:
			toolbar = "Diff: REMOTE (left) | LOCAL (right) | Click [→] or [P] pull section | [W] Wrap | [Tab] Back to files | [Up/Down] Scroll | [PgUp/PgDown] Page"
		}
		if item := m.currentItem(); m.isOversizedDiff(item) && !m.isForcedDiff(item) {
			toolbar += " | [V] View anyway"
		}
		rightWidth := max(28, m.width-2)
		body = focusedBorderStyle.Width(rightWidth).Height(bodyHeight).Render(m.viewport.View())
	} else {
		leftWidth := max(28, m.width/2-2)
		rightWidth := max(28, m.width-leftWidth-3)
		rows := m.renderListRows(leftWidth - 2)
		left := borderStyle.Width(leftWidth).Height(bodyHeight).Render(strings.Join(rows, "\n"))
		right := borderStyle.Width(rightWidth).Height(bodyHeight).Render(m.viewport.View())
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	status := m.status
	if m.confirming {
		status = "Deletion selected. Confirm deploy? [y/N]"
	} else if m.running {
		verb := "Deploying"
		switch m.transferMode {
		case TransferDownload:
			verb = "Downloading"
		case TransferUpload:
			verb = "Uploading"
		}
		if m.transferPath != "" {
			status = fmt.Sprintf("%s… %s (%d/%d)", verb, m.transferPath, m.transferDone, m.transferTotal)
		} else {
			status = fmt.Sprintf("%s… (%d/%d)", verb, m.transferDone, m.transferTotal)
		}
		status = alignStatus(status, m.transferStats(), m.width)
	} else if status == "" {
		filter := "pending"
		if m.showAll {
			filter = "all"
		}
		status = fmt.Sprintf("showing %d/%d (%s) · %d pending actions", len(m.filtered), len(m.items), filter, pendingCount(m.items))
	}
	return m.zones.Scan(strings.Join([]string{header, toolbar, body, mutedStyle.Render(status)}, "\n"))
}

func (m Model) scanView() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render(fmt.Sprintf("Project: %s | Target: %s | Connection: %s", m.projectName, m.remoteRoot, m.connection)) + "\n\n")
	if m.scanErr != nil {
		b.WriteString(errorStyle.Render("Scan failed: "+m.scanErr.Error()) + "\n\n")
		b.WriteString(mutedStyle.Render("[q] quit"))
		return b.String()
	}
	b.WriteString(m.spinner.View() + " " + m.scanStage)
	if m.scanTotal > 0 {
		b.WriteString(fmt.Sprintf("  %d / %d files", m.scanDone, m.scanTotal))
	} else if m.scanDone > 0 {
		b.WriteString(fmt.Sprintf("  %d files so far", m.scanDone))
	}
	b.WriteString("\n\n" + mutedStyle.Render("[q] quit while scanning"))
	return b.String()
}

func (m *Model) resize() {
	bodyHeight := max(8, m.height-6)
	m.viewport.Height = max(4, bodyHeight-2)
	if m.focusDiff {
		m.viewport.Width = max(20, m.width-4)
	} else {
		leftWidth := max(28, m.width/2-2)
		rightWidth := max(28, m.width-leftWidth-3)
		m.viewport.Width = max(20, rightWidth-2)
	}
	m.refreshDiff()
}

func (m *Model) listHeight() int {
	return max(5, m.height-8)
}

// GoBack reports whether the user asked to return to the project picker
// after a deploy (key r).
func (m Model) GoBack() bool { return m.goBack }

// ReBrowse reports whether the user asked to re-browse folders for the
// same project after a deploy (key b).
func (m Model) ReBrowse() bool { return m.reBrowse }

func (m *Model) renderListRows(width int) []string {
	vis := m.listHeight()
	end := min(len(m.filtered), m.offset+vis)
	rows := make([]string, 0, end-m.offset)
	for i := m.offset; i < end; i++ {
		itemIdx := m.filtered[i]
		item := m.items[itemIdx]
		line := fmt.Sprintf("%-14s %s", actionLabel(item.Action), displayPath(item))
		line = truncate(line, width)
		line = m.zones.Mark(fmt.Sprintf("file-%d", itemIdx), line)
		if state, ok := m.transferStates[displayPath(item)]; ok {
			label := transferLabel(state)
			line = fmt.Sprintf("%-14s %s", label, displayPath(item))
			line = truncate(line, width)
			if state.Err != nil {
				line = errorStyle.Render(line)
			} else {
				line = okStyle.Render(line)
			}
		}
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		rows = append(rows, line)
	}
	if len(rows) == 0 {
		msg := "no files to review"
		if !m.showAll {
			msg += " — press f to show all files"
		}
		rows = append(rows, mutedStyle.Render(msg))
	}
	return rows
}

func (m *Model) rebuildFiltered() {
	filtered := make([]int, 0, len(m.items))
	for i, item := range m.items {
		_, retainedSkip := m.retainedSkips[item]
		if m.showAll || item.Action != domain.ActionSkip || retainedSkip {
			filtered = append(filtered, i)
		}
	}
	m.filtered = filtered
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
	m.clampOffset()
}

func (m *Model) currentItem() *domain.SyncFileItem {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	return m.items[m.filtered[m.cursor]]
}

func (m *Model) moveCursor(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > len(m.filtered)-1 {
		m.cursor = len(m.filtered) - 1
	}
	m.refreshDiff()
	m.clampOffset()
}

func (m *Model) clampOffset() {
	vis := m.listHeight()
	if len(m.filtered) <= vis {
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

func (m *Model) refreshDiff() {
	m.refreshDiffPreserveOffset(0)
	m.viewport.GotoTop()
}

func (m *Model) refreshDiffPreserveOffset(yOffset int) {
	item := m.currentItem()
	if item == nil {
		m.currentSections = nil
		m.currentSectionsFile = nil
		m.activeSection = 0
		m.viewport.SetContent("No differences found.")
		return
	}
	content := displayPath(item) + "\n\n"
	if item.DiffDetails != nil {
		if m.isOversizedDiff(item) && !m.isForcedDiff(item) {
			m.currentSections = nil
			m.currentSectionsFile = nil
			m.activeSection = 0
			if summary := item.DiffDetails.Summary; summary != "" && summary != "(diff too large)" {
				content += summary + "\n\n"
			}
			content += "(diff too large)\n\nPress [v] to view anyway."
		} else if item.DiffDetails.Unified != "" {
			_, sections := parseUnifiedDiff(item.DiffDetails.Unified)
			m.currentSections = sections
			m.currentSectionsFile = item
			if m.activeSection >= len(sections) {
				m.activeSection = max(0, len(sections)-1)
			}
			content = renderSplitDiff(displayPath(item), item.DiffDetails.Summary, item.DiffDetails.Unified, m.viewport.Width, m.wordWrap, m.zones)
		} else {
			m.currentSections = nil
			m.currentSectionsFile = nil
			m.activeSection = 0
			content += item.DiffDetails.Summary
		}
	} else {
		m.currentSections = nil
		m.currentSectionsFile = nil
		m.activeSection = 0
	}
	m.viewport.SetContent(content)
	if yOffset > 0 {
		m.viewport.SetYOffset(yOffset)
	}
}

func (m *Model) pullActiveSection() (tea.Model, tea.Cmd) {
	if len(m.currentSections) == 0 {
		m.status = "no diff sections available to pull"
		return *m, nil
	}
	if m.activeSection < 0 || m.activeSection >= len(m.currentSections) {
		m.activeSection = 0
	}
	return m.pullSection(m.activeSection)
}

func (m *Model) nextSection() (tea.Model, tea.Cmd) {
	if len(m.currentSections) == 0 {
		return *m, nil
	}
	m.activeSection = (m.activeSection + 1) % len(m.currentSections)
	m.status = fmt.Sprintf("section %d of %d", m.activeSection+1, len(m.currentSections))
	return *m, nil
}

func (m *Model) prevSection() (tea.Model, tea.Cmd) {
	if len(m.currentSections) == 0 {
		return *m, nil
	}
	m.activeSection = (m.activeSection - 1 + len(m.currentSections)) % len(m.currentSections)
	m.status = fmt.Sprintf("section %d of %d", m.activeSection+1, len(m.currentSections))
	return *m, nil
}

func (m *Model) pullSection(sectionIdx int) (tea.Model, tea.Cmd) {
	if m.running || m.scanning {
		return *m, nil
	}
	item := m.currentItem()
	if item == nil {
		return *m, nil
	}
	if item.LocalPath == "" {
		m.status = "pull failed: no local path for " + displayPath(item)
		return *m, nil
	}
	if sectionIdx < 0 || sectionIdx >= len(m.currentSections) {
		return *m, nil
	}

	sec := m.currentSections[sectionIdx]
	if err := diff.ApplySection(item.LocalPath, sec); err != nil {
		m.status = "pull failed: " + err.Error()
		return *m, nil
	}

	if info, err := os.Stat(item.LocalPath); err == nil {
		if item.LocalStat == nil {
			item.LocalStat = &domain.FileMeta{Exists: true}
		}
		item.LocalStat.Size = info.Size()
		item.LocalStat.ModTime = info.ModTime()
	}

	yOffset := m.viewport.YOffset
	if m.loadDiff != nil {
		loader := m.loadDiff
		m.status = fmt.Sprintf("pulling section into %s…", displayPath(item))
		return *m, func() tea.Msg {
			details, err := loader(item)
			return diffReloadedMsg{item: item, details: details, yOffset: yOffset, err: err}
		}
	}

	m.status = fmt.Sprintf("pulled section into %s", displayPath(item))
	m.refreshDiffPreserveOffset(yOffset)
	return *m, nil
}

func (m *Model) forceCurrentDiff() (tea.Model, tea.Cmd) {
	item := m.currentItem()
	if !m.isOversizedDiff(item) || m.isForcedDiff(item) {
		return *m, nil
	}
	if item.DiffDetails.Unified != "" {
		m.forcedDiffs[item] = struct{}{}
		m.status = "showing full diff"
		m.refreshDiff()
		return *m, nil
	}
	if _, loading := m.loadingDiffs[item]; loading {
		return *m, nil
	}
	if m.loadDiff == nil {
		m.status = "full diff is unavailable"
		return *m, nil
	}
	m.loadingDiffs[item] = struct{}{}
	m.status = "loading full diff…"
	loader := m.loadDiff
	return *m, func() tea.Msg {
		details, err := loader(item)
		return diffLoadedMsg{item: item, details: details, err: err}
	}
}

func (m *Model) isOversizedDiff(item *domain.SyncFileItem) bool {
	return item != nil && item.DiffDetails != nil &&
		(item.DiffDetails.TooLarge || unifiedDiffLineCount(item.DiffDetails.Unified) > domain.MaxDiffLines)
}

func (m *Model) isForcedDiff(item *domain.SyncFileItem) bool {
	_, forced := m.forcedDiffs[item]
	return forced
}

func unifiedDiffLineCount(unified string) int {
	if unified == "" {
		return 0
	}
	count := strings.Count(unified, "\n")
	if unified[len(unified)-1] != '\n' {
		count++
	}
	return count
}

// afterAction keeps an explicitly skipped row visible so the choice can be
// reversed. Files that were already identical remain hidden by the default
// pending filter.
func (m *Model) afterAction(item *domain.SyncFileItem) {
	if m.retainedSkips == nil {
		m.retainedSkips = make(map[*domain.SyncFileItem]struct{})
	}
	if item.Action == domain.ActionSkip {
		m.retainedSkips[item] = struct{}{}
	} else {
		delete(m.retainedSkips, item)
	}
	m.rebuildFiltered()
	m.refreshDiff()
}

func (m *Model) cycleAction() {
	item := m.currentItem()
	if item == nil {
		return
	}
	if m.transferMode == TransferDownload {
		// Downloads can only be toggled with Skip; anything else would
		// attempt an upload or deletion in a download-only flow.
		if item.Action == domain.ActionDownload {
			item.Action = domain.ActionSkip
		} else {
			item.Action = domain.ActionDownload
		}
		m.afterAction(item)
		return
	}
	if m.transferMode == TransferUpload {
		// Uploads can only be toggled with Skip; anything else would
		// attempt a download or deletion in an upload-only flow.
		if item.Action == domain.ActionUpload {
			item.Action = domain.ActionSkip
		} else {
			item.Action = domain.ActionUpload
		}
		m.afterAction(item)
		return
	}
	switch item.Action {
	case domain.ActionUpload:
		item.Action = domain.ActionDownload
	case domain.ActionDownload:
		item.Action = domain.ActionDeleteRemote
	case domain.ActionDeleteRemote:
		item.Action = domain.ActionSkip
	default:
		item.Action = domain.ActionUpload
	}
	m.afterAction(item)
}

func (m *Model) cycleDelete() {
	item := m.currentItem()
	if item == nil {
		return
	}
	if item.LocalStat != nil && item.LocalStat.Exists && item.RemoteStat != nil && item.RemoteStat.Exists {
		if item.Action == domain.ActionDeleteRemote {
			item.Action = domain.ActionDeleteLocal
		} else {
			item.Action = domain.ActionDeleteRemote
		}
		m.afterAction(item)
		return
	}
	if item.RemoteStat != nil && item.RemoteStat.Exists {
		item.Action = domain.ActionDeleteRemote
	} else if item.LocalStat != nil && item.LocalStat.Exists {
		item.Action = domain.ActionDeleteLocal
	}
	m.afterAction(item)
}

func (m *Model) setAction(action domain.ActionType) {
	item := m.currentItem()
	if item == nil {
		return
	}
	item.Action = action
	m.afterAction(item)
}

func (m *Model) skipAll() {
	if len(m.items) == 0 {
		return
	}
	if m.retainedSkips == nil {
		m.retainedSkips = make(map[*domain.SyncFileItem]struct{})
	}
	for _, item := range m.items {
		item.Action = domain.ActionSkip
		m.retainedSkips[item] = struct{}{}
	}
	m.rebuildFiltered()
	m.refreshDiff()
	m.status = "marked all as skip"
}


func (m Model) hasDeletes() bool {
	for _, item := range m.items {
		if item.Action == domain.ActionDeleteLocal || item.Action == domain.ActionDeleteRemote {
			return true
		}
	}
	return false
}

func (m Model) startDeploy() (tea.Model, tea.Cmd) {
	if m.execute == nil {
		m.status = "no executor configured"
		return m, nil
	}
	m.running = true
	items := m.items
	execute := m.execute
	if m.sink != nil {
		m.sink.Start(items)
	}
	deployCmd := func() tea.Msg {
		return deployResultMsg{err: execute(context.Background(), items)}
	}
	if m.sink != nil {
		return m, tea.Batch(deployCmd, m.transferTick())
	}
	return m, deployCmd
}

func (m *Model) applyTransferSnapshot(snapshot syncer.TransferSnapshot) {
	m.transferPath = snapshot.Path
	m.transferDone = snapshot.Done
	m.transferTotal = snapshot.Total
	m.transferBytes = snapshot.BytesDone
	m.transferSize = snapshot.BytesTotal
	m.transferTime = snapshot.Elapsed
}

func (m Model) transferStats() string {
	if m.transferSize == 0 && m.transferBytes == 0 {
		return ""
	}
	var bytesPerSecond int64
	if m.transferTime > 0 {
		bytesPerSecond = int64(float64(m.transferBytes) / m.transferTime.Seconds())
	}
	return fmt.Sprintf(
		"%s / %s · %s/s",
		formatBytes(m.transferBytes),
		formatBytes(m.transferSize),
		formatBytes(bytesPerSecond),
	)
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	units := [...]string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}

func alignStatus(left, right string, width int) string {
	if right == "" || width <= 0 {
		return left
	}
	const gap = 2
	rightWidth := lipgloss.Width(right)
	availableLeft := width - rightWidth - gap
	if availableLeft < 12 {
		return truncate(left, width)
	}
	left = truncate(left, availableLeft)
	padding := width - lipgloss.Width(left) - rightWidth
	return left + strings.Repeat(" ", padding) + right
}

// verb returns the lowercase transfer verb for the current mode
// (deploy for full sync).
func (m Model) verb() string {
	switch m.transferMode {
	case TransferDownload:
		return "download"
	case TransferUpload:
		return "upload"
	default:
		return "deploy"
	}
}

// onlyLabel is the mode label used in blocked-key status messages.
func (m Model) onlyLabel() string {
	switch m.transferMode {
	case TransferDownload:
		return "download-only mode"
	case TransferUpload:
		return "upload-only mode"
	default:
		return ""
	}
}

// transferLabel renders the completed state of a transferred file.
func transferLabel(state syncer.Event) string {
	switch state.Action {
	case domain.ActionUpload:
		return "[✓ Uploaded]"
	case domain.ActionDownload:
		return "[✓ Downloaded]"
	case domain.ActionDeleteRemote, domain.ActionDeleteLocal:
		return "[✓ Deleted]"
	default:
		return "[✓ Done]"
	}
}

// displayPath renders the file's path in the review UI, qualified by its
// scope when the deploy covers multiple folders.
func displayPath(item *domain.SyncFileItem) string {
	scope := strings.TrimSuffix(item.Scope, "/")
	rel := strings.TrimPrefix(item.RelativePath, "/")
	rel = strings.TrimSuffix(rel, "/")
	if scope == "" {
		return rel
	}
	if rel == "" {
		return scope
	}
	return scope + "/" + rel
}


func actionLabel(action domain.ActionType) string {
	switch action {
	case domain.ActionUpload:
		return "[→ Upload]"
	case domain.ActionDownload:
		return "[← Download]"
	case domain.ActionDeleteRemote:
		return "[✕ Del Rem]"
	case domain.ActionDeleteLocal:
		return "[✕ Del Loc]"
	default:
		return "[⊘ Skip]"
	}
}

func pendingCount(items []*domain.SyncFileItem) int {
	count := 0
	for _, item := range items {
		if item.Action != domain.ActionSkip {
			count++
		}
	}
	return count
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
