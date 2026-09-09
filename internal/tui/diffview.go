package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"preflight/internal/diff"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
)

type diffCellKind uint8

const (
	diffCellEmpty diffCellKind = iota
	diffCellContext
	diffCellRemoved
	diffCellAdded
)

type numberedDiffLine struct {
	number int
	text   string
}

type splitDiffRow struct {
	oldNumber      int
	newNumber      int
	oldText        string
	newText        string
	oldKind        diffCellKind
	newKind        diffCellKind
	hunk           string
	sectionIndex   int
	isSectionStart bool
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$`)

var (
	syntaxStyle       = styles.Get("monokai")
	removedBackground = lipgloss.Color("#3A2027")
	addedBackground   = lipgloss.Color("#173B2C")
	removedGutter     = lipgloss.Color("#6B2737")
	addedGutter       = lipgloss.Color("#1F633F")
	remoteHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#7A293A"))
	localHeaderStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#216E45"))
	pullBtnStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#0E639C"))
	revertBtnStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#7A293A"))
)

// parseUnifiedDiff converts a unified diff into aligned remote/local rows and
// extracts discrete change sections that can be pulled individually.
func parseUnifiedDiff(unified string) ([]splitDiffRow, []diff.ChangeSection) {
	lines := strings.Split(strings.ReplaceAll(unified, "\r\n", "\n"), "\n")
	rows := make([]splitDiffRow, 0, len(lines))
	var sections []diff.ChangeSection
	var removed, added []numberedDiffLine
	oldNumber, newNumber := 0, 0
	inHunk := false
	sectionIdx := 0

	flushChanges := func() {
		if len(removed) == 0 && len(added) == 0 {
			return
		}
		sec := diff.ChangeSection{
			Index:           sectionIdx,
			LocalLineCount:  len(added),
			RemoteLineCount: len(removed),
		}
		if len(added) > 0 {
			sec.LocalStartLine = added[0].number
			sec.LocalLines = make([]string, len(added))
			for i, l := range added {
				sec.LocalLines[i] = l.text
			}
		} else {
			sec.LocalStartLine = newNumber
		}
		if len(removed) > 0 {
			sec.RemoteStartLine = removed[0].number
			sec.RemoteLines = make([]string, len(removed))
			for i, l := range removed {
				sec.RemoteLines[i] = l.text
			}
		} else {
			sec.RemoteStartLine = oldNumber
		}
		sections = append(sections, sec)

		count := max(len(removed), len(added))
		for i := 0; i < count; i++ {
			row := splitDiffRow{
				oldKind:        diffCellEmpty,
				newKind:        diffCellEmpty,
				sectionIndex:   sectionIdx,
				isSectionStart: i == 0,
			}
			if i < len(removed) {
				row.oldNumber = removed[i].number
				row.oldText = removed[i].text
				row.oldKind = diffCellRemoved
			}
			if i < len(added) {
				row.newNumber = added[i].number
				row.newText = added[i].text
				row.newKind = diffCellAdded
			}
			rows = append(rows, row)
		}
		sectionIdx++
		removed = removed[:0]
		added = added[:0]
	}

	for _, line := range lines {
		if matches := hunkHeaderPattern.FindStringSubmatch(line); matches != nil {
			flushChanges()
			oldNumber, _ = strconv.Atoi(matches[1])
			newNumber, _ = strconv.Atoi(matches[2])
			inHunk = true
			rows = append(rows, splitDiffRow{hunk: line, sectionIndex: -1})
			continue
		}
		if !inHunk || line == `\ No newline at end of file` {
			continue
		}
		if line == "" {
			// strings.Split adds one empty item for the final newline. An actual
			// blank diff line still has a leading context/add/remove marker.
			continue
		}
		switch line[0] {
		case ' ':
			flushChanges()
			rows = append(rows, splitDiffRow{
				oldNumber:    oldNumber,
				newNumber:    newNumber,
				oldText:      line[1:],
				newText:      line[1:],
				oldKind:      diffCellContext,
				newKind:      diffCellContext,
				sectionIndex: -1,
			})
			oldNumber++
			newNumber++
		case '-':
			removed = append(removed, numberedDiffLine{number: oldNumber, text: line[1:]})
			oldNumber++
		case '+':
			added = append(added, numberedDiffLine{number: newNumber, text: line[1:]})
			newNumber++
		}
	}
	flushChanges()
	return rows, sections
}

func wrapLine(source string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	source = expandTabs(source, 4)
	if lipgloss.Width(source) <= width {
		return []string{source}
	}

	var chunks []string
	runes := []rune(source)
	start := 0

	for start < len(runes) {
		curWidth := 0
		end := start
		lastBreak := -1

		for end < len(runes) {
			rw := lipgloss.Width(string(runes[end]))
			if curWidth+rw > width {
				break
			}
			curWidth += rw
			if runes[end] == ' ' || runes[end] == '\t' || runes[end] == ',' || runes[end] == '(' || runes[end] == ')' || runes[end] == '.' || runes[end] == ';' {
				lastBreak = end + 1
			}
			end++
		}

		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}

		if lastBreak > start && (lastBreak-start) >= max(1, width/3) {
			chunks = append(chunks, string(runes[start:lastBreak]))
			start = lastBreak
		} else {
			chunks = append(chunks, string(runes[start:end]))
			start = end
		}
	}

	return chunks
}

func renderSplitDiff(path, summary, unified string, width int, wrap bool, zm ...*zone.Manager) string {
	var zones *zone.Manager
	if len(zm) > 0 {
		zones = zm[0]
	}
	width = max(20, width)
	lexer := lexers.Match(path)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	language := lexer.Config().Name

	var out strings.Builder
	out.WriteString(headerStyle.Render(fitPlain(path, width)))
	out.WriteByte('\n')
	meta := summary
	if meta != "" {
		meta += "  ·  "
	}
	meta += "syntax: " + language
	out.WriteString(mutedStyle.Render(fitPlain(meta, width)))
	out.WriteString("\n\n")

	separator := " │ "
	leftWidth := (width - lipgloss.Width(separator)) / 2
	rightWidth := width - lipgloss.Width(separator) - leftWidth
	out.WriteString(renderLabel("REMOTE", leftWidth, remoteHeaderStyle))
	out.WriteString(mutedStyle.Render(separator))
	out.WriteString(renderLabel("LOCAL", rightWidth, localHeaderStyle))
	out.WriteByte('\n')

	rows, sections := parseUnifiedDiff(unified)
	if len(rows) == 0 {
		out.WriteString(mutedStyle.Render("No line-level text diff is available."))
		return out.String()
	}

	const gutterWidth = 7
	for _, row := range rows {
		if row.hunk != "" {
			out.WriteString(mutedStyle.Render(fitPlain("── "+row.hunk+" ", width)))
			out.WriteByte('\n')
			continue
		}

		leftCodeWidth := max(1, leftWidth-gutterWidth)
		rightCodeWidth := max(1, rightWidth-gutterWidth)

		var leftChunks []string
		if row.oldKind == diffCellEmpty {
			leftChunks = []string{""}
		} else if wrap {
			leftChunks = wrapLine(row.oldText, leftCodeWidth)
		} else {
			leftChunks = []string{fitPlain(expandTabs(row.oldText, 4), leftCodeWidth)}
		}

		var rightChunks []string
		if row.newKind == diffCellEmpty {
			rightChunks = []string{""}
		} else if wrap {
			rightChunks = wrapLine(row.newText, rightCodeWidth)
		} else {
			rightChunks = []string{fitPlain(expandTabs(row.newText, 4), rightCodeWidth)}
		}

		subRows := max(len(leftChunks), len(rightChunks))
		if subRows < 1 {
			subRows = 1
		}

		for k := 0; k < subRows; k++ {
			// Left side
			if k == 0 {
				if k < len(leftChunks) {
					out.WriteString(renderDiffCell(row.oldNumber, leftChunks[k], row.oldKind, leftWidth, true, lexer))
				} else {
					out.WriteString(renderDiffCell(0, "", diffCellEmpty, leftWidth, true, lexer))
				}
			} else {
				if k < len(leftChunks) {
					out.WriteString(renderDiffCell(0, leftChunks[k], row.oldKind, leftWidth, false, lexer))
				} else {
					out.WriteString(renderDiffCell(0, "", row.oldKind, leftWidth, false, lexer))
				}
			}

			// Center separator
			if k == 0 {
				out.WriteString(renderSeparator(row, sections, zones))
			} else {
				out.WriteString(mutedStyle.Render(" ┆ "))
			}

			// Right side
			if k == 0 {
				if k < len(rightChunks) {
					out.WriteString(renderDiffCell(row.newNumber, rightChunks[k], row.newKind, rightWidth, true, lexer))
				} else {
					out.WriteString(renderDiffCell(0, "", diffCellEmpty, rightWidth, true, lexer))
				}
			} else {
				if k < len(rightChunks) {
					out.WriteString(renderDiffCell(0, rightChunks[k], row.newKind, rightWidth, false, lexer))
				} else {
					out.WriteString(renderDiffCell(0, "", row.newKind, rightWidth, false, lexer))
				}
			}
			out.WriteByte('\n')
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func renderSeparator(row splitDiffRow, sections []diff.ChangeSection, zones *zone.Manager) string {
	if row.sectionIndex < 0 || row.sectionIndex >= len(sections) {
		return mutedStyle.Render(" │ ")
	}
	sec := sections[row.sectionIndex]
	if row.isSectionStart {
		btn := "[→]"
		style := pullBtnStyle
		if sec.RemoteLineCount == 0 && sec.LocalLineCount > 0 {
			btn = "[✕]"
			style = revertBtnStyle
		}
		renderedBtn := style.Render(btn)
		if zones != nil {
			renderedBtn = zones.Mark(fmt.Sprintf("pull-hunk-%d", sec.Index), renderedBtn)
		}
		return renderedBtn
	}
	return mutedStyle.Render(" ┆ ")
}

func renderLabel(label string, width int, style lipgloss.Style) string {
	return style.Render(padPlain("  "+label, width))
}

func renderDiffCell(number int, source string, kind diffCellKind, width int, isFirstSubline bool, lexer chroma.Lexer) string {
	if width <= 0 {
		return ""
	}
	const gutterWidth = 7
	if width <= gutterWidth {
		return mutedStyle.Render(strings.Repeat(" ", width))
	}

	marker := " "
	var background lipgloss.TerminalColor
	var gutterBackground lipgloss.TerminalColor
	switch kind {
	case diffCellRemoved:
		marker, background, gutterBackground = "−", removedBackground, removedGutter
	case diffCellAdded:
		marker, background, gutterBackground = "+", addedBackground, addedGutter
	}
	numberText := ""
	if isFirstSubline && number > 0 {
		numberText = strconv.Itoa(number)
	}
	if !isFirstSubline {
		marker = " "
	}
	gutter := fmt.Sprintf("%s%4s  ", marker, numberText)
	gutterStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E6E6E6"))
	if gutterBackground != nil {
		gutterStyle = gutterStyle.Background(gutterBackground)
	}

	codeWidth := width - gutterWidth
	if kind == diffCellEmpty {
		return gutterStyle.Render(gutter) + strings.Repeat(" ", codeWidth)
	}
	source = fitPlain(source, codeWidth)
	plainWidth := lipgloss.Width(source)
	highlighted := ""
	if source != "" {
		highlighted = highlightCode(source, lexer, background)
	}
	paddingStyle := lipgloss.NewStyle()
	if background != nil {
		paddingStyle = paddingStyle.Background(background)
	}
	return gutterStyle.Render(gutter) + highlighted + paddingStyle.Render(strings.Repeat(" ", max(0, codeWidth-plainWidth)))
}

func highlightCode(source string, lexer chroma.Lexer, background lipgloss.TerminalColor) string {
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		style := lipgloss.NewStyle()
		if background != nil {
			style = style.Background(background)
		}
		return style.Render(source)
	}
	var out strings.Builder
	for token := iterator(); token != chroma.EOF; token = iterator() {
		val := strings.ReplaceAll(token.Value, "\r", "")
		val = strings.ReplaceAll(val, "\n", "")
		if val == "" {
			continue
		}
		entry := syntaxStyle.Get(token.Type)
		style := lipgloss.NewStyle()
		if entry.Colour.IsSet() {
			style = style.Foreground(lipgloss.Color(entry.Colour.String()))
		}
		if background != nil {
			style = style.Background(background)
		}
		style = style.Bold(entry.Bold == chroma.Yes).Italic(entry.Italic == chroma.Yes).Underline(entry.Underline == chroma.Yes)
		out.WriteString(style.Render(val))
	}
	return out.String()
}

func expandTabs(value string, tabWidth int) string {
	var out strings.Builder
	column := 0
	for _, r := range value {
		if r == '\t' {
			spaces := tabWidth - column%tabWidth
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		out.WriteRune(r)
		column += lipgloss.Width(string(r))
	}
	return out.String()
}

func fitPlain(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func padPlain(value string, width int) string {
	value = fitPlain(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}
