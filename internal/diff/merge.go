package diff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ChangeSection represents a discrete difference block between remote (old)
// and local (new) file content.
type ChangeSection struct {
	Index           int      // 0-based index of this change section in the diff
	LocalStartLine  int      // 1-based start line in the local file
	LocalLineCount  int      // number of local lines replaced (0 for pure insertion)
	RemoteStartLine int      // 1-based start line in the remote file
	RemoteLineCount int      // number of remote lines
	RemoteLines     []string // remote line contents (without trailing newline)
	LocalLines      []string // expected local line contents (without trailing newline)
}

// ApplySectionLines applies a ChangeSection to an in-memory slice of lines,
// returning the modified lines.
func ApplySectionLines(lines []string, section ChangeSection) ([]string, error) {
	startIndex := section.LocalStartLine - 1
	if len(lines) == 0 {
		startIndex = 0
	} else if startIndex < 0 || startIndex > len(lines) {
		return nil, fmt.Errorf("invalid local start line %d (file has %d lines)", section.LocalStartLine, len(lines))
	}

	if section.LocalLineCount > 0 {
		if startIndex+section.LocalLineCount > len(lines) {
			return nil, fmt.Errorf("local change section extends beyond file (%d + %d > %d)", startIndex+1, section.LocalLineCount, len(lines))
		}
	}

	var newLines []string
	newLines = append(newLines, lines[:startIndex]...)
	newLines = append(newLines, section.RemoteLines...)
	if section.LocalLineCount > 0 {
		newLines = append(newLines, lines[startIndex+section.LocalLineCount:]...)
	} else {
		newLines = append(newLines, lines[startIndex:]...)
	}
	return newLines, nil
}

// ApplySection modifies the local file at filePath by applying the remote lines
// from section, replacing the local lines defined by LocalStartLine and LocalLineCount.
// It preserves original file permissions and line endings (CRLF vs LF).
func ApplySection(filePath string, section ChangeSection) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("cannot read local file: %w", err)
	}

	crlf := bytes.Contains(data, []byte("\r\n"))
	newline := "\n"
	if crlf {
		newline = "\r\n"
	}
	hasTrailingNewline := len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r')

	norm := strings.ReplaceAll(string(data), "\r\n", "\n")
	norm = strings.ReplaceAll(norm, "\r", "\n")
	lines := strings.Split(norm, "\n")
	if hasTrailingNewline && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	newLines, err := ApplySectionLines(lines, section)
	if err != nil {
		return err
	}

	outText := strings.Join(newLines, newline)
	if hasTrailingNewline && !strings.HasSuffix(outText, newline) {
		outText += newline
	}

	perm := os.FileMode(0644)
	if stat, statErr := os.Stat(filePath); statErr == nil {
		perm = stat.Mode().Perm()
	}

	dir := filepath.Dir(filePath)
	tmpFile, err := os.CreateTemp(dir, ".preflight-patch-*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create temp file for patch: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.WriteString(outText); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cannot write temp patch file: %w", err)
	}
	_ = tmpFile.Chmod(perm)
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cannot close temp patch file: %w", err)
	}

	if err := replaceLocalFile(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("cannot update local file: %w", err)
	}
	return nil
}

func replaceLocalFile(tmpPath, localPath string) error {
	renameErr := os.Rename(tmpPath, localPath)
	if renameErr == nil {
		return nil
	}
	targetInfo, err := os.Lstat(localPath)
	if err != nil || !targetInfo.Mode().IsRegular() {
		return renameErr
	}
	if err := overwriteFile(tmpPath, localPath); err != nil {
		return errors.Join(renameErr, fmt.Errorf("overwrite destination: %w", err))
	}
	_ = os.Remove(tmpPath)
	return nil
}

func overwriteFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
