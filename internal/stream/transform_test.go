package stream

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/sauryagur/gitforgery/internal/plan"
)

// fixtureStream is a small fast-export stream shaped exactly like git's
// output (verified against git 2.47): blob, two commits with marks and
// original-oids, an annotated tag and a lightweight-tag reset.
const fixtureStream = `blob
mark :1
original-oid 1111111111111111111111111111111111111111
data 6
hello
commit refs/heads/main
mark :2
original-oid 2222222222222222222222222222222222222222
author Old <old@x.io> 1577872800 +0000
committer Old <old@x.io> 1577872800 +0000
data 10
first msg
M 100644 :1 a.txt
commit refs/heads/main
mark :3
original-oid 3333333333333333333333333333333333333333
author Old <old@x.io> 1577959200 +0000
committer New <new@x.io> 1578045600 +0000
data 18
second

body line
from :2
M 100644 :3 b.txt
tag v1
from :3
original-oid c616bb120a1fba4956e32439825ea3fcbfc9b216
tagger Old <old@x.io> 1577872800 +0000
data 12
release one
reset refs/tags/light
from :3
`

func sig(name, email, when string) object.Signature {
	t, err := time.Parse("2006-01-02T15:04:05-0700", when)
	if err != nil {
		panic(err)
	}
	return object.Signature{Name: name, Email: email, When: t}
}

func TestRewriterPassthroughWhenNothingPlanned(t *testing.T) {
	rw := NewRewriter(nil)
	var out bytes.Buffer
	if err := rw.Run(strings.NewReader(fixtureStream), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.String() != fixtureStream {
		t.Errorf("passthrough mismatch:\n got %q\nwant %q", out.String(), fixtureStream)
	}
}

func TestRewriterTransforms(t *testing.T) {
	planned := []*plan.CommitPlan{
		{
			Old:         plumbing.NewHash("2222222222222222222222222222222222222222"),
			New:         plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Kind:        plan.KindModified,
			Changes:     []string{plan.ChangeAuthor, plan.ChangeAuthDate, plan.ChangeMessage},
			Author:      sig("Ada", "ada@x.io", "2020-01-01T12:00:00+0100"),
			Committer:   sig("Old", "old@x.io", "2020-01-01T10:00:00+0000"), // untouched
			Message:     "rewritten",
			OrigMessage: "first msg\n",
		},
		{
			Old:         plumbing.NewHash("3333333333333333333333333333333333333333"),
			New:         plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			Kind:        plan.KindModified,
			Changes:     []string{plan.ChangeCommitter}, // message identical -> data passthrough
			Author:      sig("Old", "old@x.io", "2020-01-01T10:00:00+0000"),
			Committer:   sig("New", "new@x.io", "2020-01-01T11:11:11+0000"),
			Message:     "second\n\nbody line\n",
			OrigMessage: "second\n\nbody line\n",
		},
	}

	tests := []struct {
		name    string
		wantIn  []string
		wantNot []string
	}{
		{
			name: "author line replaced with planned identity",
			// 2020-01-01T12:00:00+01:00 == 1577876400 epoch seconds.
			wantIn: []string{"author Ada <ada@x.io> 1577876400 +0100"},
			wantNot: []string{
				"author Old <old@x.io> 1577872800 +0000\ndata 10\nfirst msg",
			},
		},
		{
			name:    "untouched committer line kept byte-for-byte",
			wantIn:  []string{"committer Old <old@x.io> 1577872800 +0000"},
			wantNot: []string{"committer Ada <ada@x.io>"},
		},
		{
			name:   "message block replaced",
			wantIn: []string{"data 9\nrewritten"},
			wantNot: []string{
				"data 10\nfirst msg\n",
			},
		},
		{
			name:   "identical message passes through unchanged",
			wantIn: []string{"data 18\nsecond\n\nbody line\n"},
		},
		{
			name: "blob tag reset and file ops untouched",
			wantIn: []string{
				"blob\nmark :1\noriginal-oid 1111111111111111111111111111111111111111\ndata 6\nhello\n",
				"tag v1\n", "tagger Old <old@x.io> 1577872800 +0000\n",
				"release one\n", "reset refs/tags/light\n",
				"M 100644 :3 b.txt\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := NewRewriter(planned)
			var out bytes.Buffer
			if err := rw.Run(strings.NewReader(fixtureStream), &out); err != nil {
				t.Fatalf("Run: %v", err)
			}
			got := out.String()
			for _, want := range tt.wantIn {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q:\n%s", want, got)
				}
			}
			for _, banned := range tt.wantNot {
				if strings.Contains(got, banned) {
					t.Errorf("output still contains %q:\n%s", banned, got)
				}
			}
		})
	}
}

