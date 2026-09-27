package settle

import "testing"

func TestFromEnvUnsetReturnsNil(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv("IDEATE_GITHUB_TOKEN", "")
	if c := FromEnv(); c != nil {
		t.Fatalf("FromEnv() with no token = %+v, want nil", c)
	}
}

func TestFromEnvPrimaryToken(t *testing.T) {
	t.Setenv(EnvGitHubToken, "  primary-tok  ")
	t.Setenv("IDEATE_GITHUB_TOKEN", "legacy-tok")
	c := FromEnv()
	if c == nil || c.Token != "primary-tok" {
		t.Fatalf("FromEnv() = %+v, want Token=primary-tok", c)
	}
}

func TestFromEnvLegacyFallback(t *testing.T) {
	t.Setenv(EnvGitHubToken, "")
	t.Setenv("IDEATE_GITHUB_TOKEN", "legacy-tok")
	c := FromEnv()
	if c == nil || c.Token != "legacy-tok" {
		t.Fatalf("FromEnv() = %+v, want Token=legacy-tok", c)
	}
}
