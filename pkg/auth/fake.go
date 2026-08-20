package auth

import "context"

// FakeHub is an in-memory HubClient for tests and local development. Sessions
// maps a session-cookie value to the identity it authenticates.
type FakeHub struct {
	Sessions     map[string]Identity
	BearerTokens map[string]Identity
}

// WhoAmI implements HubClient.
func (f *FakeHub) WhoAmI(_ context.Context, sessionCookie string) (*Identity, error) {
	if id, ok := f.Sessions[sessionCookie]; ok {
		return &id, nil
	}
	return nil, ErrUnauthenticated
}

// WhoAmIBearer implements BearerHubClient.
func (f *FakeHub) WhoAmIBearer(_ context.Context, token string) (*Identity, error) {
	if id, ok := f.BearerTokens[token]; ok {
		return &id, nil
	}
	return nil, ErrUnauthenticated
}
