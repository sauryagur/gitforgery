// Package plan builds a read-only rewrite preview (DESIGN.md §4, §5.1).
//
// Build opens a repository through go-git, resolves the recipe's rev-list
// range, matches recipe rules against every commit in topological order
// (parents first, mirroring fast-export stream order) and computes the new
// object id each commit would get after a metadata-only rewrite. Nothing is
// written anywhere; trees and blobs are never touched, only commit objects
// are re-encoded in memory.
package plan

import (
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// ChangeKind classifies what a rewrite does to a single commit.
type ChangeKind int

const (
	// KindUnchanged means the rewritten commit hashes identically: no field
	// changed and no ancestor was rewritten.
	KindUnchanged ChangeKind = iota
	// KindCascade means only the parent links changed (an ancestor was
	// rewritten), so the hash changes although no rule touched this commit.
	KindCascade
	// KindModified means a rule changed metadata of the commit itself.
	KindModified
)

// Field-change labels reported by CommitPlan.Changes.
const (
	ChangeAuthor    = "author"
	ChangeCommitter = "committer"
	ChangeAuthDate  = "author-date"
	ChangeCommDate  = "committer-date"
	ChangeMessage   = "message"
	ChangeSignature = "signature"
)

// CommitPlan is the preview of one commit after applying the recipe.
type CommitPlan struct {
	Old plumbing.Hash
	New plumbing.Hash
	// Kind classifies the change (see above).
	Kind ChangeKind
	// Changes names the fields altered by matched rules, e.g. "author".
	Changes []string
	// MatchedRule is the index of the first matching rule, or -1.
	MatchedRule int

	// OrigAuthor/OrigCommitter are the source identities.
	OrigAuthor    object.Signature
	OrigCommitter object.Signature
	// Author/Committer are the resolved post-rewrite identities.
	Author    object.Signature
	Committer object.Signature
	// Message is the resolved post-rewrite message.
	Message string
}

// Plan is the full preview of applying a recipe to a repository range.
type Plan struct {
	RepoPath  string
	RangeDesc string
	// RangeSpec is the raw range expression that was resolved
	// ("" when every selected ref is included).
	RangeSpec string
	// SelectedRefs lists the refs chosen by the recipe's ref patterns,
	// sorted by name; fast-export runs against these.
	SelectedRefs []string
	// Commits holds every commit in the planned range, oldest first
	// (topological processing order).
	Commits []*CommitPlan
	// AffectedRefs lists refs whose tips land on rewritten commits,
	// sorted by name.
	AffectedRefs []string
}

// Counts summarizes the plan.
type Counts struct {
	Total    int
	Modified int
	Cascade  int
	Changed  int // Modified + Cascade: commits whose id moves
}

// Counts classifies p.Commits.
func (p *Plan) Counts() Counts {
	c := Counts{Total: len(p.Commits)}
	for _, cp := range p.Commits {
		switch cp.Kind {
		case KindModified:
			c.Modified++
		case KindCascade:
			c.Cascade++
		}
	}
	c.Changed = c.Modified + c.Cascade
	return c
}

// sameWhen reports whether two signatures carry byte-identical timestamps:
// same unix second AND same zone offset, which is exactly what the commit
// serializer writes. Two instants with different offsets therefore count as
// different, matching preview-hash behaviour.
func sameWhen(a, b time.Time) bool {
	return a.Unix() == b.Unix() && a.Format("-0700") == b.Format("-0700")
}
