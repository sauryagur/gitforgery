package recipe

import (
	"strings"
	"testing"
	"time"
)

const specExample = `# recipe.yaml — identity/date/message forgery plan
range: "main~10..main"
match:
  - when:
      author_email: '@oldcorp\.com$'
    set:
      author: "Ada Lovelace <ada@example.com>"
      committer: "inherit"
      author_date: "2020-01-01T10:00:00+00:00"
      committer_date: "author"
      subject: "{{.Subject}}"
      body: "{{.Body}}"
  - when:
      sha: "a1b2c3d4"
    set:
      author_date: "prev+2h"
options:
  strip_signatures: true
  re_sign: false
  preserve_date_order: true
  refs: ["refs/heads/*", "refs/tags/*", "refs/notes/*"]
  backup_ref: "refs/forgery/original"
`

func TestParseSpecExample(t *testing.T) {
	rec, err := Parse([]byte(specExample))
	if err != nil {
		t.Fatalf("Parse(spec example): %v", err)
	}

	if rec.Range != "main~10..main" {
		t.Errorf("Range = %q, want %q", rec.Range, "main~10..main")
	}
	if len(rec.Match) != 2 {
		t.Fatalf("len(Match) = %d, want 2", len(rec.Match))
	}

	rule := rec.Match[0]
	if !rule.Matches(Target{AuthorEmail: "dev@oldcorp.com"}) {
		t.Errorf("rule 0 should match @oldcorp.com email")
	}
	if rule.Matches(Target{AuthorEmail: "dev@newcorp.com"}) {
		t.Errorf("rule 0 should not match other emails")
	}
	id, ok := rule.Set.AuthorIdent()
	if !ok || id.Name != "Ada Lovelace" || id.Email != "ada@example.com" {
		t.Errorf("AuthorIdent = (%+v, %v), want Ada Lovelace <ada@example.com>", id, ok)
	}
	if !rule.Set.CommitterInherit() {
		t.Error("CommitterInherit = false, want true")
	}
	ad, ok := rule.Set.AuthorDate()
	if !ok || ad.Kind != DateLiteral {
		t.Fatalf("AuthorDate kind = %v (%v), want literal", ad.Kind, ok)
	}
	wantTime := time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)
	if !ad.Literal.Equal(wantTime) {
		t.Errorf("AuthorDate literal = %v, want %v", ad.Literal, wantTime)
	}
	cd, _ := rule.Set.CommitterDate()
	if cd.Kind != DateAuthor {
		t.Errorf("CommitterDate kind = %v, want DateAuthor", cd.Kind)
	}
	st, ok := rule.Set.SubjectTmpl()
	if !ok {
		t.Fatal("SubjectTmpl missing")
	}
	got := renderTemplate(t, st, TmplData{Subject: "hello"})
	if got != "hello" {
		t.Errorf("SubjectTmpl rendered %q, want \"hello\"", got)
	}
	rule2 := rec.Match[1]
	if !rule2.Matches(Target{SHA: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"}) {
		t.Error("rule 1 should match sha prefix a1b2c3d4")
	}
	pd, ok := rule2.Set.AuthorDate()
	if !ok || pd.Kind != DatePrev || pd.Dur != 2*time.Hour {
		t.Errorf("rule 1 AuthorDate = %+v (%v), want prev+2h", pd, ok)
	}

	if !rec.PreserveDateOrder() {
		t.Error("PreserveDateOrder = false, want true")
	}
	if !rec.StripSignatures() || rec.ReSign() {
		t.Errorf("StripSignatures = %v, ReSign = %v; want true/false", rec.StripSignatures(), rec.ReSign())
	}
	if rec.BackupRef() != "refs/forgery/original" {
		t.Errorf("BackupRef = %q", rec.BackupRef())
	}
}

func TestParseEmptyRecipeIsNoOpWithDefaults(t *testing.T) {
	for _, in := range []string{"", "---\n", "range: all\n"} {
		rec, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if len(rec.Match) != 0 {
			t.Errorf("Parse(%q): Match = %d rules, want 0", in, len(rec.Match))
		}
		if !rec.StripSignatures() {
			t.Errorf("StripSignatures default = false, want true")
		}
		if got, want := rec.Refs(), DefaultRefs; len(got) != len(want) {
			t.Errorf("Refs = %v, want %v", got, want)
		}
	}
}

func TestOptionsResolution(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		strip   bool
		reSign  bool
		monotnc bool
	}{
		{
			name:  "defaults",
			yaml:  "",
			strip: true,
		},
		{
			name:   "re_sign keeps stripping by default",
			yaml:   "options:\n  re_sign: true\n",
			strip:  true,
			reSign: true,
		},
		{
			name:   "re_sign with explicit strip false preserves",
			yaml:   "options:\n  re_sign: true\n  strip_signatures: false\n",
			reSign: true,
		},
		{
			name:  "strip forced even when declined without re_sign",
			yaml:  "options:\n  strip_signatures: false\n",
			strip: true,
		},
		{
			name:    "preserve_date_order on",
			yaml:    "options:\n  preserve_date_order: true\n",
			strip:   true,
			monotnc: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := Parse([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if rec.StripSignatures() != tt.strip {
				t.Errorf("StripSignatures = %v, want %v", rec.StripSignatures(), tt.strip)
			}
			if rec.ReSign() != tt.reSign {
				t.Errorf("ReSign = %v, want %v", rec.ReSign(), tt.reSign)
			}
			if rec.PreserveDateOrder() != tt.monotnc {
				t.Errorf("PreserveDateOrder = %v, want %v", rec.PreserveDateOrder(), tt.monotnc)
			}
		})
	}
}

