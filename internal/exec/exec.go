// Package exec wraps the git plumbing commands gitforgery drives during a
// rewrite (DESIGN.md §5): fast-export, fast-import, fsck and friends.
//
// Every wrapper takes an explicit repository path and streams through
// io.Reader/io.Writer so callers can pipeline export -> transform ->
// import without buffering whole streams.
package exec

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// runGit runs "git -C dir args…" and wraps failures with the stderr text,
// which carries git's actionable diagnostics.
func runGit(ctx context.Context, dir string, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// FastExport runs git fast-export in repo, writing the stream to stdout.
// args are appended verbatim (rev-list opts plus fast-export options), so
// callers control exactly which options their git supports.
func FastExport(ctx context.Context, repo string, args []string, stdout io.Writer) error {
	return runGit(ctx, repo, nil, stdout, append([]string{"fast-export"}, args...)...)
}

// FastImport feeds stdin to git fast-import in repo. exportMarks, when
// non-empty, receives the mark -> new-object-id table after the import.
// --done enforces well-formed termination of the stream.
func FastImport(ctx context.Context, repo string, stdin io.Reader, exportMarks string) error {
	args := []string{"fast-import", "--quiet", "--done"}
	if exportMarks != "" {
		args = append(args, "--export-marks="+exportMarks)
	}
	return runGit(ctx, repo, stdin, nil, args...)
}

// Fsck runs git fsck with the given extra arguments ("--full" is the
// caller's responsibility to request, matching §5.2 verification).
func Fsck(ctx context.Context, repo string, args ...string) error {
	return runGit(ctx, repo, nil, nil, append([]string{"fsck"}, args...)...)
}

// RevParse resolves a revision to its full object id.
func RevParse(ctx context.Context, repo, rev string) (string, error) {
	var out bytes.Buffer
	if err := runGit(ctx, repo, nil, &out, "rev-parse", rev); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// CloneBare clones src into a new bare repository at dst. The clone gives
// partial-range imports access to excluded ancestors (§5.2.6).
func CloneBare(ctx context.Context, src, dst string) error {
	return runGit(ctx, "", nil, nil, "clone", "--bare", "--no-hardlinks", src, dst)
}

// InitBare creates a new bare repository at path (reinitializing an
// existing one, like git init). Callers wanting create-only semantics
// must check the path first. It backs --to runs whose range covers every
// selected ref, where no ancestor can be missing from the imported
// stream (§5.2.6).
func InitBare(ctx context.Context, path string) error {
	return runGit(ctx, "", nil, nil, "init", "--bare", "--quiet", path)
}

// SetHead repoints the repository's HEAD symref at ref.
func SetHead(ctx context.Context, repo, ref string) error {
	return runGit(ctx, repo, nil, nil, "symbolic-ref", "HEAD", ref)
}

// IsDirty reports whether the repository has a worktree with uncommitted
// changes (§5.2.4). Bare repositories are never dirty.
func IsDirty(ctx context.Context, repo string) (bool, error) {
	var out bytes.Buffer
	if err := runGit(ctx, repo, nil, &out, "rev-parse", "--is-bare-repository"); err != nil {
		return false, err
	}
	if strings.TrimSpace(out.String()) == "true" {
		return false, nil
	}
	out.Reset()
	if err := runGit(ctx, repo, nil, &out, "status", "--porcelain"); err != nil {
		return false, err
	}
	return strings.TrimSpace(out.String()) != "", nil
}

var (
	signedCommitsOnce  sync.Once
	signedCommitsKnown bool
)

// SupportsSignedCommits reports whether this git's fast-export knows the
// --signed-commits option (§3.2). Git strips signed-commit signatures by
// default; the flag exists to choose explicit non-strip behavior on newer
// versions, so gitforgery probes once and passes the flag only when the
// local git understands it.
func SupportsSignedCommits() bool {
	signedCommitsOnce.Do(func() {
		cmd := exec.Command("git", "fast-export", "-h")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		_ = cmd.Run() // -h always exits non-zero; usage lands on stderr
		signedCommitsKnown = strings.Contains(stderr.String(), "--signed-commits")
	})
	return signedCommitsKnown
}

// ZeroSHA is the all-zero object id; update-ref treats it as "ref must
// not exist". SHA-1 only: apply refuses SHA-256 repositories upstream.
const ZeroSHA = "0000000000000000000000000000000000000000"

// UpdateRef moves one ref with a compare-and-swap on its current value
// and a recorded message (§5.2.3). oldVal "" skips the CAS; ZeroSHA
// asserts the ref does not exist yet. Returns the wrapped git error so
// callers can distinguish concurrent-modification failures.
func UpdateRef(ctx context.Context, repo, ref, newVal, oldVal, msg string) error {
	args := []string{"update-ref", "-m", msg, ref, newVal}
	if oldVal != "" {
		args = append(args, oldVal)
	}
	return runGit(ctx, repo, nil, nil, args...)
}

// DeleteRef removes ref.
func DeleteRef(ctx context.Context, repo, ref string) error {
	return runGit(ctx, repo, nil, nil, "update-ref", "-d", ref)
}

// ListRefs returns every existing ref under prefix (a directory such as
// "refs/heads/"), sorted by name.
func ListRefs(ctx context.Context, repo, prefix string) ([]string, error) {
	var out bytes.Buffer
	err := runGit(ctx, repo, nil, &out,
		"for-each-ref", "--format=%(refname)", "--sort=refname", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, line)
		}
	}
	return refs, nil
}

// GitDir reports the repository's absolute .git directory, where the
// transaction log is recorded.
func GitDir(ctx context.Context, repo string) (string, error) {
	var out bytes.Buffer
	if err := runGit(ctx, repo, nil, &out, "rev-parse", "--absolute-git-dir"); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// HashObject stores content as an object of the given type and returns
// its hex id. Used to synthesize rewritten annotated tag objects without
// creating refs (§5.2.3 keeps ref movement under update-ref).
func HashObject(ctx context.Context, repo, objType, content string) (string, error) {
	var out bytes.Buffer
	if err := runGit(ctx, repo, strings.NewReader(content), &out,
		"hash-object", "-t", objType, "-w", "--stdin"); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}
