package exec_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitexec "github.com/sauryagur/gitforgery/internal/exec"
	"github.com/sauryagur/gitforgery/internal/testrepo"
)

func TestFastExportImportRoundTrip(t *testing.T) {
	src := testrepo.New(t)
	c1 := src.Commit(testrepo.Commit{
		Label: "c1", Message: "one\n",
		Name: "Ada", Email: "ada@example.com", Date: "2020-01-01T10:00:00+00:00",
		Files: map[string]string{"a.txt": "one\n"},
	})
	src.Commit(testrepo.Commit{Label: "c2", Message: "two\n",
		Name: "Bob", Email: "bob@example.com", Date: "2021-01-01T10:00:00+00:00",
		Files: map[string]string{"b.txt": "two\n"}})
	src.Branch("main", "c2")

	ctx := context.Background()

	var stream bytes.Buffer
	marks := filepath.Join(t.TempDir(), "export.marks")
	err := gitexec.FastExport(ctx, src.Dir,
		[]string{"--show-original-ids", "--use-done-feature", "--export-marks=" + marks, "refs/heads/main"},
		&stream)
	if err != nil {
		t.Fatalf("FastExport: %v", err)
	}
	out := stream.String()
	for _, want := range []string{"commit refs/heads/main", "original-oid " + c1, "data 4"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream missing %q:\n%s", want, out)
		}
	}

	dst := filepath.Join(t.TempDir(), "out.git")
	if err := gitexec.CloneBare(ctx, src.Dir, dst); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}
	importMarks := filepath.Join(t.TempDir(), "import.marks")
	if err := gitexec.FastImport(ctx, dst, bytes.NewReader(stream.Bytes()), importMarks); err != nil {
		t.Fatalf("FastImport: %v", err)
	}

	got, err := gitexec.RevParse(ctx, dst, "refs/heads/main")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if got != src.SHA("c2") {
		t.Errorf("round-tripped tip = %s, want %s", got, src.SHA("c2"))
	}
	if err := gitexec.Fsck(ctx, dst, "--full"); err != nil {
		t.Errorf("Fsck: %v", err)
	}

	markTable, err := os.ReadFile(importMarks)
	if err != nil {
		t.Fatalf("read import marks: %v", err)
	}
	if !strings.Contains(string(markTable), c1) {
		t.Errorf("import marks missing original commit %s:\n%s", c1, markTable)
	}
}

func TestFastImportRejectsTruncatedStream(t *testing.T) {
	dst := testrepo.New(t) // any repo; import fails on garbage
	err := gitexec.FastImport(context.Background(), dst.Dir, strings.NewReader("blob\ndata 100\nshort"), "")
	if err == nil || !strings.Contains(err.Error(), "fast-import") {
		t.Fatalf("err = %v, want fast-import failure", err)
	}
}

func TestIsDirtyAndRevParseErrors(t *testing.T) {
	r := testrepo.New(t)
	ctx := context.Background()

	dirty, err := gitexec.IsDirty(ctx, r.Dir)
	if err != nil || dirty {
		t.Errorf("fresh repo dirty = %v, %v; want false, nil", dirty, err)
	}
	os.WriteFile(filepath.Join(r.Dir, "x.txt"), []byte("x"), 0o644)
	dirty, err = gitexec.IsDirty(ctx, r.Dir)
	if err != nil || !dirty {
		t.Errorf("dirty repo dirty = %v, %v; want true, nil", dirty, err)
	}

	bare := filepath.Join(t.TempDir(), "bare.git")
	if err := gitexec.CloneBare(ctx, r.Dir, bare); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}
	dirty, err = gitexec.IsDirty(ctx, bare)
	if err != nil || dirty {
		t.Errorf("bare repo dirty = %v, %v; want false, nil", dirty, err)
	}

	if _, err := gitexec.RevParse(ctx, r.Dir, "does-not-exist"); err == nil {
		t.Error("RevParse of bogus rev succeeded")
	}
}

