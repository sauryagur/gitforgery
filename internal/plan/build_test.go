package plan_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sauryagur/gitforgery/internal/plan"
	"github.com/sauryagur/gitforgery/internal/recipe"
	"github.com/sauryagur/gitforgery/internal/testrepo"
)

const (
	c1Msg = "Initial commit\n"
	c2Msg = "Import from oldcorp\n\nCame from the migration tool.\n"
	c3Msg = "Third commit\n"
)

func mustParse(t *testing.T, y string) *recipe.Recipe {
	t.Helper()
	rec, err := recipe.Parse([]byte(y))
	if err != nil {
		t.Fatalf("recipe.Parse: %v", err)
	}
	return rec
}

// hashCommitRaw asks real git for the object id of a raw commit, pinning
// the preview encoder to git's own serialization.
func hashCommitRaw(t *testing.T, dir, raw string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "hash-object", "-t", "commit", "--stdin")
	cmd.Stdin = strings.NewReader(raw)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("hash-object: %v: %s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String())
}

// identLine renders one "verb Name <email> unix zone" commit header line.
func identLine(verb, name, email string, when time.Time) string {
	return fmt.Sprintf("%s %s <%s> %d %s", verb, name, email, when.Unix(), when.Format("-0700"))
}

// baseFixture is a three-commit linear history on main:
//
//	c1 (Ada, 2020 UTC) <- c2 (Bob @oldcorp, 2021 +0200) <- c3 (Cara, 2022 -0700)
type baseFixture struct {
	r        *testrepo.Repo
	shas     map[string]string // label -> sha
	trees    map[string]string // label -> tree sha
	c1T, c2T time.Time         // author==committer dates of c1/c2
	c3T      time.Time
}

func newBaseFixture(t *testing.T) *baseFixture {
	t.Helper()
	f := &baseFixture{
		r:     testrepo.New(t),
		shas:  map[string]string{},
		trees: map[string]string{},
		c1T:   time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC),
		c2T:   time.Date(2021, 6, 15, 12, 30, 0, 0, time.FixedZone("", 2*3600)),
		c3T:   time.Date(2022, 11, 5, 3, 16, 0, 0, time.FixedZone("", -7*3600)),
	}
	f.shas["c1"] = f.r.Commit(testrepo.Commit{Label: "c1", Message: c1Msg,
		Name: "Ada Lovelace", Email: "ada@example.com",
		Date: "2020-01-01T10:00:00+00:00", Files: map[string]string{"a.txt": "one\n"}})
	f.shas["c2"] = f.r.Commit(testrepo.Commit{Label: "c2", Message: c2Msg,
		Name: "Bob", Email: "bob@oldcorp.com",
		Date: "2021-06-15T12:30:00+02:00", Files: map[string]string{"b.txt": "two\n"}})
	f.shas["c3"] = f.r.Commit(testrepo.Commit{Label: "c3", Message: c3Msg,
		Name: "Cara", Email: "cara@example.net",
		Date: "2022-11-05T03:16:00-07:00", Files: map[string]string{"c.txt": "three\n"}})
	for _, l := range []string{"c1", "c2", "c3"} {
		f.trees[l] = f.r.Git("rev-parse", f.shas[l]+"^{tree}")
	}
	f.r.Branch("main", "c3")
	return f
}