func TestRewriterDateOnlyRetimesIdentityLines(t *testing.T) {
	// The first fixture commit is planned with date-only changes: neither
	// "author" nor "committer" appears in Changes, but the planned
	// identity carries a new timestamp. The emitted lines must still be
	// rewritten — otherwise the commit would import unchanged and the
	// preview-hash canary would (correctly) fail on the mismatch.
	planned := []*plan.CommitPlan{
		{
			Old:         plumbing.NewHash("2222222222222222222222222222222222222222"),
			New:         plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			Kind:        plan.KindModified,
			Changes:     []string{plan.ChangeAuthDate, plan.ChangeCommDate},
			Author:      sig("Old", "old@x.io", "2020-01-01T12:00:00+0000"),
			Committer:   sig("Old", "old@x.io", "2020-01-01T12:00:00+0000"),
			Message:     "first msg\n",
			OrigMessage: "first msg\n",
		},
	}
	rw := NewRewriter(planned)
	var out bytes.Buffer
	if err := rw.Run(strings.NewReader(fixtureStream), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	// 2020-01-01T12:00:00+0000 == 1577880000 epoch seconds.
	for _, want := range []string{
		"author Old <old@x.io> 1577880000 +0000\n",
		"committer Old <old@x.io> 1577880000 +0000\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{
		"author Old <old@x.io> 1577872800 +0000\n",
		"committer Old <old@x.io> 1577872800 +0000\n",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("output still contains original %q:\n%s", banned, got)
		}
	}
}

func TestRewriterMarksTable(t *testing.T) {
	rw := NewRewriter(nil)
	var out bytes.Buffer
	if err := rw.Run(strings.NewReader(fixtureStream), &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := map[string]string{
		"1": "1111111111111111111111111111111111111111",
		"2": "2222222222222222222222222222222222222222",
		"3": "3333333333333333333333333333333333333333",
	}
	got := rw.Marks()
	if len(got) != len(want) {
		t.Fatalf("marks = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("marks[%s] = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseMarksTable(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "fast-import export-marks shape",
			data: ":1 78981922613b2afb6025042ff6bd878ac1994e85\n:2 9b538d4a874b6d9cb2e1b2928585712a87b24a74\n",
			want: map[string]string{
				"1": "78981922613b2afb6025042ff6bd878ac1994e85",
				"2": "9b538d4a874b6d9cb2e1b2928585712a87b24a74",
			},
		},
		{name: "empty table", data: "", want: map[string]string{}},
		{name: "missing colon prefix", data: "1 deadbeef\n", wantErr: true},
		{name: "non numeric mark", data: ":x deadbeef\n", wantErr: true},
		{name: "truncated line", data: ":1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMarksTable([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseMarksTable(%q) error = %v, wantErr %v", tt.data, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("table = %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("table[%s] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestDrainShortBodyErrors(t *testing.T) {
	tok := Token{Size: 10, Body: strings.NewReader("abc")}
	if err := Drain(tok); err == nil {
		t.Fatal("Drain on short body should fail")
	}
}
