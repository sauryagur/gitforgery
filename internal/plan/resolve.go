package plan

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sauryagur/gitforgery/internal/gitx"
	"github.com/sauryagur/gitforgery/internal/recipe"
)

// resolver applies the recipe's rules to every planned commit in
// topological order. It resolves the prescribed identities, dates and
// message (§6 value grammar) and re-encodes the commit to obtain the new
// object id; trees are never touched.
type resolver struct {
	rec      *recipe.Recipe
	plans    map[plumbing.Hash]*CommitPlan
	branches map[plumbing.Hash][]string

	// now is frozen once per Build so DateNow resolves identically for
	// every commit in one run.
	now time.Time

	// firstAuthorDate/firstCommDate hold the ORIGINAL dates of the first
	// planned commit (base of "first±dur"); prevAuthorDate/prevCommDate
	// hold the REWRITTEN dates of the previously processed commit (base
	// of "prev±dur"). lastCommDate tracks committer-date monotonicity.
	firstAuthorDate *time.Time
	firstCommDate   *time.Time
	prevAuthorDate  *time.Time
	prevCommDate    *time.Time
	lastCommDate    *time.Time
}

func newResolver(rec *recipe.Recipe, branches map[plumbing.Hash][]string) *resolver {
	return &resolver{
		rec:      rec,
		plans:    map[plumbing.Hash]*CommitPlan{},
		branches: branches,
		now:      time.Now(),
	}
}

// commit plans a single commit. Commits must arrive parents-first.
func (rs *resolver) commit(c *object.Commit) error {
	cp := &CommitPlan{
		Old:         c.Hash,
		MatchedRule: -1,
	}

	// Parents map to their planned replacement when the parent is inside
	// the range; excluded ancestors keep their original id (§5.2.6).
	newParents := make([]plumbing.Hash, len(c.ParentHashes))
	cascaded := false
	for i, p := range c.ParentHashes {
		if pp, ok := rs.plans[p]; ok {
			newParents[i] = pp.New
			cascaded = cascaded || pp.New != p
		} else {
			newParents[i] = p
		}
	}

	subject, body := gitx.SplitMessage(c.Message)
	target := recipe.Target{
		SHA:            c.Hash.String(),
		Author:         c.Author.Name,
		AuthorEmail:    c.Author.Email,
		Committer:      c.Committer.Name,
		CommitterEmail: c.Committer.Email,
		Subject:        subject,
		Branches:       rs.branches[c.Hash],
	}
	for i, rule := range rs.rec.Match {
		if rule.Matches(target) {
			cp.MatchedRule = i
			break
		}
	}
	set := &recipe.Set{}
	if cp.MatchedRule >= 0 {
		set = &rs.rec.Match[cp.MatchedRule].Set
	}

	// The first planned commit anchors "first±dur" expressions; its own
	// originals must be captured before any rewrite applies to it.
	if rs.firstAuthorDate == nil {
		a, cm := c.Author.When, c.Committer.When
		rs.firstAuthorDate, rs.firstCommDate = &a, &cm
	}

	author, committer := c.Author, c.Committer
	message := c.Message

	if cp.MatchedRule >= 0 {
		if id, ok := set.AuthorIdent(); ok {
			author.Name, author.Email = id.Name, id.Email
		}
		switch {
		case set.CommitterInherit():
			committer.Name, committer.Email = author.Name, author.Email
		default:
			if id, ok := set.CommitterIdent(); ok {
				committer.Name, committer.Email = id.Name, id.Email
			}
		}

		adExpr, hasAD := set.AuthorDate()
		cdExpr, hasCD := set.CommitterDate()

		if hasAD {
			w, err := rs.resolveWhen(adExpr, true)
			if err != nil {
				return err
			}
			author.When = w
		}
		switch {
		case hasCD && cdExpr.Kind == recipe.DateAuthor:
			committer.When = author.When
		case hasCD:
			w, err := rs.resolveWhen(cdExpr, false)
			if err != nil {
				return err
			}
			committer.When = w
		}

		msg, err := renderMessage(set, c, subject, body)
		if err != nil {
			return err
		}
		message = msg
	}

	// preserve_date_order clamps committer dates so they stay monotonic
	// across the processed range (§6). Applies to every commit, matched
	// or not, since cascaded commits are rewritten regardless.
	if rs.rec.PreserveDateOrder() &&
		rs.lastCommDate != nil && committer.When.Before(*rs.lastCommDate) {
		committer.When = *rs.lastCommDate
	}
	lc := committer.When
	rs.lastCommDate = &lc
	pa, pc := author.When, committer.When
	rs.prevAuthorDate, rs.prevCommDate = &pa, &pc

	var changes []string
	if author.Name != c.Author.Name || author.Email != c.Author.Email {
		changes = append(changes, ChangeAuthor)
	}
	if !sameWhen(author.When, c.Author.When) {
		changes = append(changes, ChangeAuthDate)
	}
	if committer.Name != c.Committer.Name || committer.Email != c.Committer.Email {
		changes = append(changes, ChangeCommitter)
	}
	if !sameWhen(committer.When, c.Committer.When) {
		changes = append(changes, ChangeCommDate)
	}
	if message != c.Message {
		changes = append(changes, ChangeMessage)
	}

	sig := c.PGPSignature
	rewritten := len(changes) > 0 || cascaded
	if sig != "" && rewritten && rs.rec.StripSignatures() {
		// A signature over the old bytes is meaningless after any edit
		// (§2); keeping it would import a provably invalid signature.
		sig = ""
		changes = append(changes, ChangeSignature)
	}

	cp.Changes = changes
	cp.OrigAuthor, cp.OrigCommitter = c.Author, c.Committer
	cp.Author, cp.Committer = author, committer
	cp.Message, cp.OrigMessage = message, c.Message

	switch {
	case len(changes) == 0 && !cascaded:
		cp.Kind = KindUnchanged
		cp.New = cp.Old
	default:
		h, err := gitx.HashCommit(&object.Commit{
			TreeHash:     c.TreeHash,
			ParentHashes: newParents,
			Author:       author,
			Committer:    committer,
			PGPSignature: sig,
			Message:      message,
		})
		if err != nil {
			return fmt.Errorf("re-encode commit: %w", err)
		}
		cp.New = h
		if len(changes) > 0 {
			cp.Kind = KindModified
		} else {
			cp.Kind = KindCascade
		}
	}
	rs.plans[c.Hash] = cp
	return nil
}

