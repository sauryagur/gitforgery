package apply

import (
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/sauryagur/gitforgery/internal/plan"
)

// Canary checks the imported reality against the preview (§5.1): every
// planned commit must have been imported, and fast-import's object id
// must equal the hash computed from plan data alone. A mismatch means the
// stream vocabulary drifted from the preview serializer — the run aborts
// before any ref is touched.
func Canary(p *plan.Plan, oldToNew map[string]string) error {
	for _, cp := range p.Commits {
		got, ok := oldToNew[cp.Old.String()]
		if !ok {
			return fmt.Errorf("commit %s was planned but never imported", short(cp.Old))
		}
		if got != cp.New.String() {
			return fmt.Errorf("commit %s: imported as %s, preview promised %s",
				short(cp.Old), short(plumbing.NewHash(got)), short(cp.New))
		}
	}
	return nil
}

// TreeOIDsEqual enforces §5.2.5 for metadata-only recipes: every rewritten
// commit in target must carry exactly the tree of its source commit —
// content is untouched, only identities/dates/messages move.
func TreeOIDsEqual(sourcePath, targetPath string, p *plan.Plan) error {
	src, err := git.PlainOpen(sourcePath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	tgt, err := git.PlainOpen(targetPath)
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	for _, cp := range p.Commits {
		if cp.Kind == plan.KindUnchanged {
			continue
		}
		sc, err := src.CommitObject(cp.Old)
		if err != nil {
			return fmt.Errorf("read source commit %s: %w", short(cp.Old), err)
		}
		tc, err := tgt.CommitObject(cp.New)
		if err != nil {
			return fmt.Errorf("read imported commit %s: %w", short(cp.New), err)
		}
		if sc.TreeHash != tc.TreeHash {
			return fmt.Errorf("commit %s: tree changed from %s to %s during metadata-only rewrite",
				short(cp.Old), short(sc.TreeHash), short(tc.TreeHash))
		}
	}
	return nil
}

func short(h plumbing.Hash) string {
	s := h.String()
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}
