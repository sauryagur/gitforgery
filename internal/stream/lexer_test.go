package stream

import (
	"io"
	"strings"
	"testing"
)

func TestScannerTokens(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		want    []Token
		wantErr string
	}{
		{
			name:   "empty stream",
			stream: "",
			want:   nil,
		},
		{
			name: "commit block with headers data and file op",
			stream: "commit refs/heads/main\n" +
				"mark :1\n" +
				"original-oid abc123\n" +
				"author A <a@x> 100 +0000\n" +
				"committer B <b@x> 200 +0100\n" +
				"data 4\n" +
				"msg\n" +
				"from :0\n" +
				"M 100644 :2 a.txt\n",
			want: []Token{
				{Kind: KindCommand, Verb: "commit", Arg: "refs/heads/main", Raw: "commit refs/heads/main"},
				{Kind: KindHeader, Verb: "mark", Arg: ":1", Raw: "mark :1"},
				{Kind: KindHeader, Verb: "original-oid", Arg: "abc123", Raw: "original-oid abc123"},
				{Kind: KindHeader, Verb: "author", Arg: "A <a@x> 100 +0000", Raw: "author A <a@x> 100 +0000"},
				{Kind: KindHeader, Verb: "committer", Arg: "B <b@x> 200 +0100", Raw: "committer B <b@x> 200 +0100"},
				{Kind: KindData, Verb: "data", Arg: "4", Raw: "data 4", Size: 4, Body: strings.NewReader("msg\n")},
				{Kind: KindHeader, Verb: "from", Arg: ":0", Raw: "from :0"},
				{Kind: KindOther, Verb: "M", Arg: "100644 :2 a.txt", Raw: "M 100644 :2 a.txt"},
			},
		},
		{
			name:   "tag reset blob and done",
			stream: "tag v1\nfrom :4\ntagger T <t@x> 5 -0300\ndata 3\nhi\nreset refs/tags/l\nfrom :4\nblob\nmark :9\ndata 6\nhello!done\n",
			want: []Token{
				{Kind: KindCommand, Verb: "tag", Arg: "v1"},
				{Kind: KindHeader, Verb: "from", Arg: ":4"},
				{Kind: KindHeader, Verb: "tagger", Arg: "T <t@x> 5 -0300"},
				{Kind: KindData, Verb: "data", Arg: "3", Raw: "data 3", Size: 3, Body: strings.NewReader("hi\n")},
				{Kind: KindCommand, Verb: "reset", Arg: "refs/tags/l"},
				{Kind: KindHeader, Verb: "from", Arg: ":4"},
				{Kind: KindCommand, Verb: "blob"},
				{Kind: KindHeader, Verb: "mark", Arg: ":9"},
				{Kind: KindData, Verb: "data", Arg: "6", Raw: "data 6", Size: 6, Body: strings.NewReader("hello!")},
				{Kind: KindCommand, Verb: "done"},
			},
		},
		{
			name:   "feature option progress checkpoint and comment",
			stream: "feature done\noption max-pack-size=1\nprogress one\ncheckpoint\n# just a note\n",
			want: []Token{
				{Kind: KindCommand, Verb: "feature", Arg: "done"},
				{Kind: KindCommand, Verb: "option", Arg: "max-pack-size=1"},
				{Kind: KindCommand, Verb: "progress", Arg: "one"},
				{Kind: KindCommand, Verb: "checkpoint"},
				{Kind: KindOther, Verb: "#", Arg: "just a note"},
			},
		},
		{
			name:    "bad data count",
			stream:  "data nope\n",
			wantErr: `invalid data count "nope"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewScanner(strings.NewReader(tt.stream))
			var got []Token
			var payloads [][]byte
			for {
				tok, err := sc.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					if tt.wantErr != "" && strings.Contains(err.Error(), tt.wantErr) {
						return
					}
					t.Fatalf("Next() error = %v, want %q", err, tt.wantErr)
				}
				got = append(got, tok)
				if tok.Kind == KindData { // drain in lockstep
					payload, err := io.ReadAll(io.LimitReader(tok.Body, tok.Size))
					if err != nil {
						t.Fatalf("drain data block: %v", err)
					}
					payloads = append(payloads, payload)
				}
			}
			if tt.wantErr != "" {
				t.Fatalf("no error scanning stream, want %q", tt.wantErr)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("scanned %d tokens, want %d:\n%+v", len(got), len(tt.want), got)
			}
			pi := 0
			for i := range got {
				w, g := tt.want[i], got[i]
				if w.Kind != g.Kind || w.Verb != g.Verb || w.Arg != g.Arg || w.Size != g.Size {
					t.Errorf("token %d = %+v, want %+v", i, g, w)
					continue
				}
				if w.Raw != "" && w.Raw != g.Raw {
					t.Errorf("token %d raw = %q, want %q", i, g.Raw, w.Raw)
				}
				if w.Body != nil {
					wantPayload, _ := io.ReadAll(w.Body)
					if string(payloads[pi]) != string(wantPayload) {
						t.Errorf("token %d payload = %q, want %q", i, payloads[pi], wantPayload)
					}
					pi++
				}
			}
		})
	}
}

func TestScannerDataBodyIsExactlySized(t *testing.T) {
	// The scanner must hand out exactly Size bytes even when the payload
	// contains newlines and is followed by more commands.
	sc := NewScanner(strings.NewReader("blob\nmark :1\ndata 9\nab\ncd\nef\ncommit refs/x\n"))
	if _, err := sc.Next(); err != nil { // blob
		t.Fatal(err)
	}
	if _, err := sc.Next(); err != nil { // mark
		t.Fatal(err)
	}
	data, err := sc.Next()
	if err != nil || data.Kind != KindData {
		t.Fatalf("want data token, got %+v err %v", data, err)
	}
	body, err := io.ReadAll(data.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ab\ncd\nef\n" {
		t.Fatalf("body = %q, want %q", body, "ab\ncd\nef\n")
	}
	next, err := sc.Next()
	if err != nil {
		t.Fatal(err)
	}
	if next.Kind != KindCommand || next.Arg != "refs/x" {
		t.Fatalf("token after data = %+v, want commit refs/x", next)
	}
}
