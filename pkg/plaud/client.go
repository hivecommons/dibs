// Package plaud ingests Plaud Note recordings into Dibs. It reads recordings
// through the official Plaud MCP server over stdio and submits each one as a
// private draft idea through the Dibs MCP endpoint (submit_idea).
//
// Plaud's OAuth token stays in the Plaud MCP server's own store
// (~/.plaud/tokens-mcp.json); this package never reads or copies it.
package plaud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultCommand launches the official (free) Plaud MCP server.
const DefaultCommand = "npx -y @plaud-ai/mcp@latest"

var clientImpl = &mcpsdk.Implementation{Name: "dibs-ingest-plaud", Version: "v1"}

// Tools is the subset of an MCP client session the ingester needs.
type Tools interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
	Close() error
}

// Client is a minimal MCP client session: initialize handshake, then
// tools/call returning the text content.
type Client struct {
	session *mcpsdk.ClientSession
}

// Connect performs the MCP initialize handshake over t.
func Connect(ctx context.Context, t mcpsdk.Transport) (*Client, error) {
	session, err := mcpsdk.NewClient(clientImpl, nil).Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("plaud: connecting to MCP server: %w", err)
	}
	return &Client{session: session}, nil
}

// Dial spawns command (split on whitespace; no shell quoting) and connects
// to it over stdin/stdout. The server's stderr is forwarded to stderr, which
// is how Plaud's sign-in URL reaches the user on the first run. The child
// gets an allowlisted environment (see ChildEnv), not the caller's: the
// server is a third-party package fetched from the registry on every start
// and has no use for the hub bearer token or anything else the caller's
// shell exports. A non-empty home becomes the child's $HOME, so the Plaud
// MCP server keeps its OAuth token store (~/.plaud) under that directory
// instead of the caller's home.
func Dial(ctx context.Context, command, home string, stderr io.Writer) (*Client, error) {
	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, errors.New("plaud: empty MCP command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = ChildEnv(os.Environ(), home)
	cmd.Stderr = stderr
	return Connect(ctx, &mcpsdk.CommandTransport{Command: cmd})
}

// childEnvNames are the variables passed to the MCP server by exact name
// (compared upper-cased): what a Node process needs to start, find npm's
// cache and reach the registry through a proxy, plus the Windows basics.
var childEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "TZ": true, "TERM": true,
	"LANG": true, "LANGUAGE": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"SYSTEMROOT": true, "SYSTEMDRIVE": true, "WINDIR": true, "COMSPEC": true, "PATHEXT": true,
	"APPDATA": true, "LOCALAPPDATA": true, "USERPROFILE": true, "PROGRAMFILES": true, "PROGRAMDATA": true,
}

// childEnvPrefixes are passed by upper-cased prefix: locale, XDG dirs, Node
// and npm settings (NODE_EXTRA_CA_CERTS, npm_config_*), the Plaud server's
// own settings and this subcommand's own $DIBS_PLAUD_* settings (which are
// never secrets; $DIBS_TOKEN and $DIBS_URL do not match).
var childEnvPrefixes = []string{"LC_", "XDG_", "NODE_", "NPM_CONFIG_", "PLAUD_", "DIBS_PLAUD_"}

// childEnvSecretMarkers drop a variable even when its name is allowlisted,
// so NODE_AUTH_TOKEN or npm_config__authToken cannot ride in on a prefix.
var childEnvSecretMarkers = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "AUTH"}

// ChildEnv filters environ (os.Environ() form) down to the variables the
// Plaud MCP server is allowed to see. A non-empty home replaces HOME.
func ChildEnv(environ []string, home string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !childEnvAllowed(name) {
			continue
		}
		if home != "" && strings.EqualFold(name, "HOME") {
			continue
		}
		out = append(out, kv)
	}
	if home != "" {
		out = append(out, "HOME="+home)
	}
	return out
}

func childEnvAllowed(name string) bool {
	upper := strings.ToUpper(name)
	for _, m := range childEnvSecretMarkers {
		if strings.Contains(upper, m) {
			return false
		}
	}
	if childEnvNames[upper] {
		return true
	}
	for _, p := range childEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// CallTool invokes a tool and returns its text content joined by newlines.
// A tool-level error (isError) is returned as an error.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	res, err := c.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("plaud: %s: %w", name, err)
	}
	text := resultText(res)
	if res.IsError {
		return "", fmt.Errorf("plaud: %s: %s", name, firstLine(text))
	}
	return text, nil
}

// Close ends the session (and stops a spawned server).
func (c *Client) Close() error { return c.session.Close() }

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
