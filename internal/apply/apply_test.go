package apply_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/sauryagur/gitforgery/internal/apply"
	"github.com/sauryagur/gitforgery/internal/plan"
	"github.com/sauryagur/gitforgery/internal/recipe"
	"github.com/sauryagur/gitforgery/internal/testrepo"
)

// h converts a hex sha to a plumbing hash.
func h(hex string) plumbing.Hash { return plumbing.NewHash(hex) }

const rewriteAll = `match:
  - when:
      author_email: "@x\\.io$"
    set:
      author: "Ada Lovelace <ada@example.com>"
      committer: "inherit"
      author_date: "2020-06-01T10:00:00+00:00"
      committer_date: "author"
`

func mustRecipe(t *testing.T, data string) *recipe.Recipe {
	t.Helper()
	rec, err := recipe.Parse([]byte(data))
	if err != nil {
		t.Fatalf("recipe.Parse: %v", err)
	}
	return rec
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, repo, err, out)
	}
	return strings.TrimSpace(string(out))
}

// twoCommitFixture builds the standard source: two authors, two dates,
// one branch.
func twoCommitFixture(t *testing.T) (*testrepo.Repo, string, string) {
	t.Helper()
	src := testrepo.New(t)
	c1 := src.Commit(testrepo.Commit{Label: "c1", Message: "first subject\n",
		Name: "Old Author", Email: "old@x.io", Date: "2020-01-01T10:00:00+00:00",
		Files: map[string]string{"a.txt": "alpha\n"}})
	c2 := src.Commit(testrepo.Commit{Label: "c2", Message: "second subject\n",
		Name: "Bob Builder", Email: "bob@x.io", Date: "2021-02-02T11:00:00+00:00",
		Files: map[string]string{"b.txt": "beta\n"}})
	src.Branch("main", "c2")
	return src, c1, c2
}

