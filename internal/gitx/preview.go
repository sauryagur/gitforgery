package gitx

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// HashCommit computes the object hash git assigns to c when the commit is
// serialized, without touching any repository. It re-encodes the commit with
// go-git's own encoder over an in-memory object, so the result matches both
// `git hash-object -t commit --stdin` on the same bytes and what `git
// fast-import` writes for identical metadata (both pinned by tests).
//
// The hash algorithm is always the default (SHA-1); callers must refuse
// SHA-256 repositories before using this helper.
func HashCommit(c *object.Commit) (plumbing.Hash, error) {
	obj := &plumbing.MemoryObject{}
	if err := c.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit for preview: %w", err)
	}
	return obj.Hash(), nil
}
