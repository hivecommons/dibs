package main

import "testing"

func TestGithubTokenFromEnvTrims(t *testing.T) {
	t.Setenv("DIBS_GITHUB_TOKEN", "tok\n")
	if got := githubTokenFromEnv(); got != "tok" {
		t.Fatalf("got %q, want %q", got, "tok")
	}
}
