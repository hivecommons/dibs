// Package match scores idea↔repo fit. Scores come from an LLM reached
// through hive's litellm gateway (any OpenAI-compatible chat-completions
// endpoint) when configured, with a deterministic keyword/topic-overlap
// fallback so the product fully works without a gateway. TLDRs and scores
// are computed lazily and cached on the idea record; the cache invalidates
// when the idea's content changes (store.Update clears it) or the repo
// profile changes (fingerprinted via RepoHash).
package match

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Env vars configuring the LLM gateway.
const (
	EnvLLMBaseURL = "DIBS_LLM_BASE_URL" // e.g. http://litellm:4000/v1 — empty disables the LLM (fallback only)
	EnvLLMAPIKey  = "DIBS_LLM_API_KEY"
	EnvLLMModel   = "DIBS_LLM_MODEL"
)

// DefaultModel is used when DIBS_LLM_MODEL is unset — litellm routes model
// aliases, so any name the gateway knows works here.
const DefaultModel = "gpt-4o-mini"

const llmRequestTimeout = 30 * time.Second

// maxLLMResponse bounds the gateway response we are willing to parse.
const maxLLMResponse = 1 << 20

// LLM is a minimal OpenAI-compatible chat-completions client.
type LLM struct {
	BaseURL string // includes /v1, e.g. http://litellm:4000/v1
	APIKey  string
	Model   string
	Client  *http.Client
}

// envValue reads a DIBS_-prefixed var, honoring the legacy IDEATE_-prefixed
// name (the product's pre-rename env prefix) as a fallback.
func envValue(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return os.Getenv("IDEATE_" + strings.TrimPrefix(key, "DIBS_"))
}

// LLMFromEnv builds an LLM from the DIBS_LLM_* env vars (legacy IDEATE_LLM_*
// names still honored), or nil when DIBS_LLM_BASE_URL is unset
// (fallback-only mode).
func LLMFromEnv() *LLM {
	base := strings.TrimSpace(envValue(EnvLLMBaseURL))
	if base == "" {
		return nil
	}
	model := strings.TrimSpace(envValue(EnvLLMModel))
	if model == "" {
		model = DefaultModel
	}
	return &LLM{
		BaseURL: strings.TrimRight(base, "/"),
		APIKey:  envValue(EnvLLMAPIKey),
		Model:   model,
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Chat sends one system+user exchange and returns the assistant's reply.
func (l *LLM) Chat(ctx context.Context, system, user string) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: l.Model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("match: marshaling chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("match: building chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if l.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: llmRequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("match: llm gateway unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("match: llm gateway returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var cr chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLLMResponse)).Decode(&cr); err != nil {
		return "", fmt.Errorf("match: decoding llm response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("match: llm returned no choices")
	}
	return strings.TrimSpace(cr.Choices[0].Message.Content), nil
}
