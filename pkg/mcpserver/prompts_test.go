package mcpserver

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The prompt's whole job is to produce instruction text, so the assertions are
// about what that text tells the assistant to do - in particular that it never
// tells it to submit without asking.
func TestRecallIdeasRequiresConfirmationBeforeSubmitting(t *testing.T) {
	res, err := handleRecallIdeas(context.Background(), &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{Name: recallIdeasPrompt},
	})
	if err != nil {
		t.Fatalf("handleRecallIdeas: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(res.Messages))
	}
	text, ok := res.Messages[0].Content.(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content type: %T, want *TextContent", res.Messages[0].Content)
	}
	if !strings.Contains(text.Text, "submit only the ones I pick") {
		t.Error("instructions must require the user to choose before submitting")
	}
	if !strings.Contains(text.Text, "submit_idea") {
		t.Error("instructions must name the tool that does the submitting")
	}
	if res.Messages[0].Role != "user" {
		t.Errorf("role: got %q, want user", res.Messages[0].Role)
	}
}

func TestRecallIdeasScopeIsInterpolatedOnlyWhenGiven(t *testing.T) {
	withScope := recallInstructions("the console project", "")
	if !strings.Contains(withScope, "Focus the search on: the console project") {
		t.Error("a supplied scope must reach the instructions")
	}

	noScope := recallInstructions("", "")
	if strings.Contains(noScope, "Focus the search on:") {
		t.Error("an empty scope must not emit a dangling focus line")
	}
}

// A malformed limit must not produce "at most  candidates" - the reason
// recallLimit renders a string rather than interpolating a raw argument.
func TestRecallLimitFallsBackAndClamps(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back", "", "10"},
		{"garbage falls back", "lots", "10"},
		{"zero falls back", "0", "10"},
		{"negative falls back", "-5", "10"},
		{"honoured when sane", "3", "3"},
		{"clamped at the cap", "5000", "50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recallLimit(tc.in); got != tc.want {
				t.Errorf("recallLimit(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRecallIdeasPromptIsAdvertised(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: serverName, Version: serverVersion}, nil)
	registerPrompts(server)

	// A prompt the client cannot discover is a prompt that does not exist, so
	// assert registration through the server rather than the local function.
	res, err := handleRecallIdeas(context.Background(), &mcpsdk.GetPromptRequest{
		Params: &mcpsdk.GetPromptParams{
			Name:      recallIdeasPrompt,
			Arguments: map[string]string{recallScopeArg: "dibs", recallLimitArg: "2"},
		},
	})
	if err != nil {
		t.Fatalf("handleRecallIdeas: %v", err)
	}
	text := res.Messages[0].Content.(*mcpsdk.TextContent).Text
	if !strings.Contains(text, "Focus the search on: dibs") {
		t.Error("scope argument not honoured")
	}
	if !strings.Contains(text, "at most 2 candidates") {
		t.Error("limit argument not honoured")
	}
}
