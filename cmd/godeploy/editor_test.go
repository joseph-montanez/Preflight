package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{"hx", []string{"hx"}},
		{"hx --log /tmp/hx.log", []string{"hx", "--log", "/tmp/hx.log"}},
		{`"C:\Program Files\Code\code.exe" --wait`, []string{`C:\Program Files\Code\code.exe`, "--wait"}},
		{"hx 'my file.txt'", []string{"hx", "my file.txt"}},
		{"  hx   --verbose  ", []string{"hx", "--verbose"}},
		{"", []string{}},
	}
	for _, c := range cases {
		got, err := splitCommandLine(c.line)
		if err != nil {
			t.Fatalf("splitCommandLine(%q): %v", c.line, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("splitCommandLine(%q) = %#v, want %#v", c.line, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitCommandLine(%q) = %#v, want %#v", c.line, got, c.want)
			}
		}
	}
}

func TestSplitCommandLineUnterminatedQuote(t *testing.T) {
	if _, err := splitCommandLine(`hx "oops`); err == nil {
		t.Fatal("unterminated quote must be an error")
	}
}

func TestEditorCommand(t *testing.T) {
	argv, err := editorCommand("hx --log /tmp/hx.log")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 3 || argv[0] != "hx" || argv[1] != "--log" || argv[2] != "/tmp/hx.log" {
		t.Fatalf("argv = %#v", argv)
	}
	if _, err := editorCommand(""); err == nil {
		t.Fatal("empty editor command must be an error")
	}
	if _, err := editorCommand("   "); err == nil {
		t.Fatal("blank editor command must be an error")
	}
}

// editorHelperPath returns a command line for this test binary in
// helper mode: it records its argv and cwd to a file and exits 0, so
// openEditor can be exercised end to end without a real editor.
func editorHelperPath() string {
	exe, _ := os.Executable()
	return fmt.Sprintf("%q -test.run=TestEditorHelperProcess --", exe)
}

func TestEditorHelperProcess(t *testing.T) {
	out := os.Getenv("GODEPLOY_EDITOR_HELPER_OUT")
	if out == "" {
		return
	}
	// Runs inside a real editor invocation: capture argv and cwd, then
	// exit successfully so the parent test sees a clean editor session.
	payload := strings.Join(os.Args[1:], "\x00") + "\x00" + mustGetwd()
	if err := os.WriteFile(out, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func TestOpenEditorRunsCommand(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "notes.txt")
	out := filepath.Join(dir, "out.txt")
	t.Setenv("GODEPLOY_EDITOR_HELPER_OUT", out)
	if err := openEditor(editorHelperPath(), target, dir); err != nil {
		t.Fatalf("openEditor: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("editor never ran: %v", err)
	}
	parts := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	// argv = ["-test.run=TestEditorHelperProcess", "--", target]; the
	// target must be the last argument and the cwd must be honored.
	if parts[len(parts)-2] != target {
		t.Fatalf("editor argv = %#v, want target %q as last arg", parts, target)
	}
	if got := parts[len(parts)-1]; got != dir {
		t.Fatalf("editor cwd = %q, want %q", got, dir)
	}
}

func TestOpenEditorMissingBinary(t *testing.T) {
	if err := openEditor("definitely-not-an-editor-xyz", "/tmp/f", t.TempDir()); err == nil {
		t.Fatal("missing editor binary must be an error")
	}
}

func TestOpenEditorUnconfigured(t *testing.T) {
	if err := openEditor("", "/tmp/f", t.TempDir()); err == nil {
		t.Fatal("empty editor config must be an error")
	}
}
