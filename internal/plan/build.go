package plan

import (
	"container/heap"
	"errors"
	"fmt"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sauryagur/gitforgery/internal/gitx"
	"github.com/sauryagur/gitforgery/internal/recipe"
)

// Build computes the read-only rewrite preview for repoPath under rec.
// rangeOverride, when non-empty, replaces the recipe's own range expression.
func Build(repoPath string, rec *recipe.Recipe, rangeOverride string) (*Plan, error) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		if strings.Contains(err.Error(), "objectformat") {
			// go-git cannot read SHA-256 repositories (§12); fail with
			// the reason instead of a config-extension internals error.
			return nil, fmt.Errorf("open repository %q: SHA-256 object format is not supported yet", repoPath)
		}
		return nil, fmt.Errorf("open repository %q: %w", repoPath, err)
	}
	if err := refuseSHA256(repo); err != nil {
		return nil, err
	}

	selected, err := selectRefs(repo, rec)
	if err != nil {
		return nil, err
	}

	spec := rangeOverride
	descSuffix := ""
	if spec == "" {
		spec = rec.Range
	} else {
		descSuffix = " (override)"
	}

	includeTips, excludeTips, incRev, excRev, desc, err := resolveRange(repo, selected, spec)
	if err != nil {
		return nil, err
	}
	planned, err := collectRange(repo, includeTips, excludeTips)
	if err != nil {
		return nil, err
	}
	ordered, err := topoOrder(planned)
	if err != nil {
		return nil, err
	}

	var branches map[plumbing.Hash][]string
	if usesBranchSelector(rec) {
		branches = branchMap(selected, planned)
	}
	rs := newResolver(rec, branches)

	for _, c := range ordered {
		if err := rs.commit(c); err != nil {
			return nil, fmt.Errorf("plan %s: %w", c.Hash.String()[:12], err)
		}
	}

	p := &Plan{
		RepoPath:     repoPath,
		RangeDesc:    desc + descSuffix,
		RangeSpec:    spec,
		SelectedRefs: refNames(selected),
		IncludeRevs:  incRev,
		ExcludeRevs:  excRev,
	}
	for _, c := range ordered {
		p.Commits = append(p.Commits, rs.plans[c.Hash])
	}
	p.AffectedRefs = affectedRefs(selected, rs.plans)
	return p, nil
}

// refuseSHA256 rejects repositories whose object format go-git cannot plan
// for in default builds (DESIGN.md §12.1): preview hashes would silently use
// the wrong algorithm.
func refuseSHA256(repo *git.Repository) error {
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("read repository config: %w", err)
	}
	format := objectFormat(cfg)
	switch strings.ToLower(format) {
	case "", "sha1":
		return nil
	default:
		return fmt.Errorf("repository uses %s object format; planning SHA-256 repositories is not supported yet (go-git limitation)", format)
	}
}

// objectFormat extracts extensions.objectFormat from the raw config,
// tolerating gcfg's case handling.
func objectFormat(cfg *config.Config) string {
	sec := cfg.Raw.Section("extensions")
	if v := sec.Option("objectformat"); v != "" {
		return v
	}
	return sec.Option("objectFormat")
}

// refTip is a selected ref and the commit its target peels to.
type refTip struct {
	Name string
	Hash plumbing.Hash // commit hash after peeling tags
}

var errNotACommit = errors.New("ref does not point at a commit")

