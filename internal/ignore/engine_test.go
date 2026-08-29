package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusionsOnlyIgnoresGitignore(t *testing.T) {
	// A .gitignore sitting in the working directory must NOT be consulted:
	// the exclusions-only engine is the upload escape hatch.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := NewExclusionsOnly([]string{"node_modules/"})
	cases := []struct {
		projectRel string
		scopedRel  string
		isDir      bool
		want       bool
	}{
		{"secret.txt", "secret.txt", false, false},       // only in .gitignore: not matched
		{"src/node_modules", "node_modules", true, true}, // configured exclusion: matched
		{"src/main.go", "main.go", false, false},
	}
	for _, tc := range cases {
		if got := engine.Ignored(tc.projectRel, tc.scopedRel, tc.isDir); got != tc.want {
			t.Fatalf("Ignored(%q, %q) = %v, want %v", tc.projectRel, tc.scopedRel, got, tc.want)
		}
	}
}

// TestRootMatch confirms the root-match rule used by the local upload
// browser: a trailing-slash exclusion like "dist/" excludes the picked
// root folder "dist" itself (doublestar ** matches zero segments), at
// any depth (bare-name matching, like gitignore).
func TestRootMatch(t *testing.T) {
	engine := NewExclusionsOnly([]string{"dist/", "config.local.yaml", "**/*.tmp"})
	cases := []struct {
		projectRel string
		isDir      bool
		want       bool
	}{
		{"dist", true, true},
		{"dist", false, true}, // doublestar ** matches zero segments: a file named dist matches too (pre-existing engine semantics)
		{"sub/dist", true, true},
		{"sub", true, false},
		{"config.local.yaml", false, true},
		{"sub/config.local.yaml", false, true}, // bare name matches at any depth, like gitignore
		{"kiosks/a.tmp", false, true},
		{"src/main.go", false, false},
	}
	for _, tc := range cases {
		if got := engine.RootIgnored(tc.projectRel, tc.isDir); got != tc.want {
			t.Fatalf("RootIgnored(%q, %v) = %v, want %v", tc.projectRel, tc.isDir, got, tc.want)
		}
	}
}

func TestConfiguredAndGitIgnorePatterns(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/secret.txt\nbuild/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine, err := New(root, []string{"node_modules/", "*.tmp"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		projectRel string
		scopedRel  string
		isDir      bool
		want       bool
	}{
		{"secret.txt", "secret.txt", false, true},
		{"src/build", "build", true, true},
		{"src/node_modules", "node_modules", true, true},
		{"src/cache.tmp", "cache.tmp", false, true},
		{"src/main.go", "main.go", false, false},
	}
	for _, tc := range cases {
		if got := engine.Ignored(tc.projectRel, tc.scopedRel, tc.isDir); got != tc.want {
			t.Fatalf("Ignored(%q, %q) = %v, want %v", tc.projectRel, tc.scopedRel, got, tc.want)
		}
	}
}
