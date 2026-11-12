package plaud

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestClientCallToolReturnsText(t *testing.T) {
	c := connectFake(t, defaultFake())
	got, err := c.CallTool(context.Background(), "get_note", map[string]any{"file_id": "rec-1"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(got, "voice inbox") {
		t.Fatalf("get_note = %q", got)
	}
	got, err = c.CallTool(context.Background(), "multi", nil)
	if err != nil || got != "one\ntwo" {
		t.Fatalf("multi = %q, %v; want text parts joined, non-text skipped", got, err)
	}
}

func TestClientCallToolErrors(t *testing.T) {
	c := connectFake(t, defaultFake())
	_, err := c.CallTool(context.Background(), "get_note", map[string]any{"file_id": "missing"})
	if err == nil || !strings.Contains(err.Error(), "file not found: missing") || strings.Contains(err.Error(), "more detail") {
		t.Fatalf("tool error = %v, want first line of the isError text", err)
	}
	if _, err := c.CallTool(context.Background(), "no_such_tool", nil); err == nil {
		t.Fatal("unknown tool: want error")
	}
}

func TestDialSpawnsStdioServer(t *testing.T) {
	t.Setenv(fakeEnv, "1")
	c, err := Dial(context.Background(), os.Args[0], "", io.Discard)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	got, err := c.CallTool(context.Background(), "list_files", map[string]any{"page": 1})
	if err != nil || !strings.Contains(got, "rec-1") {
		t.Fatalf("list_files = %q, %v", got, err)
	}
}

func TestDialScrubsHubTokenFromChildEnv(t *testing.T) {
	t.Setenv(fakeEnv, "1")
	t.Setenv(EnvToken, "secret-xyz")
	t.Setenv("GITHUB_TOKEN", "ghp-secret")
	t.Setenv("NODE_AUTH_TOKEN", "npm-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("PATH", "/plaud-test-path")
	t.Setenv("PLAUD_REGION", "eu")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/plaud-test-ca.pem")
	c, err := Dial(context.Background(), os.Args[0], "", io.Discard)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	got, err := c.CallTool(context.Background(), "env", nil)
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	for _, leaked := range []string{"secret-xyz", "ghp-secret", "npm-secret", "aws-secret"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("credential %q reached the child: %q", leaked, got)
		}
	}
	for _, want := range []string{"PATH=/plaud-test-path", "PLAUD_REGION=eu", "NODE_EXTRA_CA_CERTS=/plaud-test-ca.pem"} {
		if !strings.Contains(got, want) {
			t.Fatalf("allowlisted env %q not inherited: %q", want, got)
		}
	}
}

func TestChildEnvAllowlist(t *testing.T) {
	in := []string{
		"PATH=/bin", "HOME=/caller", "LANG=C.UTF-8", "LC_ALL=C", "TMPDIR=/tmp", "XDG_CACHE_HOME=/c",
		"HTTPS_PROXY=http://proxy:3128", "no_proxy=localhost", "SSL_CERT_FILE=/ca.pem",
		"NODE_OPTIONS=--max-old-space-size=512", "npm_config_cache=/npm", "PLAUD_REGION=eu",
		"DIBS_PLAUD_HOME=/plaud", "Path=C:\\Windows", "SystemRoot=C:\\Windows",
		// dropped: not allowlisted
		"DIBS_TOKEN=hub", "DIBS_URL=https://dibs", "GITHUB_TOKEN=ghp", "GH_TOKEN=gho",
		"AWS_SECRET_ACCESS_KEY=aws", "OPENAI_API_KEY=sk", "HIVE_HUB_SECRET=hub", "SSH_AUTH_SOCK=/s",
		// dropped: allowlisted prefix, secret-looking name
		"NODE_AUTH_TOKEN=npm", "npm_config__authToken=npm", "npm_config__auth=basic", "PLAUD_PASSWORD=pw", "DIBS_PLAUD_TOKEN=x",
		"malformed",
	}
	got := strings.Join(ChildEnv(in, ""), "\n")
	for _, want := range []string{
		"PATH=/bin", "HOME=/caller", "LANG=C.UTF-8", "LC_ALL=C", "TMPDIR=/tmp", "XDG_CACHE_HOME=/c",
		"HTTPS_PROXY=http://proxy:3128", "no_proxy=localhost", "SSL_CERT_FILE=/ca.pem",
		"NODE_OPTIONS=--max-old-space-size=512", "npm_config_cache=/npm", "PLAUD_REGION=eu",
		"DIBS_PLAUD_HOME=/plaud", "Path=C:\\Windows", "SystemRoot=C:\\Windows",
	} {
		if !strings.Contains(got+"\n", want+"\n") {
			t.Errorf("ChildEnv dropped %q", want)
		}
	}
	for _, dropped := range []string{
		"DIBS_TOKEN", "DIBS_URL", "GITHUB_TOKEN", "GH_TOKEN", "AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY",
		"HIVE_HUB_SECRET", "SSH_AUTH_SOCK", "NODE_AUTH_TOKEN", "npm_config__authToken", "npm_config__auth",
		"PLAUD_PASSWORD", "DIBS_PLAUD_TOKEN", "malformed",
	} {
		if strings.Contains(got, dropped) {
			t.Errorf("ChildEnv kept %q", dropped)
		}
	}
	if got := ChildEnv([]string{"HOME=/caller", "PATH=/bin"}, "/plaud"); strings.Join(got, "\n") != "PATH=/bin\nHOME=/plaud" {
		t.Errorf("ChildEnv with home = %q, want caller HOME replaced", got)
	}
}

func TestDialOverridesChildHome(t *testing.T) {
	t.Setenv(fakeEnv, "1")
	t.Setenv("HOME", "/caller-home")
	c, err := Dial(context.Background(), os.Args[0], "/plaud-home", io.Discard)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	got, err := c.CallTool(context.Background(), "env", nil)
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if !strings.Contains(got, "HOME=/plaud-home") || strings.Contains(got, "/caller-home") {
		t.Fatalf("child HOME not overridden: %q", got)
	}
}

func TestDialErrors(t *testing.T) {
	if _, err := Dial(context.Background(), "   ", "", io.Discard); err == nil {
		t.Fatal("empty command: want error")
	}
	if _, err := Dial(context.Background(), "/nonexistent/plaud-mcp --flag", "", io.Discard); err == nil {
		t.Fatal("missing binary: want error")
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  a\nb "); got != "a" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Fatalf("firstLine = %q", got)
	}
}
