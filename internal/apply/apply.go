// Package apply executes a forgery plan: it exports the planned history,
// streams it through the recipe transform into a target repository and
// verifies the result before any reference is allowed to move
// (DESIGN.md §4 apply, §5.1 data flow, §5.2 safety invariants).
package apply

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sauryagur/gitforgery/internal/exec"
	"github.com/sauryagur/gitforgery/internal/plan"
	"github.com/sauryagur/gitforgery/internal/recipe"
	"github.com/sauryagur/gitforgery/internal/stream"
)

// ErrConfirmRequired reports that a mutating run lacks consent (§4:
// every mutating command requires --yes or a confirming keystroke).
var ErrConfirmRequired = errors.New("refusing to modify history without --yes")

// signedTagModes are the --signed-tags modes git fast-export understands;
// anything else would fail deep inside export instead of up front.
var signedTagModes = map[string]bool{
	"abort": true, "strip": true, "verbatim": true, "warn": true, "warn-strip": true,
}

// Options configures one apply run.
type Options struct {
	// RepoPath is the source repository being forged.
	RepoPath string
	// Recipe is the validated recipe to execute.
	Recipe *recipe.Recipe
	// To is the destination repository path (--to). The default output
	// of apply is always a NEW repository; rewriting the source in place
	// is a separate, explicitly opted-in code path (§5.2.1).
	To string
	// Yes records user consent for the mutation.
	Yes bool
	// SignedTags selects how signed tags are exported (default "strip").
	SignedTags string
	// RangeOverride replaces the recipe's own range when non-empty.
	RangeOverride string
}

// Report summarizes a completed run.
type Report struct {
	Source string
	Target string
	Range  string
	Counts plan.Counts
	// OldToNew maps every planned commit's original id to its imported id.
	OldToNew map[string]string
}

// Run performs the export -> transform -> import -> verify pipeline.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Recipe == nil {
		return nil, errors.New("apply: no recipe given")
	}
	if opts.To == "" {
		return nil, errors.New(`apply: destination required (use --to out.git; --in-place needs explicit opt-in)`)
	}
	mode := signedMode(opts.SignedTags)
	if !signedTagModes[mode] {
		return nil, fmt.Errorf("apply: unsupported --signed-tags mode %q (want abort|strip|verbatim|warn|warn-strip)", mode)
	}

	p, err := plan.Build(opts.RepoPath, opts.Recipe, opts.RangeOverride)
	if err != nil {
		return nil, err
	}
	if len(p.SelectedRefs) == 0 {
		return nil, fmt.Errorf("apply: no refs match the recipe's ref patterns in %q", opts.RepoPath)
	}
	if !opts.Yes {
		return nil, fmt.Errorf("%w; re-run with --yes after reviewing 'gitforgery plan' output", ErrConfirmRequired)
	}

	if _, err := os.Stat(opts.To); err == nil {
		return nil, fmt.Errorf("apply: destination %q already exists", opts.To)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("apply: stat destination: %w", err)
	}
	if err := exec.InitBare(ctx, opts.To); err != nil {
		return nil, fmt.Errorf("apply: init target: %w", err)
	}

	exportMarks, importMarks, err := tempMarks()
	if err != nil {
		return nil, err
	}
	defer os.Remove(exportMarks)
	defer os.Remove(importMarks)

	rw, err := pipeline(ctx, opts, p, exportMarks, importMarks)
	if err != nil {
		return nil, err
	}

	if err := exec.Fsck(ctx, opts.To, "--full"); err != nil {
		return nil, fmt.Errorf("apply: post-import fsck failed: %w", err)
	}

	imported, err := os.ReadFile(importMarks)
	if err != nil {
		return nil, fmt.Errorf("apply: read import marks: %w", err)
	}
	newByMark, err := stream.ParseMarksTable(imported)
	if err != nil {
		return nil, fmt.Errorf("apply: parse import marks: %w", err)
	}
	oldToNew := joinMappings(rw.Marks(), newByMark)

	if err := Canary(p, oldToNew); err != nil {
		return nil, fmt.Errorf("apply: preview canary failed (stream vocabulary drift?): %w", err)
	}
	if err := TreeOIDsEqual(opts.RepoPath, opts.To, p); err != nil {
		return nil, fmt.Errorf("apply: metadata-only verification failed: %w", err)
	}

	// A freshly initialized target has an unborn default HEAD; point it
	// at the first rewritten branch so the result inspects sanely.
	if ref := firstRefName(p.IncludeRevs); ref != "" {
		if err := exec.SetHead(ctx, opts.To, ref); err != nil {
			return nil, fmt.Errorf("apply: set target HEAD: %w", err)
		}
	}

	return &Report{
		Source:   opts.RepoPath,
		Target:   opts.To,
		Range:    p.RangeDesc,
		Counts:   p.Counts(),
		OldToNew: oldToNew,
	}, nil
}

