package stream

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/sauryagur/gitforgery/internal/plan"
)

// Rewriter applies a plan to a fast-export stream, emitting the
// transformed stream for fast-import (DESIGN.md §5.1).
//
// Only commit metadata changes: author/committer identity lines and the
// message data block of planned commits. Everything else — blobs, file
// operations, tags, resets, parent links — passes through byte-for-byte.
//
// Parent links need no rewriting: fast-export references in-range commits
// by mark ("from :4") and marks keep their identity across fast-import,
// while out-of-range ancestors are referenced by object id and must not be
// rewritten anyway (§5.2.6).
type Rewriter struct {
	plans map[string]*plan.CommitPlan // original oid hex -> plan
	marks map[string]string           // mark number -> original oid hex
}

// NewRewriter indexes plans by original object id.
func NewRewriter(commits []*plan.CommitPlan) *Rewriter {
	rw := &Rewriter{
		plans: make(map[string]*plan.CommitPlan, len(commits)),
		marks: map[string]string{},
	}
	for _, cp := range commits {
		rw.plans[cp.Old.String()] = cp
	}
	return rw
}

// Marks returns the mark -> original-oid table observed while running.
// Joined with fast-import's --export-marks table it yields the old->new
// object-id mapping used for verification and ref updates.
func (rw *Rewriter) Marks() map[string]string {
	return rw.marks
}

// Run reads the stream from r and writes the transformed stream to w.
func (rw *Rewriter) Run(r io.Reader, w io.Writer) error {
	sc := NewScanner(r)
	ew := NewWriter(w)
	curVerb := ""            // verb of the open block
	pendingMark := ""        // mark seen in the current block
	var cur *plan.CommitPlan // plan of the current commit block

	for {
		tok, err := sc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("scan stream: %w", err)
		}
		switch tok.Kind {
		case KindCommand:
			curVerb = tok.Verb
			pendingMark = ""
			if tok.Verb != "commit" {
				cur = nil
			}
			err = ew.Raw(tok.Raw)

		case KindHeader:
			switch {
			case tok.Verb == "mark":
				pendingMark = trimMark(tok.Arg)
				err = ew.Raw(tok.Raw)
			case tok.Verb == "original-oid":
				if pendingMark != "" {
					rw.marks[pendingMark] = tok.Arg
				}
				if curVerb == "commit" {
					cur = rw.plans[tok.Arg]
				}
				err = ew.Raw(tok.Raw)
			case curVerb == "commit" && cur != nil &&
				(tok.Verb == "author" || tok.Verb == "committer"):
				err = rw.writeIdent(ew, tok, cur)
			default:
				err = ew.Raw(tok.Raw)
			}

		case KindData:
			if curVerb == "commit" && cur != nil && cur.Message != cur.OrigMessage {
				err = Drain(tok)
				if err == nil {
					err = ew.Data(cur.Message)
				}
			} else {
				err = ew.CopyData(tok)
			}

		default:
			err = ew.Raw(tok.Raw)
		}
		if err != nil {
			return fmt.Errorf("transform %s line: %w", tok.Verb, err)
		}
	}
	return ew.Flush()
}

// writeIdent replaces an author/committer line with the planned identity,
// or writes the original line back when the plan leaves that field alone.
func (rw *Rewriter) writeIdent(ew *Writer, tok Token, cp *plan.CommitPlan) error {
	sig := cp.Author
	orig := cp.OrigAuthor
	if tok.Verb == "committer" {
		sig = cp.Committer
		orig = cp.OrigCommitter
	}
	// Rewrite whenever the emitted line would differ: identity OR date.
	// A recipe may change only author-date/committer-date, in which case
	// neither field label appears in Changes — comparing against the
	// original identity is what makes those rewrites effective.
	if sig.Name == orig.Name && sig.Email == orig.Email &&
		plan.SameWhen(sig.When, orig.When) {
		return ew.Raw(tok.Raw)
	}
	id := Ident{Name: sig.Name, Email: sig.Email, When: sig.When}
	return ew.Raw(tok.Verb + " " + id.String())
}

// trimMark strips the leading colon from a mark argument ("12" from ":12").
func trimMark(arg string) string {
	if len(arg) > 0 && arg[0] == ':' {
		return arg[1:]
	}
	return arg
}

// ParseMarksTable parses a git fast-export/fast-import marks file
// (":<mark> <oid>" lines) into a mark -> oid table.
func ParseMarksTable(data []byte) (map[string]string, error) {
	out := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		mark, oid, ok := cutMark(sc.Text())
		if !ok {
			return nil, fmt.Errorf("marks table line %d: want \":<mark> <oid>\", got %q", lineNo, sc.Text())
		}
		out[mark] = oid
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read marks table: %w", err)
	}
	return out, nil
}

// cutMark splits ":<mark> <oid>".
func cutMark(line string) (mark, oid string, ok bool) {
	rest, found := strings.CutPrefix(line, ":")
	if !found {
		return "", "", false
	}
	mark, oid, found = strings.Cut(rest, " ")
	if !found || mark == "" || oid == "" {
		return "", "", false
	}
	if _, err := strconv.ParseUint(mark, 10, 64); err != nil {
		return "", "", false
	}
	return mark, oid, true
}
