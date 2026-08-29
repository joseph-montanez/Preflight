package pathcomp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalPathCompletion(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "api-backend"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "api-frontend"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644)

	sep := string(os.PathSeparator)
	cands, err := LocalPath(filepath.Join(dir, "api"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 dir candidates, got %v", cands)
	}
	for _, c := range cands {
		if !strings.HasSuffix(c, sep) {
			t.Fatalf("dir candidate should end with separator: %q", c)
		}
	}
	if !strings.HasPrefix(cands[0], filepath.Join(dir, "api-backend")) {
		t.Fatalf("expected dirs sorted first: %v", cands)
	}

	files, err := LocalPath(filepath.Join(dir, "readme"), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != filepath.Join(dir, "readme.md") {
		t.Fatalf("expected readme.md candidate, got %v", files)
	}
}

func TestLocalPathCompletionDirsonlyExcludesFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "keep"), 0o755)
	cands, err := LocalPath(filepath.Join(dir, "keep"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0] != filepath.Join(dir, "keep")+string(os.PathSeparator) {
		t.Fatalf("dirsOnly should return only the dir, got %v", cands)
	}
}

func TestSplitLocal(t *testing.T) {
	cases := []struct{ in, base, last string }{
		{"", ".", ""},
		{filepath.Join("var", "www", "api"), filepath.Join("var", "www"), "api"},
		{filepath.Join("var", "www") + string(os.PathSeparator), filepath.Join("var", "www"), ""},
		{"foo", ".", "foo"},
	}
	for _, c := range cases {
		base, last := SplitLocal(c.in)
		if base != c.base || last != c.last {
			t.Fatalf("SplitLocal(%q) = (%q, %q), want (%q, %q)", c.in, base, last, c.base, c.last)
		}
	}
}
