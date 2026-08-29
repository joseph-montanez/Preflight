package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
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
	oldNumber int
	newNumber int
	oldText   string
	newText   string
	oldKind   diffCellKind
	newKind   diffCellKind
	hunk      string
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
)

// parseUnifiedDiff converts a unified diff into aligned remote/local rows.
// Consecutive removals and additions are paired, so a replacement appears on
// one row instead of as two unrelated blocks.
func parseUnifiedDiff(unified string) []splitDiffRow {
	lines := strings.Split(strings.ReplaceAll(unified, "\r\n", "\n"), "\n")
	rows := make([]splitDiffRow, 0, len(lines))
	var removed, added []numberedDiffLine
	oldNumber, newNumber := 0, 0
	inHunk := false

	flushChanges := func() {
		count := max(len(removed), len(added))
		for i := 0; i < count; i++ {
			row := splitDiffRow{oldKind: diffCellEmpty, newKind: diffCellEmpty}
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
		removed = removed[:0]
		added = added[:0]
	}

	for _, line := range lines {
		if matches := hunkHeaderPattern.FindStringSubmatch(line); matches != nil {
			flushChanges()
			oldNumber, _ = strconv.Atoi(matches[1])
			newNumber, _ = strconv.Atoi(matches[2])
			inHunk = true
			rows = append(rows, splitDiffRow{hunk: line})
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
				oldNumber: oldNumber,
				newNumber: newNumber,
				oldText:   line[1:],
				newText:   line[1:],
				oldKind:   diffCellContext,
				newKind:   diffCellContext,
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
	return rows
}

func renderSplitDiff(path, summary, unified string, width int) string {
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

	rows := parseUnifiedDiff(unified)
	if len(rows) == 0 {
		out.WriteString(mutedStyle.Render("No line-level text diff is available."))
		return out.String()
	}
	for _, row := range rows {
		if row.hunk != "" {
			out.WriteString(mutedStyle.Render(fitPlain("── "+row.hunk+" ", width)))
			out.WriteByte('\n')
			continue
		}
		out.WriteString(renderDiffCell(row.oldNumber, row.oldText, row.oldKind, leftWidth, lexer))
		out.WriteString(mutedStyle.Render(separator))
		out.WriteString(renderDiffCell(row.newNumber, row.newText, row.newKind, rightWidth, lexer))
		out.WriteByte('\n')
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func renderLabel(label string, width int, style lipgloss.Style) string {
	return style.Render(padPlain("  "+label, width))
}

func renderDiffCell(number int, source string, kind diffCellKind, width int, lexer chroma.Lexer) string {
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
	if number > 0 {
		numberText = strconv.Itoa(number)
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
	source = expandTabs(source, 4)
	source = fitPlain(source, codeWidth)
	plainWidth := lipgloss.Width(source)
	highlighted := highlightCode(source, lexer, background)
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
		entry := syntaxStyle.Get(token.Type)
		style := lipgloss.NewStyle()
		if entry.Colour.IsSet() {
			style = style.Foreground(lipgloss.Color(entry.Colour.String()))
		}
		if background != nil {
			style = style.Background(background)
		}
		style = style.Bold(entry.Bold == chroma.Yes).Italic(entry.Italic == chroma.Yes).Underline(entry.Underline == chroma.Yes)
		out.WriteString(style.Render(token.Value))
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
