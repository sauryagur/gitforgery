package recipe

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/sauryagur/gitforgery/internal/gitx"
	"gopkg.in/yaml.v3"
)

// recipeYAML mirrors the §6 file format. Kept private so the public API can
// expose validated, compiled data instead of raw strings and pointer bools.
type recipeYAML struct {
	Range   string      `yaml:"range"`
	Match   []ruleYAML  `yaml:"match"`
	Options optionsYAML `yaml:"options"`
}

type ruleYAML struct {
	When whenYAML `yaml:"when"`
	Set  setYAML  `yaml:"set"`
}

type whenYAML struct {
	SHA            string `yaml:"sha"`
	Author         string `yaml:"author"`
	AuthorEmail    string `yaml:"author_email"`
	Committer      string `yaml:"committer"`
	CommitterEmail string `yaml:"committer_email"`
	Subject        string `yaml:"subject"`
	Branch         string `yaml:"branch"`
}

// FromYAML converts a decoded when block into its compiled form.
func FromYAML(w whenYAML) When {
	return When{
		SHA:            w.SHA,
		Author:         w.Author,
		AuthorEmail:    w.AuthorEmail,
		Committer:      w.Committer,
		CommitterEmail: w.CommitterEmail,
		Subject:        w.Subject,
		Branch:         w.Branch,
	}
}

type setYAML struct {
	Author        string `yaml:"author"`
	Committer     string `yaml:"committer"`
	AuthorDate    string `yaml:"author_date"`
	CommitterDate string `yaml:"committer_date"`
	Subject       string `yaml:"subject"`
	Body          string `yaml:"body"`
}

type optionsYAML struct {
	StripSignatures   *bool    `yaml:"strip_signatures"`
	ReSign            *bool    `yaml:"re_sign"`
	PreserveDateOrder *bool    `yaml:"preserve_date_order"`
	Refs              []string `yaml:"refs"`
	BackupRef         *string  `yaml:"backup_ref"`
}

// ParseFile reads and parses the recipe stored at path.
func ParseFile(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read recipe: %w", err)
	}
	rec, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rec, nil
}

// Parse decodes and validates a recipe. Unknown fields are rejected so typos
// in selectors or options fail loudly instead of silently doing nothing.
func Parse(data []byte) (*Recipe, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var y recipeYAML
	if err := dec.Decode(&y); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode recipe: %w", err)
	}

	rec := &Recipe{Range: y.Range}
	for i, ry := range y.Match {
		rule, err := compileRule(ry, i)
		if err != nil {
			return nil, err
		}
		rec.Match = append(rec.Match, rule)
	}
	if err := compileOptions(&y.Options, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func compileRule(ry ruleYAML, i int) (*Rule, error) {
	at := func(field string) string { return fmt.Sprintf("match[%d].%s", i, field) }

	rule := &Rule{When: FromYAML(ry.When)}
	for _, c := range []struct {
		field string
		raw   string
		dst   **regexp.Regexp
	}{
		{"sha", ry.When.SHA, &rule.when.sha},
		{"author", ry.When.Author, &rule.when.author},
		{"author_email", ry.When.AuthorEmail, &rule.when.authorEmail},
		{"committer", ry.When.Committer, &rule.when.committer},
		{"committer_email", ry.When.CommitterEmail, &rule.when.committerEmail},
		{"subject", ry.When.Subject, &rule.when.subject},
		{"branch", ry.When.Branch, &rule.when.branch},
	} {
		if c.raw == "" {
			continue
		}
		re, err := regexp.Compile(c.raw)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid regex %q: %v", at("when."+c.field), c.raw, err)
		}
		*c.dst = re
	}
	s := &rule.Set
	if ry.Set.Author != "" {
		if ry.Set.Author == inheritKeyword {
			return nil, fmt.Errorf("%s: \"inherit\" is only valid for committer (the author has nothing to inherit)", at("set.author"))
		}
		id, err := gitx.ParseIdentity(ry.Set.Author)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", at("set.author"), err)
		}
		s.author = id
	}
	switch {
	case ry.Set.Committer == "":
	case ry.Set.Committer == inheritKeyword:
		s.committerInh = true
	default:
		id, err := gitx.ParseIdentity(ry.Set.Committer)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", at("set.committer"), err)
		}
		s.committer = id
	}

	if ry.Set.AuthorDate != "" {
		e, err := parseDateExpr(ry.Set.AuthorDate)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", at("set.author_date"), err)
		}
		if e.Kind == DateAuthor {
			return nil, fmt.Errorf("%s: \"author\" is only valid for committer_date", at("set.author_date"))
		}
		s.authorDate = e
	}
	if ry.Set.CommitterDate != "" {
		e, err := parseDateExpr(ry.Set.CommitterDate)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", at("set.committer_date"), err)
		}
		s.committerDate = e
	}

	if ry.Set.Subject != "" {
		t, err := checkTemplate("subject", ry.Set.Subject)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid template %q: %v", at("set.subject"), ry.Set.Subject, err)
		}
		s.subjectTmpl = t
	}
	if ry.Set.Body != "" {
		t, err := checkTemplate("body", ry.Set.Body)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid template %q: %v", at("set.body"), ry.Set.Body, err)
		}
		s.bodyTmpl = t
	}
	return rule, nil
}