func TestApplyToNewRepoEndToEnd(t *testing.T) {
	src, _, c2 := twoCommitFixture(t)
	target := filepath.Join(t.TempDir(), "out.git")

	rep, err := apply.Run(context.Background(), apply.Options{
		RepoPath: src.Dir,
		Recipe:   mustRecipe(t, rewriteAll),
		To:       target,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("apply.Run: %v", err)
	}

	if rep.Counts.Total != 2 || rep.Counts.Modified != 2 {
		t.Errorf("counts = %+v, want {Total:2 Modified:2}", rep.Counts)
	}
	newTip := gitOut(t, target, "rev-parse", "refs/heads/main")
	if newTip == c2 {
		t.Error("target tip equals source tip; history was not rewritten")
	}
	if rep.OldToNew[c2] != newTip {
		t.Errorf("report mapping %s disagrees with imported ref %s", rep.OldToNew[c2], newTip)
	}

	// Identities/dates rewritten per recipe on every commit.
	log := gitOut(t, target, "log", "--format=%an|%ae|%aI")
	want := "Ada Lovelace|ada@example.com|2020-06-01T10:00:00Z\nAda Lovelace|ada@example.com|2020-06-01T10:00:00Z"
	if log != want {
		t.Errorf("rewritten log =\n%s\nwant\n%s", log, want)
	}
	// Committer mirrors author ("inherit").
	if cm := gitOut(t, target, "log", "--format=%cn|%ce"); strings.Contains(cm, "Old") || strings.Contains(cm, "Bob") {
		t.Errorf("committer not inherited: %s", cm)
	}

	// Trees are byte-identical to the source (§5.2.5).
	srcTrees := strings.Fields(gitOut(t, src.Dir, "log", "--format=%T"))
	tgtTrees := strings.Fields(gitOut(t, target, "log", "--format=%T"))
	if len(srcTrees) != len(tgtTrees) {
		t.Fatalf("commit count drifted: %d vs %d", len(srcTrees), len(tgtTrees))
	}
	for i := range srcTrees {
		if srcTrees[i] != tgtTrees[i] {
			t.Errorf("tree %d changed: %s vs %s", i, srcTrees[i], tgtTrees[i])
		}
	}
	// External fsck confirmation on top of the in-engine check.
	if out, err := exec.Command("git", "-C", target, "fsck", "--full").CombinedOutput(); err != nil {
		t.Errorf("external fsck: %v\n%s", err, out)
	}

	// Source repository untouched by a --to run.
	if got := gitOut(t, src.Dir, "rev-parse", "refs/heads/main"); got != c2 {
		t.Errorf("source moved: %s", got)
	}
}

func TestApplyOptionValidation(t *testing.T) {
	src, _, _ := twoCommitFixture(t)
	existing := t.TempDir()

	tests := []struct {
		name    string
		opts    apply.Options
		wantErr error // non-nil: require errors.Is match
		errIn   string
	}{
		{
			name:    "missing consent",
			opts:    apply.Options{RepoPath: src.Dir, Recipe: mustRecipe(t, rewriteAll), To: filepath.Join(t.TempDir(), "out.git")},
			wantErr: apply.ErrConfirmRequired,
		},
		{
			name:  "missing destination",
			opts:  apply.Options{RepoPath: src.Dir, Recipe: mustRecipe(t, rewriteAll), Yes: true},
			errIn: "--to",
		},
		{
			name:  "existing destination refused",
			opts:  apply.Options{RepoPath: src.Dir, Recipe: mustRecipe(t, rewriteAll), To: existing, Yes: true},
			errIn: "already exists",
		},
		{
			name:  "bad signed-tags mode",
			opts:  apply.Options{RepoPath: src.Dir, Recipe: mustRecipe(t, rewriteAll), To: filepath.Join(t.TempDir(), "out.git"), Yes: true, SignedTags: "explode"},
			errIn: "signed-tags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := apply.Run(context.Background(), tt.opts)
			if err == nil {
				t.Fatal("Run succeeded, want error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.errIn != "" && !strings.Contains(err.Error(), tt.errIn) {
				t.Errorf("error = %q, want substring %q", err, tt.errIn)
			}
		})
	}
}

func TestNoopRecipeLeavesTipsIdentical(t *testing.T) {
	src, _, c2 := twoCommitFixture(t)
	target := filepath.Join(t.TempDir(), "out.git")

	noOp := `
match:
  - when:
      author_email: "@nobody\\.example$"
    set:
      author: "X <x@y.z>"
`
	rep, err := apply.Run(context.Background(), apply.Options{
		RepoPath: src.Dir,
		Recipe:   mustRecipe(t, noOp),
		To:       target,
		Yes:      true,
	})
	if err != nil {
		t.Fatalf("apply.Run: %v", err)
	}
	if rep.Counts.Changed != 0 {
		t.Errorf("counts = %+v, want zero changes", rep.Counts)
	}
	if got := gitOut(t, target, "rev-parse", "refs/heads/main"); got != c2 {
		t.Errorf("no-op run changed tip: %s want %s", got, c2)
	}
	if rep.OldToNew[c2] != c2 {
		t.Errorf("no-op mapping = %s, want identity %s", rep.OldToNew[c2], c2)
	}
}

func TestCanaryVerifiesImportedReality(t *testing.T) {
	old1, new1 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	old2, new2 := "cccccccccccccccccccccccccccccccccccccccc", "dddddddddddddddddddddddddddddddddddddddd"
	p := &plan.Plan{Commits: []*plan.CommitPlan{
		{Old: h(old1), New: h(new1)},
		{Old: h(old2), New: h(new2)},
	}}

	tests := []struct {
		name    string
		mapping map[string]string
		wantErr bool
		errIn   string
	}{
		{name: "matching import passes", mapping: map[string]string{old1: new1, old2: new2}},
		{
			name:    "missing import fails",
			mapping: map[string]string{old1: new1},
			wantErr: true, errIn: "never imported",
		},
		{
			name:    "hash mismatch fails",
			mapping: map[string]string{old1: new1, old2: old2},
			wantErr: true, errIn: "preview promised",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := apply.Canary(p, tt.mapping)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Canary error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.errIn != "" && !strings.Contains(err.Error(), tt.errIn) {
				t.Errorf("error = %q, want substring %q", err, tt.errIn)
			}
		})
	}
}
