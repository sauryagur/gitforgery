package gitx

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestParseIdentity(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Identity
		wantErr string // substring; empty means success
	}{
		{
			name: "name and email",
			in:   "Ada Lovelace <ada@example.com>",
			want: Identity{Name: "Ada Lovelace", Email: "ada@example.com"},
		},
		{
			name: "surrounding whitespace trimmed",
			in:   "  Ada <ada@example.com>  ",
			want: Identity{Name: "Ada", Email: "ada@example.com"},
		},
		{
			name:    "no display name rejected",
			in:      "<ada@example.com>",
			wantErr: "empty name",
		},
		{
			name:    "empty email rejected",
			in:      "Ada <>",
			wantErr: "empty email",
		},
		{
			name:    "missing brackets rejected",
			in:      "Ada ada@example.com",
			wantErr: `want "Name <email>"`,
		},
		{
			name:    "unclosed bracket rejected",
			in:      "Ada <ada@example.com",
			wantErr: `want "Name <email>"`,
		},
		{
			name:    "empty input rejected",
			in:      "",
			wantErr: "invalid identity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIdentity(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseIdentity(%q) error = %v, want substring %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIdentity(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseIdentity(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchRefPattern(t *testing.T) {
	tests := []struct {
		pattern, ref string
		want         bool
	}{
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/heads/main", "refs/heads/dev", false},
		{"refs/heads/*", "refs/heads/main", true},
		{"refs/heads/*", "refs/heads/feat/one", true}, // '*' crosses '/'
		{"refs/heads/*", "refs/heads/", false},        // nothing after prefix
		{"refs/heads/*", "refs/tags/main", false},
		{"refs/tags/v*", "refs/tags/v1.0", true},
		{"refs/tags/v*", "refs/tags/other", false},
		{"refs/*", "refs/notes/commits", true},
		{"refs/*", "refsd/notes", false},
		{"refs/a*b*c", "refs/a-x-b-y-c", true},
		{"refs/a*b*c", "refs/a-x-c-b", false},
		{"*", "anything/at/all", true},
	}
	for _, tt := range tests {
		if got := MatchRefPattern(tt.pattern, tt.ref); got != tt.want {
			t.Errorf("MatchRefPattern(%q, %q) = %v, want %v", tt.pattern, tt.ref, got, tt.want)
		}
	}
}

// gitHashObject feeds raw object bytes to `git hash-object` and returns the
// hex hash git itself computes. This makes real git the source of truth for
// preview-hash expectations instead of go-git's own encoder.
func gitHashObject(t *testing.T, objType string, content []byte) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-t", objType, "--stdin")
	cmd.Stdin = bytes.NewReader(content)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("git hash-object: %v: %s", err, errOut.String())
	}
	return strings.TrimSpace(out.String())
}

func mustHash(t *testing.T, h string) plumbing.Hash {
	t.Helper()
	got, err := plumbing.NewHash(h), error(nil)
	if err != nil {
		t.Fatalf("bad hash fixture %q: %v", h, err)
	}
	return got
}

func TestHashCommitMatchesGitHashObject(t *testing.T) {
	cest := time.FixedZone("+0200", 2*3600)
	when := time.Unix(1112911993, 0).In(cest) // 2005-04-07T22:13:13+02:00

	tree := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	parent := "0000000000000000000000000000000000000001"

	commit := &object.Commit{
		TreeHash: mustHash(t, tree),
		ParentHashes: []plumbing.Hash{
			mustHash(t, parent),
		},
		Author:    object.Signature{Name: "Ada Lovelace", Email: "ada@example.com", When: when},
		Committer: object.Signature{Name: "Ada Lovelace", Email: "ada@example.com", When: when},
		Message:   "subject line\n\nbody line\n",
	}

	// The exact bytes git should see, spelled out independently of go-git.
	raw := "tree " + tree + "\n" +
		"parent " + parent + "\n" +
		"author Ada Lovelace <ada@example.com> 1112911993 +0200\n" +
		"committer Ada Lovelace <ada@example.com> 1112911993 +0200\n" +
		"\n" +
		"subject line\n\nbody line\n"

	want := gitHashObject(t, "commit", []byte(raw))
	got, err := HashCommit(commit)
	if err != nil {
		t.Fatalf("HashCommit: %v", err)
	}
	if got.String() != want {
		t.Errorf("HashCommit = %s, want git hash-object %s", got, want)
	}
}

func TestHashCommitSignatureAndEncodingHeaders(t *testing.T) {
	when := time.Unix(1600000000, 0).In(time.UTC)

	commit := &object.Commit{
		TreeHash:     mustHash(t, "4b825dc642cb6eb9a060e54bf8d69288fbee4904"),
		Author:       object.Signature{Name: "A B", Email: "a@b.c", When: when},
		Committer:    object.Signature{Name: "A B", Email: "a@b.c", When: when},
		Message:      "msg\n",
		Encoding:     "ISO-8859-1",
		PGPSignature: "-----BEGIN PGP SIGNATURE-----\nabc\n",
	}

	raw := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"author A B <a@b.c> 1600000000 +0000\n" +
		"committer A B <a@b.c> 1600000000 +0000\n" +
		"encoding ISO-8859-1\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" abc\n" +
		"\n" +
		"msg\n"

	want := gitHashObject(t, "commit", []byte(raw))
	got, err := HashCommit(commit)
	if err != nil {
		t.Fatalf("HashCommit: %v", err)
	}
	if got.String() != want {
		t.Errorf("HashCommit = %s, want git hash-object %s", got, want)
	}
}