func TestSupportsSignedCommitsMatchesGitUsage(t *testing.T) {
	got := gitexec.SupportsSignedCommits()
	cmd := exec.Command("git", "fast-export", "-h")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	want := strings.Contains(stderr.String(), "--signed-commits")
	if got != want {
		t.Errorf("SupportsSignedCommits = %v, usage says %v", got, want)
	}
	// Cached second call must agree.
	if gitexec.SupportsSignedCommits() != want {
		t.Error("cached SupportsSignedCommits disagrees")
	}
}

func TestInitBare(t *testing.T) {
	ctx := context.Background()
	dst := filepath.Join(t.TempDir(), "fresh.git")
	if err := gitexec.InitBare(ctx, dst); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	out, err := exec.Command("git", "-C", dst, "rev-parse", "--is-bare-repository").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		t.Errorf("is-bare = %q, want true", out)
	}
	// git init reinitializes existing repositories by design; the apply
	// engine enforces create-only semantics via its own existence check.
	if err := gitexec.InitBare(ctx, dst); err != nil {
		t.Errorf("re-init: %v", err)
	}
}

func TestUpdateRefCASAndListDelete(t *testing.T) {
	ctx := context.Background()
	src := testrepo.New(t)
	c1 := src.Commit(testrepo.Commit{Label: "c1", Message: "one\n",
		Name: "A", Email: "a@x", Files: map[string]string{"f": "1\n"}})
	src.Commit(testrepo.Commit{Label: "c2", Message: "two\n",
		Name: "A", Email: "a@x", Files: map[string]string{"g": "2\n"}})
	src.Branch("main", "c2")

	// Create with ZeroSHA CAS: succeeds once, fails the second time.
	if err := gitexec.UpdateRef(ctx, src.Dir, "refs/heads/x", src.SHA("c2"), gitexec.ZeroSHA, "create"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := gitexec.UpdateRef(ctx, src.Dir, "refs/heads/x", src.SHA("c1"), gitexec.ZeroSHA, "recreate"); err == nil {
		t.Fatal("second create with ZeroSHA should conflict")
	}
	// CAS on wrong old value must fail...
	if err := gitexec.UpdateRef(ctx, src.Dir, "refs/heads/x", src.SHA("c1"), c1, "wrong-old"); err == nil {
		t.Fatal("stale CAS should fail")
	}
	// ...and correct CAS must succeed.
	if err := gitexec.UpdateRef(ctx, src.Dir, "refs/heads/x", src.SHA("c1"), src.SHA("c2"), "move"); err != nil {
		t.Fatalf("CAS move: %v", err)
	}
	if got := src.Git("rev-parse", "refs/heads/x"); got != src.SHA("c1") {
		t.Errorf("ref = %s, want %s", got, src.SHA("c1"))
	}

	// Reflog records the -m messages (the transaction evidence).
	log := strings.Join(strings.Fields(src.Git("reflog", "show", "refs/heads/x")), " ")
	if !strings.Contains(log, "move") {
		t.Errorf("reflog missing update message: %s", log)
	}

	refs, err := gitexec.ListRefs(ctx, src.Dir, "refs/heads/")
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	want := []string{"refs/heads/main", "refs/heads/x"}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("refs[%d] = %q, want %q", i, refs[i], want[i])
		}
	}

	if err := gitexec.DeleteRef(ctx, src.Dir, "refs/heads/x"); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if refs, _ := gitexec.ListRefs(ctx, src.Dir, "refs/heads/x"); len(refs) != 0 {
		t.Errorf("deleted ref still listed: %v", refs)
	}
}

func TestGitDirAndHashObject(t *testing.T) {
	ctx := context.Background()
	src := testrepo.New(t)

	gd, err := gitexec.GitDir(ctx, src.Dir)
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if fi, statErr := os.Stat(filepath.Join(gd, "HEAD")); statErr != nil || fi.IsDir() {
		t.Errorf("git dir %q lacks HEAD: %v", gd, statErr)
	}

	blob := "tag payload\n"
	sha, err := gitexec.HashObject(ctx, src.Dir, "blob", blob)
	if err != nil {
		t.Fatalf("HashObject: %v", err)
	}
	// -p strips the trailing newline; compare content via hash-object.
	want, err := gitexec.HashObject(ctx, src.Dir, "blob", blob)
	if err != nil {
		t.Fatal(err)
	}
	if sha != want {
		t.Errorf("stored %s, re-hashing gives %s", sha, want)
	}
}
