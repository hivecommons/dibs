package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateTagLimits pins the per-tag and tag-count caps, which are the
// only user-input limits in Validate not exercised by TestValidation.
func TestValidateTagLimits(t *testing.T) {
	manyTags := make([]string, MaxTags+1)
	for i := range manyTags {
		manyTags[i] = "t"
	}
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"too many tags", manyTags, "more than"},
		{"blank tag", []string{"ok", "  "}, "cannot be blank"},
		{"long tag", []string{strings.Repeat("x", MaxTagLen+1)}, "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idea := validIdea("alice")
			idea.Tags = tc.tags
			err := Validate(idea)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if !strings.Contains(ve.Msg, tc.want) {
				t.Fatalf("msg = %q, want substring %q", ve.Msg, tc.want)
			}
		})
	}

	t.Run("at the limits is valid", func(t *testing.T) {
		idea := validIdea("alice")
		idea.Status = StatusDraft
		idea.Tags = make([]string, MaxTags)
		for i := range idea.Tags {
			idea.Tags[i] = strings.Repeat("y", MaxTagLen)
		}
		if err := Validate(idea); err != nil {
			t.Fatalf("Validate at limits: %v", err)
		}
	})
}

// TestValidateListCaps pins the ceilings on the per-idea lists that grow by
// one entry per request (swipe-away history and offers): without them a
// single idea file can be inflated until every listing that decodes it
// stalls.
func TestValidateListCaps(t *testing.T) {
	repo := func(n int) string { return "org/repo" + strings.Repeat("0", n) }
	manyPassed := make([]string, MaxPassedRepos+1)
	for i := range manyPassed {
		manyPassed[i] = "org/r"
	}
	manyOffers := make([]Offer, MaxOffers+1)
	for i := range manyOffers {
		manyOffers[i] = Offer{RepoID: "org/r", Status: OfferPending}
	}
	cases := []struct {
		name   string
		passed []string
		offers []Offer
		want   string
	}{
		{"too many passed repos", manyPassed, nil, "more than"},
		{"long passed repo id", []string{repo(MaxRepoIDLen)}, nil, "passed repo id exceeds"},
		{"too many offers", nil, manyOffers, "more than"},
		{"long offer repo id", nil, []Offer{{RepoID: repo(MaxRepoIDLen), Status: OfferPending}}, "offer repo id exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idea := validIdea("alice")
			idea.PassedRepos = tc.passed
			idea.Offers = tc.offers
			err := Validate(idea)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
			if !strings.Contains(ve.Msg, tc.want) {
				t.Fatalf("msg = %q, want substring %q", ve.Msg, tc.want)
			}
		})
	}

	t.Run("at the limits is valid", func(t *testing.T) {
		idea := validIdea("alice")
		idea.Status = StatusDraft
		idea.PassedRepos = manyPassed[:MaxPassedRepos]
		idea.Offers = manyOffers[:MaxOffers]
		if err := Validate(idea); err != nil {
			t.Fatalf("Validate at limits: %v", err)
		}
	})

	t.Run("mutate refuses to grow past the cap", func(t *testing.T) {
		s, _ := newTestStore(t)
		idea := validIdea("alice")
		idea.PassedRepos = append([]string(nil), manyPassed[:MaxPassedRepos]...)
		if err := s.Create(idea); err != nil {
			t.Fatalf("Create: %v", err)
		}
		_, err := s.Mutate(idea.ID, false, func(i *Idea) error {
			i.PassedRepos = append(i.PassedRepos, "org/one-more")
			return nil
		})
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("Mutate past cap: err = %v, want ValidationError", err)
		}
		got, err := s.Get(idea.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.PassedRepos) != MaxPassedRepos {
			t.Fatalf("persisted PassedRepos = %d, want unchanged %d", len(got.PassedRepos), MaxPassedRepos)
		}
	})
}

// TestNewOpenErrors covers the three ways New can fail before serving: the
// data dir cannot be created, index.json cannot be read, index.json is
// not valid JSON.
func TestNewOpenErrors(t *testing.T) {
	t.Run("data dir path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(file)
		if err == nil || !strings.Contains(err.Error(), "creating data dir") {
			t.Fatalf("err = %v, want creating data dir error", err)
		}
	})

	t.Run("index is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ideas", "index.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := New(dir)
		if err == nil || !strings.Contains(err.Error(), "reading index") {
			t.Fatalf("err = %v, want reading index error", err)
		}
	})

	t.Run("corrupt index", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ideas"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ideas", "index.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(dir)
		if err == nil || !strings.Contains(err.Error(), "corrupt index") {
			t.Fatalf("err = %v, want corrupt index error", err)
		}
	})

	t.Run("empty index list is a fresh store", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ideas"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ideas", "index.json"), []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := New(dir)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ideas, err := s.ListSettled()
		if err != nil || len(ideas) != 0 {
			t.Fatalf("ListSettled = %v, %v; want empty, nil", ideas, err)
		}
	})
}

// TestPingDataDirNotADirectory covers the readiness failure when the data
// path still exists but has been replaced by a regular file.
func TestPingDataDirNotADirectory(t *testing.T) {
	s, _ := newTestStore(t)
	file := filepath.Join(t.TempDir(), "plainfile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.dir = file
	s.mu.Unlock()
	err := s.Ping()
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Ping = %v, want not a directory error", err)
	}
}
