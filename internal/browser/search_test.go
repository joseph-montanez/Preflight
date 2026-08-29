package browser

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseSearchQueryPreservesFilterOrder(t *testing.T) {
	filters, err := parseSearchQuery("actions/ | %ext *.php | %contents: machines_demo")
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []searchFilterKind{searchByPath, searchByExtension, searchByContents}
	if len(filters) != len(wantKinds) {
		t.Fatalf("filters = %+v, want %d stages", filters, len(wantKinds))
	}
	for i, want := range wantKinds {
		if filters[i].kind != want {
			t.Fatalf("filter %d kind = %v, want %v (all: %+v)", i, filters[i].kind, want, filters)
		}
	}

	filters, err = parseSearchQuery("%type: folder | %s actions")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || filters[0].kind != searchByType || filters[1].kind != searchByPath {
		t.Fatalf("explicit reordered filters = %+v", filters)
	}
}

func TestParseSearchQueryRejectsInvalidFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "unknown", query: "%owner: me"},
		{name: "invalid type", query: "%type: link"},
		{name: "missing name", query: "%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseSearchQuery(tt.query); err == nil {
				t.Fatalf("parseSearchQuery(%q) unexpectedly succeeded", tt.query)
			}
		})
	}
}

func TestApplySearchFiltersSupportsTypeAndExtensionForms(t *testing.T) {
	dirs := []string{"edit2", "src"}
	files := []string{"EDIT2.PHP", "src/edit2.txt", "src/index.php"}
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "file type", query: "%type: file | %s index", want: "src/index.php"},
		{name: "bare extension", query: "%ext php | %s edit2", want: "EDIT2.PHP"},
		{name: "dot extension", query: "%ext .php | %s index", want: "src/index.php"},
		{name: "glob extension", query: "%ext *.txt | %s edit2", want: "src/edit2.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters, err := parseSearchQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			results, err := applySearchFilters(context.Background(), t.TempDir(), dirs, files, filters)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].path != tt.want || results[0].dir {
				t.Fatalf("results = %+v, want file %q", results, tt.want)
			}
		})
	}
}

func TestFileContainsAcrossChunkBoundary(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "large.php")
	prefix := bytes.Repeat([]byte{'x'}, 64*1024-4)
	data := append(prefix, []byte("machines_demo")...)
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		t.Fatal(err)
	}
	matched, err := fileContains(context.Background(), filename, []byte("machines_demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("expected match spanning read chunks")
	}
}
