package ftpclient

import (
	"io/fs"
	"time"

	ftp "github.com/jlaffaye/ftp"
)

type entryInfo struct {
	entry *ftp.Entry
}

func (e entryInfo) Name() string       { return e.entry.Name }
func (e entryInfo) Size() int64        { return int64(e.entry.Size) }
func (e entryInfo) ModTime() time.Time { return e.entry.Time }
func (e entryInfo) IsDir() bool        { return e.entry.Type == ftp.EntryTypeFolder }
func (e entryInfo) Sys() any           { return e.entry }
func (e entryInfo) Mode() fs.FileMode {
	if e.IsDir() {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
