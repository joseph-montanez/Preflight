//go:build windows

package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReplaceDownloadedFileFallsBackWhenDestinationDeniesDeleteSharing(t *testing.T) {
	dir := t.TempDir()
	tmpPath := filepath.Join(dir, "server.log.preflight-tmp")
	targetPath := filepath.Join(dir, "server.log")
	want := []byte("downloaded log snapshot")
	if err := os.WriteFile(tmpPath, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("old log contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetPtr, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		targetPtr,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)

	if err := replaceDownloadedFile(tmpPath, targetPath); err != nil {
		t.Fatalf("replaceDownloadedFile: %v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("destination = %q, want %q", got, want)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temporary download still exists: %v", err)
	}
}
