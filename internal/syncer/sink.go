package syncer

import (
	"sync"
	"time"

	"preflight/internal/domain"
)

// TransferSink collects per-file transfer events from the Executor so the
// TUI can render live deploy progress. It is safe for concurrent use: the
// executor's worker pool calls Record from multiple goroutines while the
// UI polls Snapshot/States from a ticker.
type TransferSink struct {
	mu         sync.Mutex
	path       string
	done       int
	total      int
	bytesDone  int64
	bytesTotal int64
	startedAt  time.Time
	finishedAt time.Time
	fileBytes  map[string]int64
	fileTotals map[string]int64
	states     map[string]Event // per-file completion state, keyed by scope/path
}

func NewTransferSink() *TransferSink {
	return &TransferSink{
		fileBytes:  map[string]int64{},
		fileTotals: map[string]int64{},
		states:     map[string]Event{},
	}
}

// TransferSnapshot is the aggregate state of one deployment.
type TransferSnapshot struct {
	Path       string
	Done       int
	Total      int
	BytesDone  int64
	BytesTotal int64
	Elapsed    time.Duration
}

// EventKey identifies a file across scopes: scope/path (or just path when
// the scope is the project root).
func EventKey(scope, path string) string {
	if scope == "" {
		return path
	}
	return scope + "/" + path
}

// Record is an Executor.OnEvent handler. Failed transfers do not count as
// done, so the counter never overtakes the total.
func (s *TransferSink) Record(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := EventKey(ev.Scope, ev.Path)
	s.path = key
	if ev.Err == nil {
		s.done++
		if s.done == s.total {
			s.finishedAt = time.Now()
		}
	}
	s.states[key] = ev
}

// RecordProgress records the cumulative bytes copied for one file. Calls may
// arrive concurrently from the executor's worker pool.
func (s *TransferSink) RecordProgress(progress Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := EventKey(progress.Scope, progress.Path)
	s.path = key
	s.setFileBytes(key, progress.Bytes)
}

// Start resets the sink and computes the file and byte totals for a deploy.
func (s *TransferSink) Start(items []*domain.SyncFileItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = ""
	s.done = 0
	s.total = 0
	s.bytesDone = 0
	s.bytesTotal = 0
	s.startedAt = time.Now()
	s.finishedAt = time.Time{}
	s.fileBytes = make(map[string]int64, len(items))
	s.fileTotals = make(map[string]int64, len(items))
	s.states = make(map[string]Event, len(items))
	for _, item := range items {
		if item.Action == domain.ActionSkip {
			continue
		}
		s.total++
		var size int64
		switch item.Action {
		case domain.ActionUpload:
			if item.LocalStat != nil {
				size = item.LocalStat.Size
			}
		case domain.ActionDownload:
			if item.RemoteStat != nil {
				size = item.RemoteStat.Size
			}
		}
		if size < 0 {
			size = 0
		}
		key := EventKey(item.Scope, item.RelativePath)
		s.fileTotals[key] = size
		s.bytesTotal += size
	}
}

// Snapshot returns the latest aggregate transfer state.
func (s *TransferSink) Snapshot() TransferSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	end := time.Now()
	if !s.finishedAt.IsZero() {
		end = s.finishedAt
	}
	var elapsed time.Duration
	if !s.startedAt.IsZero() {
		elapsed = end.Sub(s.startedAt)
	}
	return TransferSnapshot{
		Path:       s.path,
		Done:       s.done,
		Total:      s.total,
		BytesDone:  s.bytesDone,
		BytesTotal: s.bytesTotal,
		Elapsed:    elapsed,
	}
}

// States returns a copy of the per-file completion states.
func (s *TransferSink) States() map[string]Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Event, len(s.states))
	for k, v := range s.states {
		out[k] = v
	}
	return out
}

func (s *TransferSink) setFileBytes(key string, transferred int64) {
	if transferred <= s.fileBytes[key] {
		return
	}
	if total, ok := s.fileTotals[key]; ok && transferred > total {
		transferred = total
	}
	s.bytesDone += transferred - s.fileBytes[key]
	s.fileBytes[key] = transferred
}
