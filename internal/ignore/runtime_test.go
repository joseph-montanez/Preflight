package ignore

import (
	"testing"
)

func TestProjectRelativePatternMatching(t *testing.T) {
	root := t.TempDir()
	engine, err := New(root, []string{"kiosks/x.php"})
	if err != nil {
		t.Fatal(err)
	}
	// The pattern is project-relative: it must match regardless of the
	// scope being synced.
	if !engine.Ignored("kiosks/x.php", "x.php", false) {
		t.Fatal("expected match via project-relative path (scope = kiosks)")
	}
	if !engine.Ignored("kiosks/x.php", "public_html/x.php", false) {
		t.Fatal("expected match via project-relative path (deeper scope)")
	}
	if !engine.Ignored("kiosks/x.php", "kiosks/x.php", false) {
		t.Fatal("expected match when scope is the project root")
	}
	// A different file must not be ignored.
	if engine.Ignored("kiosks/y.php", "y.php", false) {
		t.Fatal("unrelated file should not match")
	}
	// Scope-relative matching still works as before.
	scoped, err := New(root, []string{"node_modules/"})
	if err != nil {
		t.Fatal(err)
	}
	if !scoped.Ignored("src/node_modules", "node_modules", true) {
		t.Fatal("scope-relative matching regressed")
	}
}

func TestAddRuntimePatterns(t *testing.T) {
	engine, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Ignored("web/c.js", "c.js", false) {
		t.Fatal("should not be ignored before Add")
	}
	engine.Add("web/c.js", "*.tmp")
	if !engine.Ignored("web/c.js", "c.js", false) {
		t.Fatal("Add should make the pattern effective")
	}
	if !engine.Ignored("web/cache.tmp", "cache.tmp", false) {
		t.Fatal("normalized pattern from Add should match")
	}
}
