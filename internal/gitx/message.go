package gitx

import "strings"

// SplitMessage splits a raw commit message into its subject and body
// regions, the surfaces exposed to recipe templates (§6).
//
// The subject is the first line of the message. The body is everything
// after the first blank line; a message without a blank line has no body.
// JoinMessage(SplitMessage(m)) reproduces every message except one that
// consists of a subject plus a lone trailing newline, which normalizes to
// the bare subject.
func SplitMessage(m string) (subject, body string) {
	if i := strings.IndexByte(m, '\n'); i >= 0 {
		subject = m[:i]
		if rest := m[i+1:]; strings.HasPrefix(rest, "\n") {
			body = rest[1:]
		}
		return subject, body
	}
	return m, ""
}

// JoinMessage assembles a message from its subject and body regions,
// the inverse of SplitMessage. An empty body yields just the subject.
func JoinMessage(subject, body string) string {
	if body == "" {
		return subject
	}
	return subject + "\n\n" + body
}
