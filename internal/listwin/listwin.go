// Package listwin computes the visible slice of a scrolling list so a TUI list
// can follow its cursor without rendering more rows than fit on screen.
package listwin

// Window returns the row range [start, end) to render for a list of total rows
// with the cursor at cursor when at most rows lines are available, plus whether
// rows are hidden above and/or below the window.
//
// The cursor is always inside the returned range. When rows are hidden, one
// line each is reserved for the caller's "more rows" markers, so the rendered
// block — markers included — never exceeds rows.
func Window(total, cursor, rows int) (start, end int, moreAbove, moreBelow bool) {
	if rows < 1 {
		rows = 1
	}
	if total <= rows {
		return 0, total, false, false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	if rows < 3 {
		// No room for two markers: the cursor row, plus the "below" marker
		// when a second line is available.
		return cursor, cursor + 1, false, rows >= 2 && cursor < total-1
	}
	// Keep the cursor roughly centered, then pull the window inside the list.
	visible := rows - 2
	start = cursor - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	end = start + visible
	return start, end, start > 0, end < total
}
