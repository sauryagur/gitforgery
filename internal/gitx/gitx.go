// Package gitx holds small go-git helpers shared across gitforgery:
// identity parsing, ref pattern matching, and read-only commit hash previews.
package gitx

import (
	"fmt"
	"strings"
)

// Identity is a parsed "Name <email>" identity from a recipe.
type Identity struct {
	Name  string
	Email string
}

// String renders the identity in git's "Name <email>" form.
func (i Identity) String() string {
	return fmt.Sprintf("%s <%s>", i.Name, i.Email)
}

// ParseIdentity parses a git identity literal of the form "Name <email>".
// Both name and email must be non-empty, mirroring what git itself demands
// of committer identities. Surrounding whitespace is trimmed.
func ParseIdentity(s string) (Identity, error) {
	s = strings.TrimSpace(s)
	open := strings.LastIndexByte(s, '<')
	close := strings.LastIndexByte(s, '>')
	if open == -1 || close == -1 || close < open {
		return Identity{}, fmt.Errorf("invalid identity %q: want \"Name <email>\"", s)
	}
	name := strings.TrimSpace(s[:open])
	email := s[open+1 : close]
	if name == "" {
		return Identity{}, fmt.Errorf("invalid identity %q: empty name", s)
	}
	if email == "" {
		return Identity{}, fmt.Errorf("invalid identity %q: empty email", s)
	}
	return Identity{Name: name, Email: email}, nil
}

// MatchRefPattern reports whether ref matches a ref glob pattern such as
// "refs/heads/*" or "refs/tags/v*". '*' matches any sequence of characters,
// including '/', matching git refspec semantics rather than filepath.Match.
// A pattern without '*' requires an exact match.
func MatchRefPattern(pattern, ref string) bool {
	parts := strings.Split(pattern, "*")
	switch len(parts) {
	case 1:
		return pattern == ref
	case 2:
		prefix, suffix := parts[0], parts[1]
		// Strict inequality: '*' must consume at least one character, so
		// "refs/heads/*" does not degenerate onto its own prefix.
		return len(ref) > len(prefix)+len(suffix) &&
			strings.HasPrefix(ref, prefix) &&
			strings.HasSuffix(ref, suffix)
	default:
		return matchMultiStar(parts, ref)
	}
}

// matchMultiStar matches ref against pattern segments joined by '*'.
// Each middle segment must occur, in order, after the previous match;
// the last segment must terminate ref.
func matchMultiStar(parts []string, ref string) bool {
	first, last := parts[0], parts[len(parts)-1]
	middle := parts[1 : len(parts)-1]
	if len(ref) <= len(first)+len(last) ||
		!strings.HasPrefix(ref, first) ||
		!strings.HasSuffix(ref, last) {
		return false
	}
	pos := len(first)
	end := len(ref) - len(last)
	for _, seg := range middle {
		idx := strings.Index(ref[pos:end], seg)
		if idx == -1 {
			return false
		}
		pos += idx + len(seg)
	}
	return true
}
