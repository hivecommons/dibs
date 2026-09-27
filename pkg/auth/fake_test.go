package auth

import (
	"context"
	"errors"
	"testing"
)

func TestFakeHubWhoAmI(t *testing.T) {
	f := &FakeHub{Sessions: map[string]Identity{"cookie-1": {Username: "alice"}}}

	id, err := f.WhoAmI(context.Background(), "cookie-1")
	if err != nil || id == nil || id.Username != "alice" {
		t.Fatalf("WhoAmI(cookie-1) = %+v, %v; want alice", id, err)
	}

	id, err = f.WhoAmI(context.Background(), "unknown")
	if !errors.Is(err, ErrUnauthenticated) || id != nil {
		t.Fatalf("WhoAmI(unknown) = %+v, %v; want ErrUnauthenticated", id, err)
	}
}

func TestFakeHubWhoAmIBearer(t *testing.T) {
	f := &FakeHub{BearerTokens: map[string]Identity{"tok-1": {Username: "bob"}}}

	id, err := f.WhoAmIBearer(context.Background(), "tok-1")
	if err != nil || id == nil || id.Username != "bob" {
		t.Fatalf("WhoAmIBearer(tok-1) = %+v, %v; want bob", id, err)
	}

	id, err = f.WhoAmIBearer(context.Background(), "unknown")
	if !errors.Is(err, ErrUnauthenticated) || id != nil {
		t.Fatalf("WhoAmIBearer(unknown) = %+v, %v; want ErrUnauthenticated", id, err)
	}
}
