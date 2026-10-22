package auth

import (
	"strings"

	"github.com/hivecommons/dibs/pkg/envutil"
)

const adminsEnv = "DIBS_ADMINS"

// IsAdmin reports whether login is listed in DIBS_ADMINS.
func IsAdmin(login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return false
	}
	for _, admin := range strings.Split(envutil.Get(adminsEnv), ",") {
		if strings.EqualFold(strings.TrimSpace(admin), login) {
			return true
		}
	}
	return false
}