func TestCustomRefsAndBackupRef(t *testing.T) {
	rec, err := Parse([]byte("options:\n  refs: [\"refs/heads/*\"]\n  backup_ref: refs/mine/x\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := rec.Refs(); len(got) != 1 || got[0] != "refs/heads/*" {
		t.Errorf("Refs = %v, want [refs/heads/*]", got)
	}
	if rec.BackupRef() != "refs/mine/x" {
		t.Errorf("BackupRef = %q, want refs/mine/x", rec.BackupRef())
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		errIn string // required substring of the error
	}{
		{
			name:  "unknown top-level field",
			in:    "rang: all\n",
			errIn: "field rang not found",
		},
		{
			name:  "unknown when field (typo)",
			in:    "match:\n  - when:\n      auther: x\n    set: {}\n",
			errIn: "field auther not found",
		},
		{
			name:  "author inherit rejected",
			in:    "match:\n  - set:\n      author: inherit\n",
			errIn: `set.author: "inherit" is only valid for committer`,
		},
		{
			name:  "bad author identity",
			in:    "match:\n  - set:\n      author: \"no brackets\"\n",
			errIn: "set.author: invalid identity",
		},
		{
			name:  "bad committer identity",
			in:    "match:\n  - set:\n      committer: \"A <\"\n",
			errIn: "set.committer: invalid identity",
		},
		{
			name:  "author_date cannot be author",
			in:    "match:\n  - set:\n      author_date: author\n",
			errIn: "set.author_date: \"author\" is only valid for committer_date",
		},
		{
			name:  "bad date grammar",
			in:    "match:\n  - set:\n      committer_date: yesterday\n",
			errIn: "set.committer_date: invalid date expression",
		},
		{
			name:  "invalid regex",
			in:    "match:\n  - when:\n      author_email: '@old(\\.com'\n    set: {}\n",
			errIn: "match[0].when.author_email: invalid regex",
		},
		{
			name:  "empty refs pattern",
			in:    "options:\n  refs: [\" \"]\n",
			errIn: "options.refs: empty pattern",
		},
		{
			name:  "empty backup ref",
			in:    "options:\n  backup_ref: \" \"\n",
			errIn: "options.backup_ref: must not be empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error containing %q", tt.in, tt.errIn)
			}
			if !strings.Contains(err.Error(), tt.errIn) {
				t.Errorf("error = %q, want substring %q", err, tt.errIn)
			}
		})
	}
}

func TestParseFile(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := ParseFile(t.TempDir() + "/nope.yaml"); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
	t.Run("valid file", func(t *testing.T) {
		path := t.TempDir() + "/r.yaml"
		if err := writeFile(path, "range: all\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
		rec, err := ParseFile(path)
		if err != nil {
			t.Fatalf("ParseFile: %v", err)
		}
		if rec.Range != "all" {
			t.Errorf("Range = %q, want all", rec.Range)
		}
	})
}

func TestParseDateExpr(t *testing.T) {
	tests := []struct {
		in      string
		kind    DateKind
		dur     time.Duration
		wantLit time.Time // for literals
		wantErr bool
	}{
		{in: "author", kind: DateAuthor},
		{in: "now", kind: DateNow},
		{in: "first", kind: DateFirst},
		{in: "prev", kind: DatePrev},
		{in: "first+4h", kind: DateFirst, dur: 4 * time.Hour},
		{in: "first-90m", kind: DateFirst, dur: -90 * time.Minute},
		{in: "prev+1h30m", kind: DatePrev, dur: 90 * time.Minute},
		{in: "prev-2h45m", kind: DatePrev, dur: -(2*time.Hour + 45*time.Minute)},
		{
			in:      "2020-01-01T10:00:00+05:30",
			kind:    DateLiteral,
			wantLit: time.Date(2020, 1, 1, 10, 0, 0, 0, time.FixedZone("+0530", 5*3600+1800)),
		},
		{in: "2020-01-01", wantErr: true}, // date-only is not RFC3339
		{in: "yesterday", wantErr: true},  // outside grammar
		{in: "firstly+4h", wantErr: true}, // prefix must be exact
		{in: "first+", wantErr: true},     // empty duration
		{in: "prev+x", wantErr: true},     // garbage duration
		{in: "prev++4h", wantErr: true},   // double sign
		{in: "PREV+4h", wantErr: true},    // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseDateExpr(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDateExpr(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDateExpr(%q): %v", tt.in, err)
			}
			if got.Kind != tt.kind {
				t.Errorf("kind = %v, want %v", got.Kind, tt.kind)
			}
			if got.Dur != tt.dur {
				t.Errorf("dur = %v, want %v", got.Dur, tt.dur)
			}
			if tt.wantLit != (time.Time{}) && !got.Literal.Equal(tt.wantLit) {
				t.Errorf("literal = %v, want %v", got.Literal, tt.wantLit)
			}
		})
	}
}

func TestRuleMatchesAllWhenAbsent(t *testing.T) {
	rec, err := Parse([]byte("match:\n  - set:\n      committer: \"X <x@x>\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !rec.Match[0].Matches(Target{SHA: "anything"}) {
		t.Error("rule with empty when must match everything")
	}
}

func TestBranchSelectorAnyBranch(t *testing.T) {
	src := `
match:
  - when:
      branch: "^refs/heads/release/"
    set:
      author: "Rel Bot <rel@example.com>"
`
	rec, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	rule := rec.Match[0]
	if !rule.Matches(Target{Branches: []string{"refs/heads/main", "refs/heads/release/1.0"}}) {
		t.Error("should match when any branch matches")
	}
	if rule.Matches(Target{Branches: []string{"refs/heads/main"}}) {
		t.Error("should not match when no branch matches")
	}
	if rule.Matches(Target{}) {
		t.Error("should not match with no branches")
	}
}