// pipeline wires fast-export -> Rewriter -> fast-import across io.Pipes,
// streaming blobs through without buffering them. It returns the
// rewriter so callers can join mark tables.
func pipeline(ctx context.Context, opts Options, p *plan.Plan, exportMarks, importMarks string) (*stream.Rewriter, error) {
	args := []string{
		"--show-original-ids",
		"--use-done-feature",
		"--signed-tags=" + signedMode(opts.SignedTags),
		"--export-marks=" + exportMarks,
	}
	if opts.Recipe.StripSignatures() && exec.SupportsSignedCommits() {
		args = append(args, "--signed-commits=strip")
	}
	args = append(args, p.IncludeRevs...)
	for _, rev := range p.ExcludeRevs {
		args = append(args, "^"+rev)
	}

	srcToRewrite, srcW := io.Pipe()
	rwToImport, rwW := io.Pipe()
	errs := make([]error, 2)
	done := make(chan struct{}, 2)

	go func() {
		errs[0] = exec.FastExport(ctx, opts.RepoPath, args, srcW)
		srcW.Close()
		done <- struct{}{}
	}()
	rw := stream.NewRewriter(p.Commits)
	go func() {
		errs[1] = rw.Run(srcToRewrite, rwW)
		srcToRewrite.CloseWithError(errs[1])
		rwW.Close()
		done <- struct{}{}
	}()
	importErr := exec.FastImport(ctx, opts.To, rwToImport, importMarks)
	rwToImport.CloseWithError(importErr)
	<-done
	<-done
	if err := errors.Join(errs[0], errs[1], importErr); err != nil {
		return nil, fmt.Errorf("apply: rewrite pipeline: %w", err)
	}
	return rw, nil
}

// tempMarks creates the two scratch mark tables: one fed to fast-export
// (--export-marks), one filled by fast-import (--export-marks on the
// import side yields the mark -> new-oid table).
func tempMarks() (exportPath, importPath string, err error) {
	exportPath, err = newMarkFile("gitforgery-export-marks-")
	if err != nil {
		return "", "", err
	}
	importPath, err = newMarkFile("gitforgery-import-marks-")
	if err != nil {
		os.Remove(exportPath)
		return "", "", err
	}
	return exportPath, importPath, nil
}

func newMarkFile(pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("apply: create marks file: %w", err)
	}
	path := f.Name()
	f.Close()
	return path, nil
}

func signedMode(flag string) string {
	if flag == "" {
		return "strip"
	}
	return flag
}

// firstRefName returns the first argument that looks like a full refname.
func firstRefName(revs []string) string {
	for _, r := range revs {
		if strings.HasPrefix(r, "refs/") {
			return r
		}
	}
	return ""
}

// joinMappings combines the stream's mark->original-id table with
// fast-import's mark->new-id table into an old->new object map.
func joinMappings(markToOld, markToNew map[string]string) map[string]string {
	out := make(map[string]string, len(markToOld))
	for mark, old := range markToOld {
		if neu, ok := markToNew[mark]; ok {
			out[old] = neu
		}
	}
	return out
}