func (f *baseFixture) plan(t *testing.T, recYAML string) *plan.Plan {
	t.Helper()
	p, err := plan.Build(f.r.Dir, mustParse(t, recYAML), "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

const recOldcorp = `
match:
  - when:
      author_email: "@oldcorp\\.com$"
    set:
      author: "Ada Lovelace <ada@example.com>"
      committer: "inherit"
      author_date: "2019-03-04T08:00:00Z"
      committer_date: "author"
`

// TestBuildRewritesAuthorAndCascades is the golden test for a metadata-only
// rewrite: the new ids come straight from `git hash-object` over the exact
// expected commit bytes.
func TestBuildRewritesAuthorAndCascades(t *testing.T) {
	f := newBaseFixture(t)
	ad := time.Date(2019, 3, 4, 8, 0, 0, 0, time.UTC)

	wantC2 := hashCommitRaw(t, f.r.Dir, fmt.Sprintf(
		"tree %s\nparent %s\n%s\n%s\n\n%s",
		f.trees["c2"], f.shas["c1"],
		identLine("author", "Ada Lovelace", "ada@example.com", ad),
		identLine("committer", "Ada Lovelace", "ada@example.com", ad),
		c2Msg))
	wantC3 := hashCommitRaw(t, f.r.Dir, fmt.Sprintf(
		"tree %s\nparent %s\n%s\n%s\n\n%s",
		f.trees["c3"], wantC2,
		identLine("author", "Cara", "cara@example.net", f.c3T),
		identLine("committer", "Cara", "cara@example.net", f.c3T),
		c3Msg))

	p := f.plan(t, recOldcorp)

	counts := p.Counts()
	if counts != (plan.Counts{Total: 3, Modified: 1, Cascade: 1, Changed: 2}) {
		t.Errorf("Counts = %+v, want {3 1 1 2}", counts)
	}
	if p.RangeSpec != "" || len(p.SelectedRefs) != 1 || p.SelectedRefs[0] != "refs/heads/main" {
		t.Errorf("RangeSpec/SelectedRefs = %q/%v", p.RangeSpec, p.SelectedRefs)
	}
	if got := p.AffectedRefs; len(got) != 1 || got[0] != "refs/heads/main" {
		t.Fatalf("AffectedRefs = %v, want [refs/heads/main]", got)
	}

	byOld := map[string]*plan.CommitPlan{}
	for _, cp := range p.Commits {
		byOld[cp.Old.String()] = cp
	}

	c2p := byOld[f.shas["c2"]]
	if c2p.Kind != plan.KindModified || c2p.MatchedRule != 0 {
		t.Errorf("c2 Kind/MatchedRule = %v/%d, want modified/0", c2p.Kind, c2p.MatchedRule)
	}
	if want := []string{"author", "author-date", "committer", "committer-date"}; !equalStrings(c2p.Changes, want) {
		t.Errorf("c2 Changes = %v, want %v", c2p.Changes, want)
	}
	if c2p.New.String() != wantC2 {
		t.Errorf("c2 New = %s, want golden %s", c2p.New, wantC2)
	}
	if c2p.Author.Name != "Ada Lovelace" || c2p.Author.Email != "ada@example.com" {
		t.Errorf("c2 Author = %+v", c2p.Author)
	}
	if !c2p.Committer.When.Equal(ad) || !c2p.Author.When.Equal(ad) {
		t.Errorf("c2 dates = %v/%v, both want %v", c2p.Author.When, c2p.Committer.When, ad)
	}
	if c2p.Message != c2Msg {
		t.Errorf("c2 Message changed without template: %q", c2p.Message)
	}

	c3p := byOld[f.shas["c3"]]
	if c3p.Kind != plan.KindCascade || len(c3p.Changes) != 0 || c3p.MatchedRule != -1 {
		t.Errorf("c3 kind/changes/rule = %v/%v/%d, want cascade/{}/-1", c3p.Kind, c3p.Changes, c3p.MatchedRule)
	}
	if c3p.New.String() != wantC3 {
		t.Errorf("c3 New = %s, want golden %s", c3p.New, wantC3)
	}

	c1p := byOld[f.shas["c1"]]
	if c1p.Kind != plan.KindUnchanged || c1p.New != c1p.Old {
		t.Errorf("c1 = %+v, want unchanged", c1p)
	}
}

func TestBuildNoMatchLeavesRepoUntouched(t *testing.T) {
	f := newBaseFixture(t)
	before := f.r.Checksum()
	p := f.plan(t, `
match:
  - when:
      author_email: '@nomatch\.org$'
    set:
      author: "X <x@x.org>"
`)

	c := p.Counts()
	if c.Total != 3 || c.Changed != 0 {
		t.Errorf("Counts = %+v, want total 3 changed 0", c)
	}
	for _, cp := range p.Commits {
		if cp.Kind != plan.KindUnchanged || cp.New != cp.Old {
			t.Errorf("%s: %+v, want unchanged", cp.Old, cp)
		}
	}
	if len(p.AffectedRefs) != 0 {
		t.Errorf("AffectedRefs = %v, want none", p.AffectedRefs)
	}
	if after := f.r.Checksum(); after != before {
		t.Error("Build modified the repository")
	}
}

func TestBuildPartialRangeKeepsExcludedParent(t *testing.T) {
	f := newBaseFixture(t)
	rec := mustParse(t, recOldcorp)
	p, err := plan.Build(f.r.Dir, rec, f.shas["c1"]+"..main")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Commits) != 2 {
		t.Fatalf("planned %d commits, want 2", len(p.Commits))
	}
	if p.Commits[0].Old.String() != f.shas["c2"] {
		t.Fatalf("first planned commit = %s, want c2", p.Commits[0].Old)
	}

	// c2 is rewritten but its excluded parent keeps the original id.
	ad := time.Date(2019, 3, 4, 8, 0, 0, 0, time.UTC)
	wantC2 := hashCommitRaw(t, f.r.Dir, fmt.Sprintf(
		"tree %s\nparent %s\n%s\n%s\n\n%s",
		f.trees["c2"], f.shas["c1"],
		identLine("author", "Ada Lovelace", "ada@example.com", ad),
		identLine("committer", "Ada Lovelace", "ada@example.com", ad),
		c2Msg))
	if p.Commits[0].New.String() != wantC2 {
		t.Errorf("c2 New = %s, want golden %s", p.Commits[0].New, wantC2)
	}
	if p.RangeDesc == "" {
		t.Error("RangeDesc empty")
	}
}

func TestBuildPrevRelativeDates(t *testing.T) {
	f := newBaseFixture(t)
	p := f.plan(t, `
match:
  - when:
      subject: "^Import"
    set:
      committer_date: "prev+2h"
`)
	var c2p *plan.CommitPlan
	for _, cp := range p.Commits {
		if cp.Old.String() == f.shas["c2"] {
			c2p = cp
		}
	}
	if c2p == nil {
		t.Fatal("c2 not planned")
	}
	want := f.c1T.Add(2 * time.Hour) // prev = first commit's rewritten (=original) committer date
	if !c2p.Committer.When.Equal(want) {
		t.Errorf("c2 committer date = %v, want %v", c2p.Committer.When, want)
	}
}

func TestBuildPrevOnFirstCommitErrors(t *testing.T) {
	f := newBaseFixture(t)
	_, err := plan.Build(f.r.Dir, mustParse(t, `
match:
  - when: {}
    set:
      committer_date: "prev+1h"
`), "")
	if err == nil || !strings.Contains(err.Error(), "prev") {
		t.Errorf("err = %v, want prev-not-available failure", err)
	}
}

func TestBuildPreserveDateOrderClamp(t *testing.T) {
	f := newBaseFixture(t)
	const rule = `
options:
  preserve_date_order: %t
match:
  - when:
      author_email: "@oldcorp\\.com$"
    set:
      committer_date: "2019-01-01T00:00:00Z"
`

	p := f.plan(t, fmt.Sprintf(rule, true))
	var clamped *plan.CommitPlan
	for _, cp := range p.Commits {
		if cp.Old.String() == f.shas["c2"] {
			clamped = cp
		}
	}
	if !clamped.Committer.When.Equal(f.c1T) {
		t.Errorf("preserve_date_order: c2 committer date = %v, want clamp to %v", clamped.Committer.When, f.c1T)
	}

	p = f.plan(t, fmt.Sprintf(rule, false))
	for _, cp := range p.Commits {
		if cp.Old.String() == f.shas["c2"] {
			clamped = cp
		}
	}
	if want := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC); !clamped.Committer.When.Equal(want) {
		t.Errorf("no clamp: c2 committer date = %v, want literal %v", clamped.Committer.When, want)
	}
}

