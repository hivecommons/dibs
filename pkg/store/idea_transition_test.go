package store

import (
	"errors"
	"strings"
	"testing"
)

// TestTransitionTo pins the Idea-level lifecycle helper: no-op on same
// status, mutation on an allowed edge, and a ValidationError naming the
// action on a disallowed edge (status left untouched).
func TestTransitionTo(t *testing.T) {
	t.Run("same status is a no-op success", func(t *testing.T) {
		i := &Idea{Status: StatusOffered}
		if err := i.TransitionTo(StatusOffered, "offer"); err != nil {
			t.Fatalf("TransitionTo same status: %v", err)
		}
		if i.Status != StatusOffered {
			t.Fatalf("status changed on no-op: %s", i.Status)
		}
	})

	t.Run("allowed edge mutates status", func(t *testing.T) {
		i := &Idea{Status: StatusDraft}
		if err := i.TransitionTo(StatusOffered, "offer"); err != nil {
			t.Fatalf("TransitionTo draft→offered: %v", err)
		}
		if i.Status != StatusOffered {
			t.Fatalf("status = %s, want %s", i.Status, StatusOffered)
		}
	})

	t.Run("disallowed edge errors and leaves status", func(t *testing.T) {
		i := &Idea{Status: StatusSettled}
		err := i.TransitionTo(StatusDraft, "reopen")
		if err == nil {
			t.Fatal("TransitionTo settled→draft succeeded")
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error type = %T, want *ValidationError", err)
		}
		if !strings.Contains(err.Error(), "cannot reopen an idea in status settled") {
			t.Fatalf("error message = %q, want action and status named", err)
		}
		if i.Status != StatusSettled {
			t.Fatalf("status mutated on rejected transition: %s", i.Status)
		}
	})
}

// TestTryTransition pins the silent-no-op variant used by the decline path.
func TestTryTransition(t *testing.T) {
	i := &Idea{Status: StatusOffered}
	if !i.TryTransition(StatusDeclined) {
		t.Fatal("TryTransition offered→declined = false")
	}
	if i.Status != StatusDeclined {
		t.Fatalf("status = %s, want %s", i.Status, StatusDeclined)
	}
	if i.TryTransition(StatusSettled) {
		t.Fatal("TryTransition declined→settled = true")
	}
	if i.Status != StatusDeclined {
		t.Fatalf("status mutated on refused transition: %s", i.Status)
	}
}
