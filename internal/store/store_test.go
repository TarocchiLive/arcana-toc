package store

import (
	"errors"
	"os"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestQueueCRUD(t *testing.T) {
	s := openTest(t)

	// Duplicate song requests are separate entries (invariant 2).
	id1, err := s.AddQueueEntry(QueueEntry{Platform: "qqmusic", TrackID: "003w2xz20QlUZt", Title: "最伟大的作品", Artist: "周杰伦", VIP: true, RequestedBy: "arc-dream"})
	if err != nil {
		t.Fatalf("add 1: %v", err)
	}
	id2, err := s.AddQueueEntry(QueueEntry{Platform: "qqmusic", TrackID: "003w2xz20QlUZt"})
	if err != nil {
		t.Fatalf("add 2: %v", err)
	}
	id3, err := s.AddQueueEntry(QueueEntry{Platform: "spotify", TrackID: "spotify:track:abc", Status: "playing"})
	if err != nil {
		t.Fatalf("add 3: %v", err)
	}

	all, err := s.QueueEntries()
	if err != nil {
		t.Fatalf("QueueEntries: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}
	if !all[0].VIP || all[1].VIP {
		t.Fatalf("VIP flags = [%v %v], want [true false]", all[0].VIP, all[1].VIP)
	}

	pending, err := s.QueueEntries("pending")
	if err != nil {
		t.Fatalf("QueueEntries(pending): %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("len(pending) = %d, want 2", len(pending))
	}

	if err := s.ArchiveQueueEntry(id1, "completed"); err != nil {
		t.Fatalf("ArchiveQueueEntry: %v", err)
	}
	if err := s.UpdateQueueStatus(9999, "playing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("UpdateQueueStatus(missing) err = %v, want not-exist", err)
	}
	history, err := s.HistoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].QueueID != id1 || history[0].Outcome != "completed" || !history[0].VIP {
		t.Fatalf("history = %+v", history)
	}
	active, err := s.QueueEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].ID != id2 || active[1].ID != id3 {
		t.Fatalf("active entries = %+v", active)
	}
	if err := s.ArchiveQueueEntry(9999, "completed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive missing err = %v, want not-exist", err)
	}
}

func TestReplaceQueueRollsBackDeletionAndHistoryOnInsertFailure(t *testing.T) {
	s := openTest(t)
	id, err := s.AddQueueEntry(QueueEntry{Platform: "netease", TrackID: "old", Status: "playing"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceQueue([]QueueEntry{
		{Platform: "qqmusic", TrackID: "new"},
		{Platform: "qqmusic"},
	}); err == nil {
		t.Fatal("accepted invalid replacement")
	}
	rows, err := s.QueueEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].Status != "playing" {
		t.Fatalf("queue changed after rollback: %+v", rows)
	}
	history, err := s.HistoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("rolled-back replacement created history: %+v", history)
	}
}
