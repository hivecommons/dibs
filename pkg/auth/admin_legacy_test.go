package auth

import "testing"

func TestAdminLegacyConfiguration(t *testing.T) {
	t.Setenv("DIBS_ADMINS", "")
	t.Setenv("IDEATE_ADMINS", "alice")
	if !IsAdmin("alice") {
		t.Fatal("legacy admins ignored")
	}
	t.Setenv("DIBS_ADMINS", "bob")
	if IsAdmin("alice") || !IsAdmin("bob") {
		t.Fatal("current admins must take precedence")
	}
}
