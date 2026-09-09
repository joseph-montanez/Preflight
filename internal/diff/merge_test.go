package diff

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestApplySectionLines(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		section   ChangeSection
		want      []string
		expectErr bool
	}{
		{
			name:  "insert at start",
			lines: []string{"b", "c"},
			section: ChangeSection{
				LocalStartLine: 1,
				LocalLineCount: 0,
				RemoteLines:    []string{"a"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name:  "insert in middle",
			lines: []string{"a", "c"},
			section: ChangeSection{
				LocalStartLine: 2,
				LocalLineCount: 0,
				RemoteLines:    []string{"b1", "b2"},
			},
			want: []string{"a", "b1", "b2", "c"},
		},
		{
			name:  "insert at end",
			lines: []string{"a", "b"},
			section: ChangeSection{
				LocalStartLine: 3,
				LocalLineCount: 0,
				RemoteLines:    []string{"c"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name:  "replace single line",
			lines: []string{"a", "old", "c"},
			section: ChangeSection{
				LocalStartLine: 2,
				LocalLineCount: 1,
				RemoteLines:    []string{"new"},
			},
			want: []string{"a", "new", "c"},
		},
		{
			name:  "replace multiple lines with multiple lines",
			lines: []string{"a", "old1", "old2", "d"},
			section: ChangeSection{
				LocalStartLine: 2,
				LocalLineCount: 2,
				RemoteLines:    []string{"new1", "new2", "new3"},
			},
			want: []string{"a", "new1", "new2", "new3", "d"},
		},
		{
			name:  "delete local lines (empty remote lines)",
			lines: []string{"a", "local_only", "c"},
			section: ChangeSection{
				LocalStartLine: 2,
				LocalLineCount: 1,
				RemoteLines:    nil,
			},
			want: []string{"a", "c"},
		},
		{
			name:  "empty file insert",
			lines: []string{},
			section: ChangeSection{
				LocalStartLine: 1,
				LocalLineCount: 0,
				RemoteLines:    []string{"line1", "line2"},
			},
			want: []string{"line1", "line2"},
		},
		{
			name:  "invalid start line negative",
			lines: []string{"a"},
			section: ChangeSection{
				LocalStartLine: 0,
				LocalLineCount: 0,
				RemoteLines:    []string{"x"},
			},
			expectErr: true,
		},
		{
			name:  "invalid start line beyond file length",
			lines: []string{"a"},
			section: ChangeSection{
				LocalStartLine: 3,
				LocalLineCount: 0,
				RemoteLines:    []string{"x"},
			},
			expectErr: true,
		},
		{
			name:  "replacement extends beyond file length",
			lines: []string{"a", "b"},
			section: ChangeSection{
				LocalStartLine: 2,
				LocalLineCount: 3,
				RemoteLines:    []string{"x"},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplySectionLines(tt.lines, tt.section)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplySectionFilePreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := []byte("func A() {}\r\nfunc C() {}\r\n")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	sec := ChangeSection{
		LocalStartLine: 2,
		LocalLineCount: 0,
		RemoteLines:    []string{"func B() {}"},
	}

	if err := ApplySection(filePath, sec); err != nil {
		t.Fatalf("ApplySection failed: %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	want := "func A() {}\r\nfunc B() {}\r\nfunc C() {}\r\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}

func TestApplySectionFilePreservesLF(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := []byte("func A() {}\nfunc C() {}\n")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	sec := ChangeSection{
		LocalStartLine: 2,
		LocalLineCount: 0,
		RemoteLines:    []string{"func B() {}"},
	}

	if err := ApplySection(filePath, sec); err != nil {
		t.Fatalf("ApplySection failed: %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	want := "func A() {}\nfunc B() {}\nfunc C() {}\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
	if bytes.Contains(got, []byte("\r")) {
		t.Fatal("expected LF only, found CR")
	}
}

func TestApplySectionFileWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	content := []byte("line1\nold")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	sec := ChangeSection{
		LocalStartLine: 2,
		LocalLineCount: 1,
		RemoteLines:    []string{"new"},
	}

	if err := ApplySection(filePath, sec); err != nil {
		t.Fatalf("ApplySection failed: %v", err)
	}

	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	want := "line1\nnew"
	if string(got) != want {
		t.Fatalf("got %q, want %q", string(got), want)
	}
}
