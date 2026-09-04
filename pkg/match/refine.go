package match

// LLM-assisted idea refinement. Two moments use it: when POSTING an idea
// ("✨ Refine with AI" — rough title/body → a structured draft) and when
// SUBMITTING an accepted idea to its repo (expand into a GitHub-issue-quality
// description tailored to that repo). Both are strictly suggestions: the
// user always edits/decides, and without an LLM the step is simply skipped
// (Refine returns nil — the graceful fallback).

import (
	"context"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/hivecommons/dibs/pkg/registry"
	"github.com/hivecommons/dibs/pkg/store"
)

// RefinedDraft is an LLM-improved title/body suggestion.
type RefinedDraft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const refineSystemPrompt = `You improve rough open-source project idea drafts into well-structured write-ups.
Produce a markdown body with these sections: a clear problem statement, who benefits, and acceptance criteria (a checklist of what "done" looks like).
Reply with ONLY the improved title on the first line, then a blank line, then the improved markdown body. No preamble, no code fences.`

const expandSystemPrompt = `You expand an accepted open-source project idea into a complete, GitHub-issue-quality description tailored to the target repository.
Use the repository's name, description, and topics to ground terminology and scope. Include: a problem statement, motivation / who benefits, a proposed approach, and acceptance criteria (a checklist).
Reply with ONLY the issue title on the first line, then a blank line, then the full markdown issue body. No preamble, no code fences.`

// Refine asks the LLM to improve title/body. When repo is non-nil the
// prompt targets that repo (the pre-submission expansion); otherwise it is
// the generic posting-time refinement. Returns nil — skip the step — when
// no LLM is configured or the LLM fails: refinement is never load-bearing.
func (e *Engine) Refine(ctx context.Context, title, body string, repo *registry.RepoProfile) *RefinedDraft {
	if e == nil || e.LLM == nil {
		return nil
	}
	system := refineSystemPrompt
	user := "Title: " + title + "\n\nBody:\n" + truncate(body, maxPromptBody)
	if repo != nil {
		system = expandSystemPrompt
		user = "TARGET REPOSITORY\nName: " + repo.RepoID +
			"\nDescription: " + repo.Description +
			"\nTopics: " + strings.Join(repo.Topics, ", ") +
			"\nAppetite: " + repo.Appetite +
			"\n\nIDEA\n" + user
	}
	out, err := e.LLM.Chat(ctx, system, user)
	if err != nil {
		log.Printf("match: refine llm failed, skipping refinement: %v", err)
		return nil
	}
	draft := parseRefined(out)
	if draft == nil {
		log.Printf("match: refine llm reply unparsable, skipping refinement: %.80s", out)
	}
	return draft
}

// parseRefined splits an LLM reply into title (first line) and body (the
// rest), enforcing the store's field limits. Returns nil when either part
// is empty.
func parseRefined(out string) *RefinedDraft {
	out = strings.TrimSpace(out)
	title, body, ok := strings.Cut(out, "\n")
	if !ok {
		return nil
	}
	title = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(strings.TrimSpace(title), "# "), "Title:"))
	body = strings.TrimSpace(body)
	if title == "" || body == "" {
		return nil
	}
	title = truncate(title, store.MaxTitleLen)
	if len(body) > store.MaxBodyBytes {
		b := body[:store.MaxBodyBytes-len("…")]
		for len(b) > 0 && !utf8.ValidString(b) {
			b = b[:len(b)-1] // back off to a rune boundary
		}
		body = b + "…"
	}
	return &RefinedDraft{Title: title, Body: body}
}
