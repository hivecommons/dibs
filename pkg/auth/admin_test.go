package auth

import "testing"

func TestIsAdminParsesEnv(t *testing.T) {
	t.Setenv(adminsEnv, " Alice, BOB ,,carol ")
	for _, login := range []string{"alice", "ALICE", " bob ", "Carol"} {
		if !IsAdmin(login) {
			t.Fatalf("IsAdmin(%q) = false, want true", login)
		}
	}
	for _, login := range []string{"", "dave", "alic"} {
		if IsAdmin(login) {
			t.Fatalf("IsAdmin(%q) = true, want false", login)
		}
	}
}

func TestIsAdminLegacyEnvFallback(t *testing.T) {
	t.Setenv(adminsEnv, "")
	t.Setenv("IDEATE_ADMINS", "alice")
	if !IsAdmin("alice") {
		t.Fatal("IsAdmin(alice) = false, want true via IDEATE_ADMINS")
	}
}