func TestBuildSubjectTemplate(t *testing.T) {
	f := newBaseFixture(t)
	p := f.plan(t, `
match:
  - when:
      subject: "^Import"
    set:
      subject: "[imported] {{.Subject}}"
`)
	var c2p *plan.CommitPlan
	for _, cp := range p.Commits {
		if cp.Old.String() == f.shas["c2"] {
			c2p = cp
		}
	}
	want := "[imported] Import from oldcorp\n\nCame from the migration tool.\n"
	if c2p.Message != want {
		t.Errorf("Message = %q, want %q", c2p.Message, want)
	}
	if !hasChange(c2p.Changes, plan.ChangeMessage) {
		t.Errorf("Changes = %v, want message change", c2p.Changes)
	}
}

// signedRoot returns the raw bytes of an unsigned-parent root commit that
// carries a fake gpgsig header.
func signedRoot(tree string) string {
	return "tree " + tree + "\n" +
		"author Sig Neo <sig@example.io> 1500000000 +0000\n" +
		"committer Sig Neo <sig@example.io> 1500000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" \n" +
		" iQEcBAABCgAGBQJfake=\n" +
		" =abcd\n" +
		" -----END PGP SIGNATURE-----\n" +
		"\n" +
		"Signed root\n"
}

func TestBuildStripsSignatureWhenRewriting(t *testing.T) {
	f := newBaseFixture(t)
	sigSha := f.r.HashObject("commit", signedRoot(f.trees["c1"]))
	f.r.SetRef("refs/heads/sig", sigSha)

	p := f.plan(t, `
match:
  - when:
      author: "^Sig"
    set:
      author: "New Name <new@example.io>"
`)

	var cp *plan.CommitPlan
	for _, c := range p.Commits {
		if c.Old.String() == sigSha {
			cp = c
		}
	}
	if cp == nil {
		t.Fatal("signed commit not planned")
	}
	if !hasChange(cp.Changes, plan.ChangeSignature) || !hasChange(cp.Changes, plan.ChangeAuthor) {
		t.Fatalf("Changes = %v, want author + signature", cp.Changes)
	}
	raw := strings.Replace(signedRoot(f.trees["c1"]),
		identLine("author", "Sig Neo", "sig@example.io", time.Unix(1500000000, 0).UTC()),
		identLine("author", "New Name", "new@example.io", time.Unix(1500000000, 0).UTC()), 1)
	want := hashCommitRaw(t, f.r.Dir, stripGpgsig(raw))
	if cp.New.String() != want {
		t.Errorf("New = %s, want stripped golden %s", cp.New, want)
	}
}

