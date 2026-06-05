package auth

import "strings"

// DeriveIdentity turns an email (and optional display name) into a DNS/namespace-
// safe handle and a git author name. The handle is the email local-part lowered
// with non-alphanumeric runs collapsed to single hyphens (e.g.
// "Ravi.Panchal@ibm.com" -> "ravi-panchal"). gitName falls back to the handle
// when no display name is present.
func DeriveIdentity(email, displayName string) (handle, gitName string) {
	local := email
	if i := strings.IndexByte(email, '@'); i >= 0 {
		local = email[:i]
	}
	handle = slug(local)
	gitName = strings.TrimSpace(displayName)
	if gitName == "" {
		gitName = handle
	}
	return handle, gitName
}

func slug(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