// selectRefs returns every ref matching the recipe's ref patterns, with tag
// objects peeled down to their commits.
func selectRefs(repo *git.Repository, rec *recipe.Recipe) ([]refTip, error) {
	iter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	var out []refTip
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		name := ref.Name().String()
		for _, pat := range rec.Refs() {
			if !gitx.MatchRefPattern(pat, name) {
				continue
			}
			c, err := peelToCommit(repo, ref.Hash())
			if errors.Is(err, errNotACommit) {
				return nil // e.g. a tag on a blob: nothing to plan
			}
			if err != nil {
				return err
			}
			out = append(out, refTip{Name: name, Hash: c.Hash})
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect refs: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// peelToCommit follows tag objects until it reaches a commit.
func peelToCommit(repo *git.Repository, h plumbing.Hash) (*object.Commit, error) {
	for range 32 {
		obj, err := repo.Object(plumbing.AnyObject, h)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", h, err)
		}
		switch t := obj.(type) {
		case *object.Commit:
			return t, nil
		case *object.Tag:
			h = t.Target
		default:
			return nil, errNotACommit
		}
	}
	return nil, fmt.Errorf("tag chain at %s too deep", h)
}

// resolveRange splits a rev-list style expression into include/exclude
// tips plus the equivalent rev-list arguments (include/exclude revision
// expressions, "" normalized to "HEAD"; fast-export only labels commit
// blocks for named revisions, never raw hashes).
//
// Supported forms (§6):
//
//	"" | "all"   everything reachable from the recipe's selected refs
//	"X"          everything reachable from revision X
//	"A..B"       reachable from B but not from A; an omitted side means HEAD
func resolveRange(repo *git.Repository, selected []refTip, spec string) (include, exclude []plumbing.Hash, incRev, excRev []string, desc string, err error) {
	switch spec {
	case "", "all":
		seen := map[plumbing.Hash]struct{}{}
		for _, tip := range selected {
			if _, dup := seen[tip.Hash]; !dup {
				seen[tip.Hash] = struct{}{}
				include = append(include, tip.Hash)
				incRev = append(incRev, tip.Name)
			}
		}
		names := make([]string, len(selected))
		for i, tip := range selected {
			names[i] = tip.Name
		}
		return include, nil, incRev, nil, "all reachable from [" + strings.Join(names, ", ") + "]", nil
	}

	if before, after, found := strings.Cut(spec, ".."); found {
		if strings.Contains(after, "..") {
			return nil, nil, nil, nil, "", fmt.Errorf("range %q: only one '..' is supported", spec)
		}
		excl, err := resolveOne(repo, before)
		if err != nil {
			return nil, nil, nil, nil, "", fmt.Errorf("range %q: %v", spec, err)
		}
		incl, err := resolveOne(repo, after)
		if err != nil {
			return nil, nil, nil, nil, "", fmt.Errorf("range %q: %v", spec, err)
		}
		return []plumbing.Hash{*incl}, []plumbing.Hash{*excl},
			[]string{orHead(after)}, []string{orHead(before)}, spec, nil
	}

	tip, err := resolveOne(repo, spec)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("range %q: %v", spec, err)
	}
	return []plumbing.Hash{*tip}, nil, []string{orHead(spec)}, nil, spec, nil
}

// orHead normalizes an empty range side to HEAD (§6).
func orHead(rev string) string {
	if rev == "" {
		return "HEAD"
	}
	return rev
}

// resolveOne resolves a single revision to its peeled commit. An empty
// revision means HEAD.
func resolveOne(repo *git.Repository, rev string) (*plumbing.Hash, error) {
	if rev == "" {
		rev = "HEAD"
	}
	h, err := repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil || h == nil {
		return nil, fmt.Errorf("cannot resolve %q: %v", rev, err)
	}
	c, err := peelToCommit(repo, *h)
	if err != nil {
		return nil, err
	}
	hash := c.Hash
	return &hash, nil
}

