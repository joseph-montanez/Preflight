package listwin

import "testing"

func TestWindowFitsWithoutMarkers(t *testing.T) {
	start, end, above, below := Window(3, 2, 5)
	if start != 0 || end != 3 || above || below {
		t.Fatalf("Window(3,2,5) = %d,%d,%v,%v; want 0,3,false,false", start, end, above, below)
	}
}

// TestWindowKeepsCursorVisible walks every cursor position at several heights
// and checks the invariants: the cursor is inside the window, the window is
// backed by real rows, and the rendered block (window + markers) fits.
func TestWindowKeepsCursorVisible(t *testing.T) {
	for _, total := range []int{1, 2, 5, 30} {
		for _, rows := range []int{1, 2, 3, 4, 10, 40} {
			for cursor := 0; cursor < total; cursor++ {
				start, end, above, below := Window(total, cursor, rows)
				if start < 0 || end > total || start >= end {
					t.Fatalf("total=%d rows=%d cursor=%d: bad range [%d,%d)", total, rows, cursor, start, end)
				}
				if cursor < start || cursor >= end {
					t.Fatalf("total=%d rows=%d cursor=%d: cursor outside [%d,%d)", total, rows, cursor, start, end)
				}
				markers := 0
				if above {
					markers++
				}
				if below {
					markers++
				}
				if lines := end - start + markers; lines > rows {
					t.Fatalf("total=%d rows=%d cursor=%d: %d lines (window %d + %d markers)", total, rows, cursor, lines, end-start, markers)
				}
				// Markers are only omitted when there is no room for them.
				if rows >= 3 {
					if above != (start > 0) {
						t.Fatalf("total=%d rows=%d cursor=%d: above=%v but start=%d", total, rows, cursor, above, start)
					}
					if below != (end < total) {
						t.Fatalf("total=%d rows=%d cursor=%d: below=%v but end=%d of %d", total, rows, cursor, below, end, total)
					}
				} else if above {
					t.Fatalf("total=%d rows=%d cursor=%d: no room for an above marker", total, rows, cursor)
				}
			}
		}
	}
}

func TestWindowClampsOutOfRangeInputs(t *testing.T) {
	if start, end, _, _ := Window(10, -5, 5); start != 0 || end != 3 {
		t.Fatalf("negative cursor: got [%d,%d)", start, end)
	}
	if start, end, _, _ := Window(10, 99, 5); start != 7 || end != 10 {
		t.Fatalf("cursor past the end: got [%d,%d)", start, end)
	}
	if start, end, _, _ := Window(0, 0, 5); start != 0 || end != 0 {
		t.Fatalf("empty list: got [%d,%d)", start, end)
	}
	if start, end, _, _ := Window(4, 3, 0); start != 3 || end != 4 {
		t.Fatalf("rows<1 should still show the cursor row: got [%d,%d)", start, end)
	}
}