func TestBuildKeepsSignatureWhenReSignRequested(t *testing.T) {
	f := newBaseFixture(t)
	sigSha := f.r.HashObject("commit", signedRoot(f.trees["c1"]))
	f.r.SetRef("refs/heads/sig", sigSha)

	p := f.plan(t, `
options:
  re_sign: true
  strip_signatures: false
match:
  - when:
      author: "^Sig"
    set:
      author: "New Name <new@example.io>"
`)

	var cp *plan.CommitPlan
	for _, c := range p.Commits {
		if c.Old.String() == sigSha {
			cp = c
		}
	}
	if cp == nil {
		t.Fatal("signed commit not planned")
	}
	if hasChange(cp.Changes, plan.ChangeSignature) {
		t.Errorf("Changes = %v, signature must be kept for re-signing", cp.Changes)
	}
	want := hashCommitRaw(t, f.r.Dir,
		strings.Replace(signedRoot(f.trees["c1"]),
			identLine("author", "Sig Neo", "sig@example.io", time.Unix(1500000000, 0).UTC()),
			identLine("author", "New Name", "new@example.io", time.Unix(1500000000, 0).UTC()), 1))
	if cp.New.String() != want {
		t.Errorf("New = %s, want signed golden %s", cp.New, want)
	}
}

// stripGpgsig removes the gpgsig header (all its continuation lines) from
// raw commit bytes.
func stripGpgsig(raw string) string {
	var out []string
	inSig := false
	for _, line := range strings.SplitAfter(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "gpgsig "):
			inSig = true
		case inSig && strings.HasPrefix(line, " "):
		case inSig && line == "\n":
			out = append(out, line)
			inSig = false
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "")
}

func TestBuildBranchSelector(t *testing.T) {
	f := newBaseFixture(t)
	cbSha := f.r.Commit(testrepo.Commit{Label: "cb", Message: "Feature work\n",
		Name: "Dave", Email: "dave@example.org",
		Date:  "2023-02-02T08:00:00+00:00",
		Files: map[string]string{"feat.txt": "f\n"}, Parents: []string{"c1"}})
	f.r.Branch("feature/auth", "cb")

	p := f.plan(t, `
match:
  - when:
      branch: "^refs/heads/feature"
    set:
      author: "Eve <eve@example.io>"
`)
	var cbp, c1p *plan.CommitPlan
	for _, cp := range p.Commits {
		switch cp.Old.String() {
		case cbSha:
			cbp = cp
		case f.shas["c1"]:
			c1p = cp
		case f.shas["c2"], f.shas["c3"]:
			// The rule rewrote shared root c1 (it is reachable from
			// feature/auth too), so the main line cascades.
			if cp.Kind != plan.KindCascade || len(cp.Changes) != 0 ||
				!cp.Author.When.Equal(cp.OrigAuthor.When) {
				t.Errorf("main-line commit %s = %+v, want pure cascade", cp.Old, cp)
			}
		}
	}
	if cbp == nil || cbp.Kind != plan.KindModified || cbp.Author.Name != "Eve" {
		t.Fatalf("branch commit not rewritten to Eve: %+v", cbp)
	}
	if c1p == nil || c1p.Kind != plan.KindModified {
		t.Errorf("shared root c1 not modified: %+v", c1p)
	}
}

func TestBuildEmptyRepoYieldsEmptyPlan(t *testing.T) {
	r := testrepo.New(t)
	p, err := plan.Build(r.Dir, mustParse(t, recOldcorp), "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Commits) != 0 || len(p.SelectedRefs) != 0 {
		t.Errorf("Plan = %+v, want empty", p)
	}
}

func TestBuildRefusesSHA256(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(out.String())
	}
	run("init", "-q", "-b", "main", "--object-format=sha256")
	if err := writeFileIn(dir, "a.txt", "one\n"); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	tree := run("write-tree")
	run("-c", "user.name=x", "-c", "user.email=x@x.io",
		"commit-tree", tree, "-m", "root")

	if _, err := plan.Build(dir, mustParse(t, ""), ""); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("err = %v, want SHA-256 refusal", err)
	}
}

func writeFileIn(dir, path, content string) error {
	return os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644)
}

func hasChange(changes []string, want string) bool {
	for _, c := range changes {
		if c == want {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