// collectRange gathers every commit reachable from include that is not
// reachable from exclude. Every commit reachable from an excluded tip —
// the tip itself included — becomes a barrier for the include walk.
func collectRange(repo *git.Repository, include, exclude []plumbing.Hash) (map[plumbing.Hash]*object.Commit, error) {
	blocked := map[plumbing.Hash]struct{}{}
	if err := walkClosure(repo, exclude, nil, func(h plumbing.Hash, _ *object.Commit) {
		blocked[h] = struct{}{}
	}); err != nil {
		return nil, err
	}
	out := map[plumbing.Hash]*object.Commit{}
	if err := walkClosure(repo, include, blocked, func(h plumbing.Hash, c *object.Commit) {
		out[h] = c
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// walkClosure DFS-walks commit history from roots. Commits in blocked
// (when non-nil) are skipped with their ancestors; visit is called once
// per remaining commit.
func walkClosure(repo *git.Repository, roots []plumbing.Hash, blocked map[plumbing.Hash]struct{}, visit func(plumbing.Hash, *object.Commit)) error {
	stack := append([]plumbing.Hash(nil), roots...)
	seen := map[plumbing.Hash]struct{}{}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		if blocked != nil {
			if _, ok := blocked[h]; ok {
				continue
			}
		}
		c, err := repo.CommitObject(h)
		if err != nil {
			return fmt.Errorf("read commit %s: %w", h, err)
		}
		if visit != nil {
			visit(h, c)
		}
		stack = append(stack, c.ParentHashes...)
	}
	return nil
}

// topoOrder orders planned commits parents-first (the order fast-export and
// fast-import stream commits). Among ready commits, newest committer date
// wins, then ascending oid — deterministic and close to rev-list date order.
func topoOrder(planned map[plumbing.Hash]*object.Commit) ([]*object.Commit, error) {
	remaining := make(map[plumbing.Hash]int, len(planned)) // unprocessed in-range parents
	children := make(map[plumbing.Hash][]plumbing.Hash, len(planned))
	for h, c := range planned {
		n := 0
		for _, p := range c.ParentHashes {
			if _, inRange := planned[p]; !inRange {
				continue // excluded ancestor: not a constraint
			}
			children[p] = append(children[p], h)
			n++
		}
		remaining[h] = n
	}

	ready := &readyHeap{}
	for h, n := range remaining {
		if n == 0 {
			heap.Push(ready, planned[h])
		}
	}

	out := make([]*object.Commit, 0, len(planned))
	for ready.Len() > 0 {
		c := heap.Pop(ready).(*object.Commit)
		out = append(out, c)
		for _, child := range children[c.Hash] {
			remaining[child]--
			if remaining[child] == 0 {
				heap.Push(ready, planned[child])
			}
		}
	}
	if len(out) != len(planned) {
		return nil, errors.New("commit graph contains a cycle outside git's guarantees")
	}
	return out, nil
}

type readyHeap []*object.Commit

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	if !h[i].Committer.When.Equal(h[j].Committer.When) {
		return h[i].Committer.When.After(h[j].Committer.When)
	}
	return h[i].Hash.String() < h[j].Hash.String()
}
func (h readyHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x interface{}) { *h = append(*h, x.(*object.Commit)) }
func (h *readyHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// usesBranchSelector reports whether any rule selects on branches, so the
// branch reachability map is only built when needed.
func usesBranchSelector(rec *recipe.Recipe) bool {
	for _, r := range rec.Match {
		if r.When.Branch != "" {
			return true
		}
	}
	return false
}

// branchMap records, per planned commit, every selected ref whose history
// contains it.
func branchMap(selected []refTip, planned map[plumbing.Hash]*object.Commit) map[plumbing.Hash][]string {
	reach := make(map[plumbing.Hash][]string, len(planned))
	for _, tip := range selected {
		stack := []plumbing.Hash{tip.Hash}
		seen := map[plumbing.Hash]struct{}{}
		for len(stack) > 0 {
			h := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			c, ok := planned[h]
			if !ok {
				continue // outside planned range
			}
			reach[h] = append(reach[h], tip.Name)
			stack = append(stack, c.ParentHashes...)
		}
	}
	return reach
}

// affectedRefs lists selected refs whose tip would move under the plan.
func affectedRefs(selected []refTip, plans map[plumbing.Hash]*CommitPlan) []string {
	var out []string
	for _, tip := range selected {
		if cp, ok := plans[tip.Hash]; ok && cp.New != cp.Old {
			out = append(out, tip.Name)
		}
	}
	sort.Strings(out)
	return out
}

// refNames extracts the names of selected refs in selection order.
func refNames(selected []refTip) []string {
	out := make([]string, len(selected))
	for i, tip := range selected {
		out[i] = tip.Name
	}
	return out
}
