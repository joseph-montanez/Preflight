package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanProgressNonExistentScope(t *testing.T) {
	dir := t.TempDir()

	// Scope does not exist locally (e.g. deleted file)
	files, err := ScanProgress(dir, "deleted_file.txt", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for non-existent scope: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty files map, got %v", files)
	}
}

func TestScanProgressSingleFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "app.go")
	if err := os.WriteFile(filePath, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := ScanProgress(dir, "app.go", nil, nil)
	if err != nil {
		t.Fatalf("ScanProgress: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	meta, ok := files[""]
	if !ok || !meta.Exists {
		t.Fatalf("expected files[\"\"] to exist, got %v", meta)
	}
}
