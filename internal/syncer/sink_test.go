package syncer

import (
	"errors"
	"testing"

	"godeploy/internal/domain"
)

func TestTransferSink(t *testing.T) {
	s := NewTransferSink()
	s.Start([]*domain.SyncFileItem{
		{RelativePath: "a.txt", Action: domain.ActionUpload, LocalStat: &domain.FileMeta{Size: 100}},
		{RelativePath: "b.txt", Action: domain.ActionUpload, LocalStat: &domain.FileMeta{Size: 200}},
		{RelativePath: "c.txt", Action: domain.ActionDownload, RemoteStat: &domain.FileMeta{Size: 300}},
	})
	s.RecordProgress(Progress{Path: "a.txt", Action: domain.ActionUpload, Bytes: 100})
	s.Record(Event{Path: "a.txt", Action: domain.ActionUpload})
	s.Record(Event{Path: "b.txt", Action: domain.ActionUpload, Err: errors.New("boom")})
	snapshot := s.Snapshot()
	if snapshot.Path != "b.txt" || snapshot.Done != 1 || snapshot.Total != 3 {
		t.Fatalf("Snapshot() = %+v, want path=b.txt done=1 total=3", snapshot)
	}
	if snapshot.BytesDone != 100 || snapshot.BytesTotal != 600 {
		t.Fatalf("Snapshot() bytes = %d/%d, want 100/600", snapshot.BytesDone, snapshot.BytesTotal)
	}
	states := s.States()
	if len(states) != 2 {
		t.Fatalf("States() = %v, want 2 entries", states)
	}
	if states["a.txt"].Err != nil || states["b.txt"].Err == nil {
		t.Fatalf("states missing error info: %v", states)
	}
}

func TestTransferSinkProgressDoesNotDoubleCountRetries(t *testing.T) {
	s := NewTransferSink()
	s.Start([]*domain.SyncFileItem{
		{RelativePath: "a.txt", Action: domain.ActionUpload, LocalStat: &domain.FileMeta{Size: 100}},
	})
	s.RecordProgress(Progress{Path: "a.txt", Action: domain.ActionUpload, Bytes: 100})
	s.RecordProgress(Progress{Path: "a.txt", Action: domain.ActionUpload, Bytes: 40})
	s.RecordProgress(Progress{Path: "a.txt", Action: domain.ActionUpload, Bytes: 100})

	snapshot := s.Snapshot()
	if snapshot.BytesDone != 100 || snapshot.BytesTotal != 100 {
		t.Fatalf("Snapshot() bytes = %d/%d, want 100/100", snapshot.BytesDone, snapshot.BytesTotal)
	}
}

func TestTransferSinkKeepsActualBytesForShortChangingFile(t *testing.T) {
	s := NewTransferSink()
	s.Start([]*domain.SyncFileItem{
		{RelativePath: "log.txt", Action: domain.ActionDownload, RemoteStat: &domain.FileMeta{Size: 100}},
	})
	s.RecordProgress(Progress{Path: "log.txt", Action: domain.ActionDownload, Bytes: 60})
	s.Record(Event{Path: "log.txt", Action: domain.ActionDownload})

	snapshot := s.Snapshot()
	if snapshot.BytesDone != 60 || snapshot.BytesTotal != 100 {
		t.Fatalf("Snapshot() bytes = %d/%d, want actual/planned 60/100", snapshot.BytesDone, snapshot.BytesTotal)
	}
}

func TestTransferSinkScopeKeying(t *testing.T) {
	s := NewTransferSink()
	s.Record(Event{Path: "index.php", Scope: "web", Action: domain.ActionUpload})
	s.Record(Event{Path: "index.php", Scope: "kiosks", Action: domain.ActionUpload})
	states := s.States()
	if len(states) != 2 {
		t.Fatalf("same-relative-path files in different scopes must not collide: %v", states)
	}
	if _, ok := states["web/index.php"]; !ok {
		t.Fatalf("missing web/index.php in %v", states)
	}
}
