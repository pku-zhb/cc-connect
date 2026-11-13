package acp

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// usageBackend identifies which account-quota source an ACP session uses.
// GetUsage dispatches on this value; it does not try one backend and then
// another.
type usageBackend string

const (
	usageBackendNone usageBackend = ""
	usageBackendMuse usageBackend = "muse"
	usageBackendAgy  usageBackend = "agy"
)

func parseUsageBackend(raw string) (usageBackend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none", "off":
		return usageBackendNone, nil
	case "muse":
		return usageBackendMuse, nil
	case "agy", "antigravity":
		return usageBackendAgy, nil
	default:
		return "", fmt.Errorf("acp: unsupported usage_backend %q (want muse or agy)", raw)
	}
}

// resolveUsageBackend picks a single backend. An explicit usage_backend option
// always wins. Otherwise the configured command and display name are matched
// as identities. Ambiguous or unknown identities stay unset; callers must not
// invent a backend by probing.
func resolveUsageBackend(explicit, command, displayName string) (usageBackend, error) {
	parsed, err := parseUsageBackend(explicit)
	if err != nil {
		return "", err
	}
	if parsed != usageBackendNone {
		return parsed, nil
	}
	return inferUsageBackend(command, displayName), nil
}

func inferUsageBackend(command, displayName string) usageBackend {
	agy := matchesUsageIdentity(command, displayName, "agy", "antigravity")
	muse := matchesUsageIdentity(command, displayName, "muse")
	switch {
	case agy && !muse:
		return usageBackendAgy
	case muse && !agy:
		return usageBackendMuse
	default:
		return usageBackendNone
	}
}

func matchesUsageIdentity(command, displayName string, tokens ...string) bool {
	base := strings.TrimSuffix(filepath.Base(command), filepath.Ext(command))
	return hasIdentityToken(base, tokens...) || hasIdentityToken(displayName, tokens...)
}

func hasIdentityToken(s string, tokens ...string) bool {
	for _, part := range identityTokens(s) {
		for _, tok := range tokens {
			if part == tok {
				return true
			}
		}
	}
	return false
}

func identityTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