// resolveWhen evaluates a date expression. authorSide selects which date
// stream ("first"/"prev" refer to author dates when resolving an
// author_date expression, committer dates otherwise).
func (rs *resolver) resolveWhen(e recipe.DateExpr, authorSide bool) (time.Time, error) {
	switch e.Kind {
	case recipe.DateLiteral:
		return e.Literal, nil
	case recipe.DateNow:
		return rs.now, nil
	case recipe.DateFirst:
		var t time.Time
		if authorSide {
			t = *rs.firstAuthorDate
		} else {
			t = *rs.firstCommDate
		}
		return t.Add(e.Dur), nil
	case recipe.DatePrev:
		prev := rs.prevCommDate
		if authorSide {
			prev = rs.prevAuthorDate
		}
		if prev == nil {
			return time.Time{}, errors.New(`"prev" date used by the first commit in range: there is no previous commit`)
		}
		return prev.Add(e.Dur), nil
	default:
		return time.Time{}, fmt.Errorf("unhandled date kind %v", e.Kind)
	}
}

// renderMessage executes the rule's subject/body templates over the
// original commit's data and reassembles the full message. Without any
// template the original bytes are kept verbatim.
func renderMessage(set *recipe.Set, c *object.Commit, subject, body string) (string, error) {
	st, hasSubject := set.SubjectTmpl()
	bt, hasBody := set.BodyTmpl()
	if !hasSubject && !hasBody {
		return c.Message, nil
	}
	data := recipe.TmplData{
		Subject:    subject,
		Body:       body,
		Author:     fmt.Sprintf("%s <%s>", c.Author.Name, c.Author.Email),
		Committer:  fmt.Sprintf("%s <%s>", c.Committer.Name, c.Committer.Email),
		AuthorDate: c.Author.When.Format(time.RFC3339),
	}
	newSubject, newBody := subject, body
	if hasSubject {
		s, err := execTmpl(st, data)
		if err != nil {
			return "", fmt.Errorf("subject template: %w", err)
		}
		newSubject = s
	}
	if hasBody {
		b, err := execTmpl(bt, data)
		if err != nil {
			return "", fmt.Errorf("body template: %w", err)
		}
		newBody = b
	}
	return gitx.JoinMessage(newSubject, newBody), nil
}

func execTmpl(t *template.Template, data recipe.TmplData) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
