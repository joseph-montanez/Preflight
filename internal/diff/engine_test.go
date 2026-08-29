package diff

import (
	"strings"
	"testing"

	"godeploy/internal/domain"
)

func TestNormalizeText(t *testing.T) {
	engine := NewEngine()
	input := []byte("  const   x = 1; // comment\r\n\r\n# ignored\nvalue   here\n")
	got := string(engine.NormalizeText(input, TextOptions{
		IgnoreWhitespace: true,
		IgnoreBlankLines: true,
		StripComments:    true,
	}))
	want := "const x = 1;\nvalue here"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCompareTextIgnoresConfiguredDifferences(t *testing.T) {
	engine := NewEngine()
	result := engine.CompareText(
		[]byte("const value = 1;\n"),
		[]byte(" const   value = 1; // old\n"),
		"local.go",
		"remote.go",
		TextOptions{IgnoreWhitespace: true, StripComments: true},
	)
	if !result.Identical {
		t.Fatalf("expected normalized text to be identical: %s", result.Unified)
	}
}

func TestCompareTextPreservesWhitespaceInDisplayedDiff(t *testing.T) {
	engine := NewEngine()
	result := engine.CompareText(
		[]byte("func main() {\n\t\tfmt.Println(\"new\")\n}\n"),
		[]byte("func main() {\n    fmt.Println(\"old\")\n}\n"),
		"local.go",
		"remote.go",
		TextOptions{IgnoreWhitespace: true},
	)

	if result.Identical {
		t.Fatal("expected the changed string literal to be detected")
	}
	if !strings.Contains(result.Unified, "-    fmt.Println(\"old\")") {
		t.Fatalf("remote indentation was not preserved:\n%s", result.Unified)
	}
	if !strings.Contains(result.Unified, "+\t\tfmt.Println(\"new\")") {
		t.Fatalf("local indentation was not preserved:\n%s", result.Unified)
	}
}

func TestCompareTextCapsLargeFiles(t *testing.T) {
	engine := NewEngine()
	local := []byte(strings.Repeat("local\n", domain.MaxDiffLines+1))
	remote := []byte(strings.Repeat("remote\n", domain.MaxDiffLines+1))

	result := engine.CompareText(local, remote, "local.csv", "remote.csv", TextOptions{})

	if !result.TooLarge {
		t.Fatal("expected a large diff to be capped")
	}
	if result.Unified != "" {
		t.Fatalf("large diff retained %d bytes of unified output", len(result.Unified))
	}
	if result.Summary != "(diff too large)" {
		t.Fatalf("summary = %q, want %q", result.Summary, "(diff too large)")
	}
}

func TestCompareTextRecognizesLargeFilesWithDifferentLineEndings(t *testing.T) {
	engine := NewEngine()
	local := []byte(strings.Repeat("same\n", domain.MaxDiffLines+1))
	remote := []byte(strings.Repeat("same\r\n", domain.MaxDiffLines+1))

	result := engine.CompareText(local, remote, "local.csv", "remote.csv", TextOptions{})

	if !result.Identical || result.TooLarge {
		t.Fatalf("newline-normalized files should be identical: %+v", result)
	}
}

func TestSourceLineCountHandlesCommonLineEndings(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "empty", text: "", want: 0},
		{name: "unterminated", text: "a\nb", want: 2},
		{name: "LF terminated", text: "a\nb\n", want: 2},
		{name: "CRLF terminated", text: "a\r\nb\r\n", want: 2},
		{name: "CR terminated", text: "a\rb\r", want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourceLineCount([]byte(tt.text)); got != tt.want {
				t.Fatalf("sourceLineCount(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestCompareTextAllowsThousandLineFileWithSmallDiff(t *testing.T) {
	engine := NewEngine()
	lines := make([]string, domain.MaxDiffLines)
	for i := range lines {
		lines[i] = "same"
	}
	remote := strings.Join(lines, "\n")
	lines[len(lines)-1] = "changed"
	local := strings.Join(lines, "\n")

	result := engine.CompareText([]byte(local), []byte(remote), "local.csv", "remote.csv", TextOptions{})

	if result.TooLarge {
		t.Fatal("a 1,000-line file with a small diff should be rendered")
	}
	if result.Unified == "" {
		t.Fatal("expected a unified diff")
	}
}

func TestCompareTextCapsUnifiedOutputOverLimit(t *testing.T) {
	engine := NewEngine()
	local := []byte(strings.Repeat("local\n", 600))
	remote := []byte(strings.Repeat("remote\n", 600))

	result := engine.CompareText(local, remote, "local.csv", "remote.csv", TextOptions{})

	if !result.TooLarge || result.Unified != "" {
		t.Fatalf("large unified output was not capped: %+v", result)
	}
}

func TestCompareTextUnboundedBuildsLargeDiff(t *testing.T) {
	engine := NewEngine()
	local := []byte(strings.Repeat("local\n", domain.MaxDiffLines+1))
	remote := []byte(strings.Repeat("remote\n", domain.MaxDiffLines+1))

	result := engine.CompareTextUnbounded(local, remote, "local.csv", "remote.csv", TextOptions{})

	if result.TooLarge {
		t.Fatal("explicit unbounded comparison should bypass the line guard")
	}
	if result.Unified == "" {
		t.Fatal("explicit unbounded comparison did not build a diff")
	}
}
