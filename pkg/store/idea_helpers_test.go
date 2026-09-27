package store

import (
	"errors"
	"testing"
)

func TestOfferTo(t *testing.T) {
	idea := &Idea{Offers: []Offer{
		{RepoID: "org/alpha", Status: OfferPending},
		{RepoID: "org/beta", Status: OfferAccepted},
	}}
	if got := idea.OfferTo("org/beta"); got == nil || got.Status != OfferAccepted {
		t.Fatalf("OfferTo(org/beta) = %+v, want accepted offer", got)
	}
	// The returned pointer must alias the slice element so callers can mutate it.
	idea.OfferTo("org/alpha").Status = OfferDeclined
	if idea.Offers[0].Status != OfferDeclined {
		t.Fatalf("OfferTo result does not alias Offers: %+v", idea.Offers[0])
	}
	if got := idea.OfferTo("org/missing"); got != nil {
		t.Fatalf("OfferTo(missing) = %+v, want nil", got)
	}
}

func TestIdeaHasPassed(t *testing.T) {
	idea := &Idea{PassedRepos: []string{"org/alpha", "org/beta"}}
	if !idea.HasPassed("org/beta") {
		t.Fatal("HasPassed(org/beta) = false, want true")
	}
	if idea.HasPassed("org/gamma") {
		t.Fatal("HasPassed(org/gamma) = true, want false")
	}
	if (&Idea{}).HasPassed("org/alpha") {
		t.Fatal("empty idea HasPassed = true, want false")
	}
}

func TestValidationErrorMessage(t *testing.T) {
	err := &ValidationError{"title is required"}
	if got := err.Error(); got != "store: title is required" {
		t.Fatalf("Error() = %q", got)
	}
	var ve *ValidationError
	if !errors.As(error(err), &ve) {
		t.Fatal("errors.As failed for *ValidationError")
	}
}

func TestListAllAndListSettled(t *testing.T) {
	s, _ := newTestStore(t)

	pub := validIdea("alice")
	if err := s.Create(pub); err != nil {
		t.Fatalf("Create public: %v", err)
	}
	priv := validIdea("bob")
	priv.Visibility = VisibilityPrivate
	if err := s.Create(priv); err != nil {
		t.Fatalf("Create private: %v", err)
	}

	all, err := s.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll returned %d ideas, want 2 (must include private)", len(all))
	}

	settled, err := s.ListSettled()
	if err != nil {
		t.Fatalf("ListSettled: %v", err)
	}
	if len(settled) != 0 {
		t.Fatalf("ListSettled with no settled ideas = %d, want 0 (and non-nil)", len(settled))
	}
	if settled == nil {
		t.Fatal("ListSettled must return an empty non-nil slice")
	}

	if _, err := s.Mutate(pub.ID, true, func(i *Idea) error {
		i.Status = StatusSettled
		return nil
	}); err != nil {
		t.Fatalf("Mutate to settled: %v", err)
	}

	settled, err = s.ListSettled()
	if err != nil {
		t.Fatalf("ListSettled: %v", err)
	}
	if len(settled) != 1 || settled[0].ID != pub.ID {
		t.Fatalf("ListSettled = %+v, want only the settled idea %s", settled, pub.ID)
	}
}
