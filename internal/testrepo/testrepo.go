// Package testrepo builds tiny deterministic git repositories for tests.
//
// Repos are created with plumbing commands (write-tree, commit-tree,
// update-ref) and fixed identities/dates, so every fixture yields the same
// object ids on any machine and git version. Tests then pin or relate SHAs
// without fear of drift.
package testrepo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Commit describes one commit in a fixture script.
type Commit struct {
	Label   string            // identifier used by SHA/Parents; must be unique
	Message string            // full raw message (newlines preserved verbatim)
	Name    string            // author AND committer display name
	Email   string            // author AND committer email
	Date    string            // RFC3339; sets both GIT_AUTHOR_DATE and GIT_COMMITTER_DATE
	Files   map[string]string // path → content, written before the commit
	Parents []string          // labels of parent commits; empty = previous commit
}

// Repo is a fixture repository under construction.
type Repo struct {
	t    *testing.T
	Dir  string
	shas map[string]string
	last string
}

// New creates an empty repository (branch "main") in a fresh temp dir.
func New(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	r := &Repo{t: t, Dir: dir, shas: map[string]string{}}
	r.Git("init", "-q", "-b", "main")
	return r
}

// Git runs a git command in the repo and returns trimmed stdout.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String())
}

// Commit creates a commit object from c and returns its hex sha. With no
// Parents, the previously created commit is used as the single parent.
func (r *Repo) Commit(c Commit) string {
	r.t.Helper()
	if c.Label == "" {
		r.t.Fatal("testrepo.Commit: Label is required")
	}
	for path, content := range c.Files {
		full := filepath.Join(r.Dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			r.t.Fatalf("write %s: %v", path, err)
		}
	}
	r.Git("add", "-A")
	tree := r.Git("write-tree")

	args := []string{"commit-tree", tree}
	parents := c.Parents
	if len(parents) == 0 && r.last != "" {
		parents = []string{"@last"}
	}
	for _, p := range parents {
		sha := p
		switch {
		case sha == "@last":
			sha = r.last
		default:
			if resolved, ok := r.shas[p]; ok {
				sha = resolved // label; fall back to raw object id
			}
		}
		args = append(args, "-p", sha)
	}

	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Stdin = strings.NewReader(c.Message)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_AUTHOR_NAME=%s", identityPart(c.Name, "Ada Lovelace")),
		fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", identityPart(c.Email, "ada@example.com")),
		fmt.Sprintf("GIT_AUTHOR_DATE=%s", identityPart(c.Date, "2005-04-07T22:13:13+00:00")),
		fmt.Sprintf("GIT_COMMITTER_NAME=%s", identityPart(c.Name, "Ada Lovelace")),
		fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", identityPart(c.Email, "ada@example.com")),
		fmt.Sprintf("GIT_COMMITTER_DATE=%s", identityPart(c.Date, "2005-04-07T22:13:13+00:00")),
	)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git commit-tree (%s): %v: %s", c.Label, err, strings.TrimSpace(errOut.String()))
	}
	sha := strings.TrimSpace(out.String())
	r.shas[c.Label] = sha
	r.last = sha
	return sha
}

func identityPart(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Branch points refs/heads/<name> at the commit labeled label.
func (r *Repo) Branch(name, label string) {
	r.Git("update-ref", "refs/heads/"+name, r.SHA(label))
}

// Tag creates a lightweight tag at the commit labeled label.
func (r *Repo) Tag(name, label string) {
	r.Git("update-ref", "refs/tags/"+name, r.SHA(label))
}

// AnnotatedTag creates an annotated tag object at the labeled commit.
func (r *Repo) AnnotatedTag(name, label, message string) {
	r.Git("tag", "-a", name, r.SHA(label), "-m", message)
}

// SHA returns the hex sha of the labeled commit.
func (r *Repo) SHA(label string) string {
	r.t.Helper()
	sha, ok := r.shas[label]
	if !ok {
		r.t.Fatalf("testrepo: unknown commit label %q", label)
	}
	return sha
}

// HashObject writes raw bytes as an object of the given type into the repo's
// ODB and returns its hex sha.
func (r *Repo) HashObject(objType string, content string) string {
	r.t.Helper()
	cmd := exec.Command("git", "-C", r.Dir, "hash-object", "-t", objType, "--stdin", "-w")
	cmd.Stdin = strings.NewReader(content)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git hash-object -w: %v: %s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String())
}

// SetRef points an arbitrary ref at a raw sha.
func (r *Repo) SetRef(name, sha string) {
	r.Git("update-ref", name, sha)
}

// Checksum walks the repository directory and returns a stable digest of all
// file contents, used to assert that plan leaves repos untouched.
func (r *Repo) Checksum() string {
	r.t.Helper()
	var h strings.Builder
	err := filepath.Walk(r.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(r.Dir, path)
		h.WriteString(rel)
		h.WriteByte(0)
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.WriteString(fmt.Sprintf("%x", data))
		h.WriteByte(0)
		return nil
	})
	if err != nil {
		r.t.Fatalf("checksum: %v", err)
	}
	return h.String()
}
