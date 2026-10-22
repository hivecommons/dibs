// Package envutil reads configuration with the legacy IDEATE_ prefix fallback.
package envutil

import (
	"os"
	"strings"
)

// Get returns the first non-empty value of key and, for DIBS_-prefixed
// keys only, the corresponding IDEATE_-prefixed key. Empty values are
// treated as unset; values are otherwise returned unchanged.
func Get(key string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	if suffix, ok := strings.CutPrefix(key, "DIBS_"); ok {
		return os.Getenv("IDEATE_" + suffix)
	}
	return ""
}

// GetOr returns Get(key), or def when neither name has a non-empty value.
func GetOr(key, def string) string {
	if value := Get(key); value != "" {
		return value
	}
	return def
}
