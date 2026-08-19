package notify

import (
	"fmt"
	"testing"
)

func mustAdd(t *testing.T, s *Store, user, kind, msg string) {
	t.Helper()
	if err := s.Add(user, kind, msg, "idea1", "org/repo"); err != nil {
		t.Fatalf("Add: %v", err)
	}
}

func TestAddListMarkRead(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, s, "alice", KindOffer, "offer!")
	mustAdd(t, s, "alice", KindMatch, "match!")
	mustAdd(t, s, "bob", KindIssue, "issue!")

	got := s.ListByUser("alice", false)
	if len(got) != 2 {
		t.Fatalf("alice feed: %+v", got)
	}
	// Users only ever see their own feed.
	if bobs := s.ListByUser("bob", false); len(bobs) != 1 || bobs[0].Message != "issue!" {
		t.Fatalf("bob feed: %+v", bobs)
	}

	// Alice cannot mark bob's notification read.
	bobID := s.ListByUser("bob", false)[0].ID
	if err := s.MarkRead("alice", []string{bobID}, false); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if s.ListByUser("bob", true)[0].Read {
		t.Fatal("alice must not mark bob's notification read")
	}

	// Mark one, then all.
	if err := s.MarkRead("alice", []string{got[0].ID}, false); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if unread := s.ListByUser("alice", true); len(unread) != 1 {
		t.Fatalf("unread after one: %+v", unread)
	}
	if err := s.MarkRead("alice", nil, true); err != nil {
		t.Fatalf("MarkRead all: %v", err)
	}
	if unread := s.ListByUser("alice", true); len(unread) != 0 {
		t.Fatalf("unread after all: %+v", unread)
	}

	// Persistence across reopen.
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.ListByUser("alice", false); len(got) != 2 || !got[0].Read {
		t.Fatalf("reopened feed: %+v", got)
	}
}

func TestPerUserCap(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < MaxPerUser+10; i++ {
		mustAdd(t, s, "alice", KindMatch, fmt.Sprintf("n%d", i))
	}
	mustAdd(t, s, "bob", KindMatch, "bob keeps his")
	if got := s.ListByUser("alice", false); len(got) != MaxPerUser {
		t.Fatalf("alice feed should be capped at %d, got %d", MaxPerUser, len(got))
	}
	if got := s.ListByUser("bob", false); len(got) != 1 {
		t.Fatalf("bob's feed affected by alice's trim: %+v", got)
	}
}
