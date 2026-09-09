package domain

import (
	"errors"
	"io"
	"io/fs"
	"time"
)

type ActionType int

const (
	ActionSkip ActionType = iota
	ActionUpload
	ActionDownload
	ActionDeleteRemote
	ActionDeleteLocal
)

func (a ActionType) String() string {
	switch a {
	case ActionUpload:
		return "Upload"
	case ActionDownload:
		return "Download"
	case ActionDeleteRemote:
		return "Delete Remote"
	case ActionDeleteLocal:
		return "Delete Local"
	default:
		return "Skip"
	}
}

type DiffMode string

const (
	DiffModeSize    DiffMode = "size"
	DiffModeModTime DiffMode = "modtime"
	DiffModeBinary  DiffMode = "binary"
	DiffModeText    DiffMode = "text"
)

type SyncFileItem struct {
	RelativePath string
	Scope        string // sync scope this file belongs to ("" = project root)
	// LocalPath and RemotePath are the resolved endpoints for this item.
	// They are populated by the planner so a project can map different local
	// subfolders to unrelated remote directories.
	LocalPath   string
	RemotePath  string
	LocalStat   *FileMeta
	RemoteStat  *FileMeta
	Action      ActionType
	DiffDetails *DiffResult
}

type FileMeta struct {
	Size    int64
	ModTime time.Time
	Mode    fs.FileMode
	Hash    string
	Exists  bool
}

type DiffResult struct {
	Identical bool
	Unified   string
	Summary   string
	TooLarge  bool
}

// MaxDiffLines is the largest text diff retained and rendered by the sync
// review. Larger files are still compared, but their line-level diff is
// omitted to keep memory and rendering work bounded.
const MaxDiffLines = 1000

// MaxTextDiffBytes is the largest file that may enter the in-memory text diff
// engine. Larger files are compared with a streaming hash instead because a
// relatively small input can make the Myers diff allocate enormous matrices.
const MaxTextDiffBytes int64 = 1 << 20

var ErrFastHashUnsupported = errors.New("remote fast hash unsupported")

type RemoteClient interface {
	Connect() error
	Close() error
	Stat(path string) (*FileMeta, error)
	ReadDir(path string) ([]fs.FileInfo, error)
	OpenReader(path string) (io.ReadCloser, error)
	OpenWriter(path string) (io.WriteCloser, error)
	Remove(path string) error
	MkdirAll(path string) error
	FastHash(path string) (string, error)
}

// Execer runs a shell command on the remote host, streaming stdout and
// stderr as it runs. It is implemented by transports with a shell (SFTP);
// FTP has no exec channel, so task steps type-assert it and report a clear
// error for accounts that cannot run commands.
type Execer interface {
	Exec(command string, stdout, stderr io.Writer) error
}

type RenameCapable interface {
	Rename(oldPath, newPath string) error
}

type ChtimesCapable interface {
	Chtimes(path string, atime, mtime time.Time) error
}
