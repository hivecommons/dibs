package ingest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const submitTimeout = time.Minute

var sinkImpl = &mcpsdk.Implementation{Name: "dibs-ingest", Version: "v1"}

// DibsSink submits ideas with the Dibs MCP submit_idea tool (private
// draft owned by the token's identity).
type DibsSink struct {
	// Endpoint is the Dibs MCP URL, e.g. https://dibs.hivecommons.dev/mcp.
	Endpoint string
	// Token is the hub bearer token. It is only ever sent in the
	// Authorization header, never logged.
	Token string
	// Transport defaults to http.DefaultTransport.
	Transport http.RoundTripper
}

// Submit implements Sink.
func (s *DibsSink) Submit(ctx context.Context, idea Idea) error {
	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	base := s.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt := &bearerTransport{base: base, token: s.Token}
	session, err := mcpsdk.NewClient(sinkImpl, nil).Connect(ctx, &mcpsdk.StreamableClientTransport{
		Endpoint:             s.Endpoint,
		HTTPClient:           &http.Client{Transport: rt},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return rt.wrap(err)
	}
	defer session.Close()
	tags := idea.Tags
	if tags == nil {
		tags = []string{}
	}
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "submit_idea",
		Arguments: map[string]any{"title": idea.Title, "description": idea.Description, "tags": tags},
	})
	if err != nil {
		return rt.wrap(err)
	}
	if res.IsError {
		return fmt.Errorf("dibs: submit_idea: %s", firstLine(resultText(res)))
	}
	return nil
}

// bearerTransport adds the Authorization header and remembers a 429.
type bearerTransport struct {
	base    http.RoundTripper
	token   string
	limited atomic.Bool
}

func (t *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusTooManyRequests {
		t.limited.Store(true)
	}
	return resp, err
}

func (t *bearerTransport) wrap(err error) error {
	if t.limited.Load() {
		return fmt.Errorf("%w: %v", ErrRateLimited, err)
	}
	return fmt.Errorf("dibs: submit_idea: %w", err)
}

func resultText(res *mcpsdk.CallToolResult) string {
	var parts []string
	for _, content := range res.Content {
		if t, ok := content.(*mcpsdk.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