// checkTemplate parses tmpl source and executes it once against an empty
// TmplData, so misspelled fields fail at recipe load instead of mid-plan.
func checkTemplate(name, src string) (*template.Template, error) {
	tmpl, err := template.New(name).Parse(src)
	if err != nil {
		return nil, err
	}
	if err := tmpl.Execute(&bytes.Buffer{}, TmplData{}); err != nil {
		return nil, fmt.Errorf("template references unavailable fields: %v", err)
	}
	return tmpl, nil
}

const inheritKeyword = "inherit"

func compileOptions(y *optionsYAML, rec *Recipe) error {
	boolOr := func(p *bool, def bool) bool {
		if p == nil {
			return def
		}
		return *p
	}
	rec.reSign = boolOr(y.ReSign, false)
	// Invalid signatures must go; they survive only when the user asked for
	// re-signing AND explicitly declined stripping.
	strip := true
	if rec.reSign && y.StripSignatures != nil {
		strip = *y.StripSignatures
	}
	rec.stripSignatures = strip
	rec.preserveDateOrder = boolOr(y.PreserveDateOrder, false)

	if len(y.Refs) == 0 {
		rec.refs = append(rec.refs, DefaultRefs...)
	} else {
		for _, ref := range y.Refs {
			if strings.TrimSpace(ref) == "" {
				return errors.New("options.refs: empty pattern")
			}
		}
		rec.refs = y.Refs
	}

	rec.backupRef = DefaultBackupRef
	if y.BackupRef != nil {
		ref := strings.TrimSpace(*y.BackupRef)
		if ref == "" {
			return errors.New("options.backup_ref: must not be empty")
		}
		rec.backupRef = ref
	}
	return nil
}

// parseDateExpr implements the §6 date value grammar:
//
//	RFC3339 | author | now | first[+dur|-dur] | prev[+dur|-dur]
//
// where dur is a Go duration such as "4h", "90m" or "1h30m".
func parseDateExpr(s string) (DateExpr, error) {
	switch s {
	case "author":
		return DateExpr{Kind: DateAuthor}, nil
	case "now":
		return DateExpr{Kind: DateNow}, nil
	case "first":
		return DateExpr{Kind: DateFirst}, nil
	case "prev":
		return DateExpr{Kind: DatePrev}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return DateExpr{Kind: DateLiteral, Literal: t}, nil
	}
	for _, base := range []struct {
		name string
		kind DateKind
	}{
		{"first", DateFirst},
		{"prev", DatePrev},
	} {
		rest, ok := strings.CutPrefix(s, base.name)
		if !ok || rest == "" {
			continue
		}
		sign := byte('+')
		switch rest[0] {
		case '+', '-':
			sign = rest[0]
			rest = rest[1:]
		}
		dur, err := time.ParseDuration(rest)
		if err != nil || rest == "" || rest[0] == '+' || rest[0] == '-' {
			return DateExpr{}, fmt.Errorf("invalid duration in %q: want e.g. %s+4h or %s-90m", s, base.name, base.name)
		}
		if sign == '-' {
			dur = -dur
		}
		return DateExpr{Kind: base.kind, Dur: dur}, nil
	}
	return DateExpr{}, fmt.Errorf("invalid date expression %q: want RFC3339, \"author\", \"now\", first±dur or prev±dur", s)
}
