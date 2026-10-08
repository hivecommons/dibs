package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestHTTPHubClientBadBaseURL pins the request-construction arm of both
// WhoAmI and WhoAmIBearer: a BaseURL that cannot form a URL (a control
// character in the host) must surface as a wrapped "building hub request"
// error — never as ErrUnauthenticated, which would log the user out.
func TestHTTPHubClientBadBaseURL(t *testing.T) {
	c := &HTTPHubClient{BaseURL: "http://bad\x7fhost"}

	for name, call := range map[string]func() (*Identity, error){
		"cookie": func() (*Identity, error) { return c.WhoAmI(context.Background(), "sess") },
		"bearer": func() (*Identity, error) { return c.WhoAmIBearer(context.Background(), "tok") },
	} {
		id, err := call()
		if id != nil {
			t.Fatalf("%s: identity = %+v, want nil", name, id)
		}
		if err == nil || !strings.Contains(err.Error(), "building hub request") {
			t.Fatalf("%s: err = %v, want building hub request", name, err)
		}
		if errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("%s: malformed BaseURL must not read as unauthenticated: %v", name, err)
		}
	}
}
