// Package recipe parses and validates gitforgery forgery recipes (DESIGN.md §6).
//
// A recipe is declarative and side-effect free: it selects commits by rule
// ("when") and prescribes metadata replacements ("set"). Parse performs strict
// decoding plus full static validation, so consumers can rely on every accessor
// returning well-formed data. Evaluation against concrete commits lives in
// internal/plan.
package recipe

import (
	"regexp"
	"text/template"
	"time"

	"github.com/sauryagur/gitforgery/internal/gitx"
)

// DateKind enumerates the value grammar of date expressions (§6):
// literals, "author", "now", "first±dur", "prev±dur".
type DateKind int

const (
	// DateNone marks an absent date expression.
	DateNone DateKind = iota
	// DateLiteral is an absolute RFC3339 timestamp.
	DateLiteral
	// DateAuthor mirrors this commit's rewritten author date.
	// Only valid for committer_date.
	DateAuthor
	// DateNow resolves at run time.
	DateNow
	// DateFirst offsets the first commit's original date in the planned
	// range: the author date when resolving author_date, the committer
	// date when resolving committer_date.
	DateFirst
	// DatePrev offsets the previous processed commit's rewritten date:
	// author date for author_date expressions, committer date for
	// committer_date expressions.
	DatePrev
)

// DateExpr is a parsed date value expression.
type DateExpr struct {
	Kind    DateKind
	Literal time.Time     // valid iff Kind == DateLiteral
	Dur     time.Duration // offset applied by DateFirst/DatePrev
}

// String renders the expression back into recipe syntax (used in errors).
func (e DateExpr) String() string {
	switch e.Kind {
	case DateLiteral:
		return e.Literal.Format(time.RFC3339)
	case DateAuthor:
		return "author"
	case DateNow:
		return "now"
	case DateFirst:
		return "first" + durString(e.Dur)
	case DatePrev:
		return "prev" + durString(e.Dur)
	default:
		return ""
	}
}

func durString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	if d > 0 {
		return "+" + d.String()
	}
	return "-" + (-d).String()
}

// TmplData is the template context available to subject/body templates (§6).
type TmplData struct {
	Subject    string
	Body       string
	Author     string // original author as "Name <email>"
	Committer  string // original committer as "Name <email>"
	AuthorDate string // original author date, RFC3339
}

// When groups the when-selectors of a rule (§6). Every field is a regular
// expression over the corresponding commit attribute; an absent (empty)
// field matches everything. All present fields must match.
type When struct {
	SHA            string `yaml:"sha"`
	Author         string `yaml:"author"`
	AuthorEmail    string `yaml:"author_email"`
	Committer      string `yaml:"committer"`
	CommitterEmail string `yaml:"committer_email"`
	Subject        string `yaml:"subject"`
	Branch         string `yaml:"branch"`
}

// Rule is one ordered match/set entry of a recipe. The first rule whose
// selectors all match a commit applies; later rules are skipped.
type Rule struct {
	When When // absent selectors match everything
	Set  Set  // prescribed replacements

	when whenRegexps
}

// Target is the commit surface visible to when-selectors.
type Target struct {
	SHA            string   // full hex object id of the source commit
	Author         string   // author name
	AuthorEmail    string   // author email
	Committer      string   // committer name
	CommitterEmail string   // committer email
	Subject        string   // message subject region
	Branches       []string // refs whose history contains the commit
}

type whenRegexps struct {
	sha, author, authorEmail, committer, committerEmail, subject, branch *regexp.Regexp
}

// Matches reports whether every configured selector matches t.
func (r *Rule) Matches(t Target) bool {
	w := r.when
	if w.sha != nil && !w.sha.MatchString(t.SHA) {
		return false
	}
	if w.author != nil && !w.author.MatchString(t.Author) {
		return false
	}
	if w.authorEmail != nil && !w.authorEmail.MatchString(t.AuthorEmail) {
		return false
	}
	if w.committer != nil && !w.committer.MatchString(t.Committer) {
		return false
	}
	if w.committerEmail != nil && !w.committerEmail.MatchString(t.CommitterEmail) {
		return false
	}
	if w.subject != nil && !w.subject.MatchString(t.Subject) {
		return false
	}
	if w.branch != nil {
		matched := false
		for _, b := range t.Branches {
			if w.branch.MatchString(b) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Set holds the replacements a matched rule applies. Accessors report whether
// the corresponding field was configured.
type Set struct {
	author        gitx.Identity
	committer     gitx.Identity
	committerInh  bool
	authorDate    DateExpr
	committerDate DateExpr
	subjectTmpl   *template.Template
	bodyTmpl      *template.Template
}

// AuthorIdent returns the literal replacement identity for the author.
func (s *Set) AuthorIdent() (gitx.Identity, bool) { return s.author, !isZero(s.author) }

// CommitterInherit reports whether the committer mirrors the rewritten author.
func (s *Set) CommitterInherit() bool { return s.committerInh }

// CommitterIdent returns the literal replacement identity for the committer.
func (s *Set) CommitterIdent() (gitx.Identity, bool) { return s.committer, !isZero(s.committer) }

// AuthorDate returns the configured author-date expression.
func (s *Set) AuthorDate() (DateExpr, bool) { return s.authorDate, s.authorDate.Kind != DateNone }

// CommitterDate returns the configured committer-date expression.
func (s *Set) CommitterDate() (DateExpr, bool) {
	return s.committerDate, s.committerDate.Kind != DateNone
}

// SubjectTmpl returns the compiled subject template.
func (s *Set) SubjectTmpl() (*template.Template, bool) { return s.subjectTmpl, s.subjectTmpl != nil }

// BodyTmpl returns the compiled body template.
func (s *Set) BodyTmpl() (*template.Template, bool) { return s.bodyTmpl, s.bodyTmpl != nil }

func isZero(i gitx.Identity) bool { return i.Name == "" && i.Email == "" }

// Recipe is a validated forgery recipe.
type Recipe struct {
	Range string // raw range expression; "" and "all" mean all reachable commits
	Match []*Rule

	stripSignatures   bool
	reSign            bool
	preserveDateOrder bool
	refs              []string
	backupRef         string
}

// StripSignatures reports whether signatures must be dropped during rewrite.
// Signatures are always invalid after any edit, so this is forced on unless
// re-signing was requested and stripping was explicitly declined.
func (r *Recipe) StripSignatures() bool {
	if !r.reSign {
		return true
	}
	return r.stripSignatures
}

// ReSign reports whether the user asked for post-apply re-signing.
func (r *Recipe) ReSign() bool { return r.reSign }

// PreserveDateOrder reports whether committer dates should be clamped to stay
// monotonic across the rewritten range.
func (r *Recipe) PreserveDateOrder() bool { return r.preserveDateOrder }

// Refs returns the ref glob patterns the recipe operates on.
func (r *Recipe) Refs() []string { return r.refs }

// BackupRef returns the namespace under which pre-rewrite tips are stored.
func (r *Recipe) BackupRef() string { return r.backupRef }

// DefaultRefs is the default set of ref globs a recipe operates on (§6).
var DefaultRefs = []string{"refs/heads/*", "refs/tags/*", "refs/notes/*"}

// DefaultBackupRef is the default backup-ref namespace (§5.2).
const DefaultBackupRef = "refs/forgery/original"
