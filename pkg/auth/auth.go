// Package auth bridges Ideate to the hive hub's session authentication.
//
// Ideate has no login of its own. It is served at idea.kubestellar.io and
// relies on the hive hub's session cookie ("hive_hub_user"). For the browser
// to send that cookie here, the hub must scope it to .kubestellar.io (or
// Ideate grows its own OAuth flow against the hub) — a deployment follow-up
// tracked in the Wave 1 PR. This package validates whatever cookie arrives by
// calling back to the hub and attaches the resulting identity to the request
// context.
//
// Hub contract (see the follow-up note in the Wave 1 PR): the hub exposes
//
//	GET {HUB_URL}/api/saas/whoami
//
// which, given a valid session cookie, returns 200 with
//
//	{"username":"...","display_name":"...","email":"...","avatar_url":"..."}
//
// and 401 otherwise. Until that endpoint lands hub-side, this package codes
// against the HubClient interface so tests (and any interim adapter) can
// substitute a fake.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"time"
)

// SessionCookieName is the hub's session cookie, set on the shared domain by
// the hub's OAuth flow (hive hub saas.go reads the same name).
const SessionCookieName = "hive_hub_user"

// WhoAmIPath is the hub endpoint that resolves a session cookie to a user.
const WhoAmIPath = "/api/saas/whoami"

// ErrUnauthenticated means the hub rejected (or could not find) the session.
var ErrUnauthenticated = errors.New("auth: not authenticated")

// Identity is the authenticated user attached to every request.
type Identity struct {
	// Username is the stable identity key: the GitHub login for GitHub users,
	// or the hub's canonical "provider:sub" form for other providers. It is
	// the key ideas and repo ownership are scoped by.
	Username    string `json:"username"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// HubClient resolves a hub session cookie to an identity.
type HubClient interface {
	// WhoAmI returns the identity for the given session cookie value, or
	// ErrUnauthenticated if the session is missing/expired/invalid.
	WhoAmI(ctx context.Context, sessionCookie string) (*Identity, error)
}

// HTTPHubClient is the production HubClient: it forwards the session cookie
// to the hub's whoami endpoint.
type HTTPHubClient struct {
	// BaseURL is the hub origin, e.g. https://hive.kubestellar.io.
	BaseURL string
	// Client defaults to a 10s-timeout client when nil.
	Client *http.Client
}

const hubRequestTimeout = 10 * time.Second

// maxWhoAmIBody bounds the hub response we are willing to parse.
const maxWhoAmIBody = 1 << 16

func (c *HTTPHubClient) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: hubRequestTimeout}
}

// WhoAmI implements HubClient against a real hub.
func (c *HTTPHubClient) WhoAmI(ctx context.Context, sessionCookie string) (*Identity, error) {
	if sessionCookie == "" {
		return nil, ErrUnauthenticated
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+WhoAmIPath, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: building hub request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sessionCookie})
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: hub unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var id Identity
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxWhoAmIBody)).Decode(&id); err != nil {
			return nil, fmt.Errorf("auth: decoding hub response: %w", err)
		}
		if id.Username == "" {
			return nil, fmt.Errorf("auth: hub returned empty username")
		}
		return &id, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrUnauthenticated
	default:
		return nil, fmt.Errorf("auth: hub returned unexpected status %d", resp.StatusCode)
	}
}

type ctxKey struct{}

// FromContext returns the identity attached by Middleware, or nil.
func FromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKey{}).(*Identity)
	return id
}

// WithIdentity attaches an identity to a context (exported for tests of
// downstream handlers).
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Middleware validates the hub session and attaches the identity.
//
// Unauthenticated requests get a 401 JSON body when isAPI(r) is true, and a
// friendly "sign in at the hub" interstitial page otherwise. hubURL is the
// human-facing hub origin used in that interstitial.
type Middleware struct {
	Hub    HubClient
	HubURL string
	// IsAPI classifies a request as API (JSON errors) vs page (HTML
	// interstitial). Nil means everything is API.
	IsAPI func(r *http.Request) bool
}

// Wrap returns next guarded by the session check.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var session string
		if c, err := r.Cookie(SessionCookieName); err == nil {
			session = c.Value
		}
		id, err := m.Hub.WhoAmI(r.Context(), session)
		if err != nil {
			m.reject(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

func (m *Middleware) reject(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusUnauthorized
	msg := "not authenticated"
	if !errors.Is(err, ErrUnauthenticated) {
		status = http.StatusBadGateway
		msg = "auth backend unavailable"
	}
	if m.IsAPI == nil || m.IsAPI(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = interstitialTmpl.Execute(w, map[string]string{"HubURL": m.HubURL})
}

var interstitialTmpl = template.Must(template.New("interstitial").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in — Ideate</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
background:#0d1117;color:#f6f8fb;font-family:Inter,ui-sans-serif,system-ui,-apple-system,sans-serif}
.card{background:#1c2128;border:1px solid #30363d;border-radius:12px;padding:40px 48px;max-width:460px;text-align:center}
h1{font-size:1.3rem;margin:0 0 12px}p{color:#a8b3c2;line-height:1.5;margin:0 0 24px}
a.pill{display:inline-block;background:#f4c75f;color:#080b0f;font-weight:700;text-decoration:none;
padding:10px 24px;border-radius:999px}
</style></head><body>
<div class="card">
<div style="font-size:2rem;margin-bottom:8px">&#128161;</div>
<h1>Sign in to Ideate</h1>
<p>Ideate uses your Hive Hub account. Sign in at hive.kubestellar.io, then come back &mdash; your session carries over automatically.</p>
<a class="pill" href="{{.HubURL}}">Sign in at hive.kubestellar.io</a>
</div></body></html>`))
