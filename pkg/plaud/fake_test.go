package plaud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/hivecommons/dibs/pkg/auth"
	"github.com/hivecommons/dibs/pkg/deps"
	"github.com/hivecommons/dibs/pkg/mcpserver"
	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeEnv makes the test binary act as a fake Plaud MCP server over stdio
// (helper-process pattern), so Dial is exercised without npx.
const fakeEnv = "DIBS_PLAUD_FAKE_MCP"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		_ = defaultFake().server().Run(context.Background(), &mcpsdk.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakePlaud is a fake Plaud MCP server.
type fakePlaud struct {
	list        string
	listErr     string
	notes       map[string]string
	transcripts map[string]string

	mu    sync.Mutex
	calls []string
}

func defaultFake() *fakePlaud {
	return &fakePlaud{
		list:        `{"files":[{"id":"rec-1","name":"Standup idea","start_at":"2026-11-10T09:00:00Z","duration":65000}]}`,
		notes:       map[string]string{"rec-1": `{"summary":"Build a voice inbox for Dibs."}`},
		transcripts: map[string]string{"rec-1": `[{"speaker":"Me","content":"What if Dibs took voice notes?"}]`},
	}
}

func textResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}

func errorResult(s string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}}
}

func (f *fakePlaud) server() *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fake-plaud", Version: "v0"}, nil)
	schema := map[string]any{"type": "object"}
	args := func(req *mcpsdk.CallToolRequest) map[string]any {
		var m map[string]any
		_ = json.Unmarshal(req.Params.Arguments, &m)
		return m
	}
	s.AddTool(&mcpsdk.Tool{Name: "list_files", InputSchema: schema}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		a := args(req)
		f.record(fmt.Sprintf("list_files page=%v", a["page"]))
		if f.listErr != "" {
			return errorResult(f.listErr), nil
		}
		if fmt.Sprint(a["page"]) != "1" {
			return textResult("[]"), nil
		}
		return textResult(f.list), nil
	})
	lookup := func(name string, m map[string]string) {
		s.AddTool(&mcpsdk.Tool{Name: name, InputSchema: schema}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			id := fmt.Sprint(args(req)["file_id"])
			f.record(name + " " + id)
			v, ok := m[id]
			if !ok {
				return errorResult("file not found: " + id + "\nmore detail"), nil
			}
			return textResult(v), nil
		})
	}
	lookup("get_note", f.notes)
	lookup("get_transcript", f.transcripts)
	s.AddTool(&mcpsdk.Tool{Name: "multi", InputSchema: schema}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "one"},
			&mcpsdk.ImageContent{Data: []byte("x"), MIMEType: "image/png"},
			&mcpsdk.TextContent{Text: "two"},
		}}, nil
	})
	return s
}

func (f *fakePlaud) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// connectFake connects a Client to f over in-memory transports.
func connectFake(t *testing.T, f *fakePlaud) *Client {
	t.Helper()
	ct, st := mcpsdk.NewInMemoryTransports()
	ss, err := f.server().Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	c, err := Connect(context.Background(), ct)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// dibsServer runs the real Dibs MCP handler at /mcp, authenticating the
// bearer token "hub-token" as alice.
func dibsServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewHandler(mcpserver.Config{
		Hub:  &auth.FakeHub{BearerTokens: map[string]auth.Identity{"hub-token": {Username: "alice", DisplayName: "Alice"}}},
		Deps: deps.Deps{Store: st, Registry: reg},
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}
