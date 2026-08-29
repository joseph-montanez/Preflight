package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

// Document the depth semantics of configured exclusion patterns.
func TestPatternDepthSemantics(t *testing.T) {
	root := t.TempDir()
	engine, err := New(root, []string{".git/", "*.log", "**/.git/"})
	if err != nil {
		t.Fatal(err)
	}
	// Need separate engines: patterns share the same list above.
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)

	rootOnly, err := New(root, []string{".git/"})
	if err != nil {
		t.Fatal(err)
	}
	anyDepth, err := New(root, []string{"**/.git/"})
	if err != nil {
		t.Fatal(err)
	}
	logRoot, err := New(root, []string{"*.log"})
	if err != nil {
		t.Fatal(err)
	}
	logAnyDepth, err := New(root, []string{"**/*.log"})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		engine *Engine
		proj   string // project-relative path
		isDir  bool
		want   bool
	}{
		// ".git/" (normalized to .git/**) matches the TOP-LEVEL .git dir...
		{"root .git dir", rootOnly, ".git", true, true},
		{"root .git file", rootOnly, ".git/config", false, true},
		// ...but NOT a nested .git repo.
		{"nested .git dir", rootOnly, "sub/.git", true, false},
		{"nested .git file", rootOnly, "sub/.git/config", false, false},
		// "**/.git/" matches .git at any depth.
		{"anydepth root", anyDepth, ".git", true, true},
		{"anydepth nested", anyDepth, "sub/.git", true, true},
		{"anydepth nested file", anyDepth, "sub/.git/config", false, true},
		// "*.log" is root-anchored; "**/*.log" is not.
		{"log root", logRoot, "app.log", false, true},
		{"log nested", logRoot, "logs/app.log", false, false},
		{"log nested anydepth", logAnyDepth, "logs/app.log", false, true},
	}
	for _, tc := range cases {
		got := tc.engine.Ignored(tc.proj, tc.proj, tc.isDir)
		if got != tc.want {
			t.Fatalf("%s: Ignored(%q, isDir=%v) = %v, want %v", tc.name, tc.proj, tc.isDir, got, tc.want)
		}
	}
	_ = engine
}
