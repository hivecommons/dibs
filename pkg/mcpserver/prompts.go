package mcpserver

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The recall_ideas prompt turns an assistant's own history with the user into
// Dibs submissions. It exists because the ideas worth listing are usually not
// the ones a user thinks to type into a form: they are the ones mentioned in
// passing months ago and never acted on. Only the assistant can see those, so
// the server cannot do this work itself - it can only ask well.
const (
	recallIdeasPrompt = "recall_ideas"
	// scopeArg narrows the sweep. Empty means "everything you can see".
	recallScopeArg = "scope"
	// limitArg caps how many candidates come back, so the reply stays readable.
	recallLimitArg = "limit"
	// defaultRecallLimit is a shortlist a person will actually read to the end.
	defaultRecallLimit = 10
	// maxRecallLimit stops an argument from asking for an unreviewable dump.
	maxRecallLimit = 50
)

func registerPrompts(server *mcpsdk.Server) {
	server.AddPrompt(&mcpsdk.Prompt{
		Name:        recallIdeasPrompt,
		Title:       "Recall stranded ideas",
		Description: "Search your memory and past conversations with this user for product ideas, feature wishes, requirements or specs they described but never acted on, then offer to submit the good ones to Dibs.",
		Arguments: []*mcpsdk.PromptArgument{
			{
				Name:        recallScopeArg,
				Description: "Optional focus for the sweep, e.g. a project name, a repo, or a time period like \"the last year\". Omit to search everything available.",
			},
			{
				Name:        recallLimitArg,
				Description: fmt.Sprintf("Maximum candidates to report. Defaults to %d, capped at %d.", defaultRecallLimit, maxRecallLimit),
			},
		},
	}, handleRecallIdeas)
}

func handleRecallIdeas(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	var scope, limit string
	if req != nil && req.Params != nil {
		scope = strings.TrimSpace(req.Params.Arguments[recallScopeArg])
		limit = strings.TrimSpace(req.Params.Arguments[recallLimitArg])
	}

	return &mcpsdk.GetPromptResult{
		Description: "Recall unacted-on ideas from this user's history and offer to list them on Dibs.",
		Messages: []*mcpsdk.PromptMessage{{
			Role:    "user",
			Content: &mcpsdk.TextContent{Text: recallInstructions(scope, limit)},
		}},
	}, nil
}

// recallInstructions builds the instruction text. It is deliberately explicit
// about not submitting anything unasked: the tool it points at writes to a
// public listing under the user's name, so a confirmation step is part of the
// prompt's contract rather than a nicety.
func recallInstructions(scope, limit string) string {
	var b strings.Builder

	b.WriteString("Search your memory and your past conversations with me for ideas I described but never acted on: product ideas, feature wishes, \"someone should build\" asides, requirements, or specs.\n\n")

	if scope != "" {
		fmt.Fprintf(&b, "Focus the search on: %s\n\n", scope)
	}

	fmt.Fprintf(&b, "Report at most %s candidates. For each one:\n", recallLimit(limit))
	b.WriteString("- a one-line title\n")
	b.WriteString("- two or three sentences of description, in enough detail that someone who was not in the original conversation could act on it\n")
	b.WriteString("- when I mentioned it, and what I was working on at the time\n")
	b.WriteString("- whether anything came of it, as far as you can tell\n\n")

	b.WriteString("Leave out anything I clearly finished, abandoned on purpose, or that was someone else's idea rather than mine. Prefer specific ideas over vague wishes: \"a CLI that diffs two Helm releases\" is worth listing, \"better tooling\" is not.\n\n")

	b.WriteString("Then ask me which ones to list on Dibs, and submit only the ones I pick, using the submit_idea tool. Do not submit anything before I choose - submit_idea creates a public listing under my name. If you have no bearer token configured, say so instead of guessing, and point me at the endpoint's help page.\n\n")

	b.WriteString("If you find nothing worth listing, say that plainly rather than padding the list.")

	return b.String()
}

// recallLimit renders the requested cap, falling back to the default when the
// argument is missing or not a sane positive number. It returns a string
// because the value is interpolated into prose, and because an assistant reads
// "10" and "ten" identically while a malformed argument should not produce
// "at most  candidates".
func recallLimit(raw string) string {
	n := defaultRecallLimit
	if raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > maxRecallLimit {
		n = maxRecallLimit
	}
	return fmt.Sprintf("%d", n)
}
