package manage

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// plainView strips styling so the rendered rows can be asserted as text.
func plainView(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func manyFields(n int) []Field {
	fields := make([]Field, 0, n)
	for i := 0; i < n; i++ {
		fields = append(fields, Field{
			Tag:   fmt.Sprintf("field%d", i),
			Kind:  KindText,
			Label: fmt.Sprintf("Field%02d", i),
			Value: "value",
		})
	}
	return fields
}

// TestFormScrollsWithCursor covers the pre-existing bug where the field list
// rendered from the first field: in a short terminal the cursor could sit below
// the rendered window and the edited field became invisible.
func TestFormScrollsWithCursor(t *testing.T) {
	f := NewForm("Test", manyFields(20), func(*Form) error { return nil }, nil)

	f.Cursor = 0
	f.focus()
	view := plainView(f.View(80, 12))
	if !strings.Contains(view, "▸ Field00") {
		t.Fatalf("first field must be visible at the top:\n%s", view)
	}
	if !strings.Contains(view, "↓ ") {
		t.Fatalf("hidden fields below should be counted:\n%s", view)
	}

	// Walk to the end; every stop must keep the edited field on screen.
	for i := 0; i < len(f.Fields); i++ {
		f.Cursor = i
		f.focus()
		view := plainView(f.View(80, 12))
		want := fmt.Sprintf("▸ Field%02d", i)
		if !strings.Contains(view, want) {
			t.Fatalf("cursor field %q missing at cursor %d:\n%s", want, i, view)
		}
	}

	// The last field scrolls the window, so rows hidden above are counted.
	view = plainView(f.View(80, 12))
	if !strings.Contains(view, "↑ ") {
		t.Fatalf("hidden fields above should be counted at the last field:\n%s", view)
	}
}

// TestFormWindowFitsHeight checks the rendered box never grows past the height
// it was given, which is what pushed the help/status rows off screen before.
func TestFormWindowFitsHeight(t *testing.T) {
	f := NewForm("Test", manyFields(20), func(*Form) error { return nil }, nil)
	for _, height := range []int{12, 16, 30, 60} {
		for _, cursor := range []int{0, 7, 19} {
			f.Cursor = cursor
			f.focus()
			lines := strings.Split(plainView(f.View(80, height)), "\n")
			if len(lines) > height {
				t.Fatalf("height=%d cursor=%d: form rendered %d lines", height, cursor, len(lines))
			}
			if !strings.Contains(plainView(f.View(80, height)), fmt.Sprintf("▸ Field%02d", cursor)) {
				t.Fatalf("height=%d cursor=%d: cursor field missing", height, cursor)
			}
		}
	}
}

// TestFormKeepsPopupAndCursorVisible checks that an open completion popup does
// not push the field it belongs to out of the visible window.
func TestFormKeepsPopupAndCursorVisible(t *testing.T) {
	f := NewForm("Test", manyFields(20), func(*Form) error { return nil }, nil)
	f.Cursor = 19
	f.focus()
	f.popup = []string{"cand-a", "cand-b", "cand-c"}
	f.popupAt = 1

	view := plainView(f.View(80, 16))
	if !strings.Contains(view, "▸ Field19") {
		t.Fatalf("cursor field must stay visible with the popup open:\n%s", view)
	}
	if !strings.Contains(view, "▸ cand-b") {
		t.Fatalf("popup must be rendered:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) > 16 {
		t.Fatalf("popup overflowed the window: %d lines", len(lines))
	}
}
