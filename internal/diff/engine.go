package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"preflight/internal/domain"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

type TextOptions struct {
	IgnoreWhitespace bool
	IgnoreBlankLines bool
	StripComments    bool
}

type Engine struct {
	commentRegexes []*regexp.Regexp
	whitespace     *regexp.Regexp
}

func NewEngine() *Engine {
	return &Engine{
		commentRegexes: []*regexp.Regexp{
			regexp.MustCompile(`(?m)//.*$`),
			regexp.MustCompile(`/\*[\s\S]*?\*/`),
			regexp.MustCompile(`(?m)#.*$`),
		},
		whitespace: regexp.MustCompile(`\s+`),
	}
}

func (e *Engine) NormalizeText(src []byte, opts TextOptions) []byte {
	content := strings.ReplaceAll(string(src), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if opts.StripComments {
		for _, re := range e.commentRegexes {
			content = re.ReplaceAllString(content, "")
		}
	}
	lines := strings.Split(content, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		if opts.IgnoreWhitespace {
			line = strings.TrimSpace(line)
			line = e.whitespace.ReplaceAllString(line, " ")
		}
		if opts.IgnoreBlankLines && strings.TrimSpace(line) == "" {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return []byte(strings.Join(cleaned, "\n"))
}

func (e *Engine) CompareText(local, remote []byte, localPath, remotePath string, opts TextOptions) *domain.DiffResult {
	return e.compareText(local, remote, localPath, remotePath, opts, true)
}

// CompareTextUnbounded generates a text diff without the normal line-count
// guard. Callers should only use it after an explicit user request.
func (e *Engine) CompareTextUnbounded(local, remote []byte, localPath, remotePath string, opts TextOptions) *domain.DiffResult {
	return e.compareText(local, remote, localPath, remotePath, opts, false)
}

func (e *Engine) compareText(local, remote []byte, localPath, remotePath string, opts TextOptions, limit bool) *domain.DiffResult {
	if bytes.Equal(local, remote) {
		return &domain.DiffResult{Identical: true, Summary: "identical after normalization"}
	}
	if opts == (TextOptions{}) {
		if equalNormalizedLineEndings(local, remote) {
			return &domain.DiffResult{Identical: true, Summary: "identical after normalization"}
		}
		if limit && (sourceLineCount(local) > domain.MaxDiffLines || sourceLineCount(remote) > domain.MaxDiffLines) {
			return diffTooLargeResult()
		}
	}
	normLocal := e.NormalizeText(local, opts)
	normRemote := e.NormalizeText(remote, opts)
	if bytes.Equal(normLocal, normRemote) {
		return &domain.DiffResult{Identical: true, Summary: "identical after normalization"}
	}
	if limit && (textLineCount(normLocal) > domain.MaxDiffLines || textLineCount(normRemote) > domain.MaxDiffLines) {
		return diffTooLargeResult()
	}

	comparisonEdits := myers.ComputeEdits(span.URIFromPath(remotePath), string(normRemote), string(normLocal))
	displayLocal, displayRemote := normLocal, normRemote
	displayEdits := comparisonEdits
	if opts.IgnoreWhitespace {
		displayOpts := opts
		displayOpts.IgnoreWhitespace = false
		displayLocal = e.NormalizeText(local, displayOpts)
		displayRemote = e.NormalizeText(remote, displayOpts)
		displayEdits = myers.ComputeEdits(span.URIFromPath(remotePath), string(displayRemote), string(displayLocal))
	}
	unified := gotextdiff.ToUnified(remotePath, localPath, string(displayRemote), displayEdits)
	unifiedText := fmt.Sprint(unified)
	if limit && textLineCount([]byte(unifiedText)) > domain.MaxDiffLines {
		return diffTooLargeResult()
	}
	return &domain.DiffResult{
		Identical: false,
		Unified:   unifiedText,
		Summary:   fmt.Sprintf("%d edits detected", len(comparisonEdits)),
	}
}

func diffTooLargeResult() *domain.DiffResult {
	return &domain.DiffResult{Summary: "(diff too large)", TooLarge: true}
}

func textLineCount(text []byte) int {
	if len(text) == 0 {
		return 0
	}
	count := bytes.Count(text, []byte{'\n'})
	if text[len(text)-1] != '\n' {
		count++
	}
	return count
}

func sourceLineCount(text []byte) int {
	if len(text) == 0 {
		return 0
	}
	count := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			count++
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
		case '\n':
			count++
		}
	}
	if last := text[len(text)-1]; last != '\r' && last != '\n' {
		count++
	}
	return count
}

func equalNormalizedLineEndings(left, right []byte) bool {
	leftIndex, rightIndex := 0, 0
	for {
		leftByte, leftOK := nextNormalizedByte(left, &leftIndex)
		rightByte, rightOK := nextNormalizedByte(right, &rightIndex)
		if leftOK != rightOK {
			return false
		}
		if !leftOK {
			return true
		}
		if leftByte != rightByte {
			return false
		}
	}
}

func nextNormalizedByte(text []byte, index *int) (byte, bool) {
	if *index >= len(text) {
		return 0, false
	}
	b := text[*index]
	*index++
	if b == '\r' {
		if *index < len(text) && text[*index] == '\n' {
			*index++
		}
		return '\n', true
	}
	return b, true
}

func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
