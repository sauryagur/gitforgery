// Package stream reads and writes the git fast-export/fast-import stream
// vocabulary (DESIGN.md §3.2, §5).
//
// The lexer is incremental: data-block payloads are never buffered by the
// package itself; callers copy them straight from the underlying reader,
// so multi-hundred-megabyte blobs flow through in bounded memory. Every
// untouched byte round-trips exactly.
package stream

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Ident is one "Name <email> unix zone" identity line value (author,
// committer or tagger). When carries both the instant and its original
// zone offset, which the commit serializer preserves byte-for-byte.
type Ident struct {
	Name  string
	Email string
	When  time.Time
}

// ParseIdent parses the value of an identity line, i.e. everything after
// the "author "/"committer "/“tagger ” verb:
//
//	Name <email> 1136134400 +0100
func ParseIdent(s string) (Ident, error) {
	open := strings.LastIndexByte(s, '<')
	close := strings.LastIndexByte(s, '>')
	if open == -1 || close == -1 || close < open {
		return Ident{}, fmt.Errorf("invalid identity %q: want \"Name <email> secs zone\"", s)
	}
	name := strings.TrimSuffix(s[:open], " ")
	email := s[open+1 : close]
	secStr, zone, ok := strings.Cut(strings.TrimSpace(s[close+1:]), " ")
	if !ok {
		return Ident{}, fmt.Errorf("invalid identity %q: missing timestamp or zone", s)
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(secStr), 10, 64)
	if err != nil {
		return Ident{}, fmt.Errorf("invalid identity %q: bad timestamp %q", s, secStr)
	}
	zone2, err := time.Parse("-0700", strings.TrimSpace(zone))
	if err != nil {
		return Ident{}, fmt.Errorf("invalid identity %q: bad zone %q", s, zone)
	}
	_, offset := zone2.Zone()
	loc := time.FixedZone("", offset)
	return Ident{Name: name, Email: email, When: time.Unix(secs, 0).In(loc)}, nil
}

// String renders the identity back into stream form.
func (i Ident) String() string {
	return fmt.Sprintf("%s <%s> %d %s", i.Name, i.Email, i.When.Unix(), i.When.Format("-0700"))
}
