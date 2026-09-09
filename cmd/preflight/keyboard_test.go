package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// readAll drains a keyboardInput over the given chunks (each written as
// a separate underlying read, like real keystrokes).
func drain(t *testing.T, chunks ...string) string {
	t.Helper()
	inner, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	ki := &keyboardInput{inner: inner}
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, ki)
		done <- buf.String()
	}()
	for _, c := range chunks {
		if _, err := w.WriteString(c); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	return <-done
}

func TestKeyboardInputTranslatesCSIu(t *testing.T) {
	got := drain(t,
		"a",
		"\x1b[27;1;66~",  // ↓
		"\x1b[27;1;66~",  // ↓
		"\x1b[27;3;109~", // Alt+M
		"b",
	)
	want := "a\x1b[B\x1b[B\x1bmb"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKeyboardInputLegacyPassesThrough(t *testing.T) {
	got := drain(t, "/api", "\x1b[B", "\x1bm")
	want := "/api\x1b[B\x1bm"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKeyboardInputSplitSequence(t *testing.T) {
	// A CSI-u sequence split across two reads must still translate.
	got := drain(t, "\x1b[27;", "3;109~")
	if got != "\x1bm" {
		t.Fatalf("got %q, want alt+m", got)
	}
}

func TestKeyboardInputModifiers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[27;2;9~", "\x1b[Z"},         // shift+tab
		{"\x1b[27;1;13~", "\r"},            // enter
		{"\x1b[27;3;13~", "\x1b\r"},        // alt+enter
		{"\x1b[27;1;65~", "\x1b[A"},        // up
		{"\x1b[27;3;65~", "\x1b[1;3A"},     // alt+up
		{"\x1b[27;5;66~", "\x1b[1;5B"},     // ctrl+down
		{"\x1b[27;2;77~", "M"},             // shift+m
		{"\x1b[27;1;72~", "\x1b[H"},        // home
		{"\x1b[27;3;109~", "\x1bm"},        // alt+m
		{"\x1b[27;5;109~", "\r"},           // ctrl+m
		{"\x1b[27;9;66~", "\x1b[27;9;66~"}, // unknown modifier: untouched
		{"plain text", "plain text"},
		// Kitty keyboard protocol CSI-u:
		{"\x1b[109;3u", "\x1bm"},           // alt+m
		{"\x1b[109;4u", "\x1bM"},           // shift+alt+m
		{"\x1b[77;3u", "\x1bM"},            // alt+M
		{"\x1b[109:77;4u", "\x1bM"},        // shift+alt+m with shifted codepoint
		{"\x1b[101;3u", "\x1be"},           // alt+e
		{"\x1b[101;4u", "\x1bE"},           // shift+alt+e
		{"\x1b[13;3u", "\x1b\r"},           // alt+enter
		{"\x1b[13u", "\r"},                 // enter
		{"\x1b[9;2u", "\x1b[Z"},            // shift+tab
		{"\x1b[57374u", "\x1b[B"},          // kitty down
		{"\x1b[57373;3u", "\x1b[1;3A"},     // kitty alt+up
		{"\x1b[109;3:1u", "\x1bm"},         // press event
		{"\x1b[109;3:2u", "\x1bm"},         // repeat event
		{"\x1b[109;3:3u", ""},              // release event (dropped)
	}
	for _, c := range cases {
		var hold []byte
		got := string(translateCSIu([]byte(c.in), &hold))
		if got != c.want {
			t.Errorf("translateCSIu(%q) = %q, want %q", c.in, got, c.want)
		}
		if len(hold) != 0 {
			t.Errorf("translateCSIu(%q) left hold %q", c.in, hold)
		}
	}
}

func TestKeyboardInputKittyCSIu(t *testing.T) {
	got := drain(t,
		"/",
		"b", "i", "l", "l",
		"\x1b[57374u", // Kitty ↓
		"\x1b[109;3u", // Kitty Alt+M
		"\r",          // Enter
	)
	want := "/bill\x1b[B\x1bm\r"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKeyboardInputKittySplitSequence(t *testing.T) {
	got := drain(t, "\x1b[109;", "3u")
	if got != "\x1bm" {
		t.Fatalf("got %q, want alt+m", got)
	}
}
