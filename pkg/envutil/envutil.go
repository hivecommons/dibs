// Package envutil reads DIBS_* environment variables with the legacy
// IDEATE_* prefix honored as a fallback.
package envutil

import (
	"os"
	"strings"
)

// Get returns os.Getenv(key). When that is empty and key has the DIBS_
// prefix, it returns the value of the legacy IDEATE_-prefixed name (the
// product's pre-rename env prefix).
func Get(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if after, ok := strings.CutPrefix(key, "DIBS_"); ok {
		return os.Getenv("IDEATE_" + after)
	}
	return ""
}

// GetOr is Get with a default for when the variable is unset or empty.
func GetOr(key, def string) string {
	if v := Get(key); v != "" {
		return v
	}
	return def
}
