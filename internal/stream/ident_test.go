package stream

import (
	"testing"
	"time"
)

func TestParseIdentRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Ident
		wantErr bool
	}{
		{
			name: "canonical identity",
			in:   "Ada Lovelace <ada@example.com> 1577872800 +0000",
			want: Ident{Name: "Ada Lovelace", Email: "ada@example.com",
				When: time.Unix(1577872800, 0).In(time.FixedZone("", 0))},
		},
		{
			name: "negative zone offset preserved",
			in:   "A B <a@b.io> 1000 -0530",
			want: Ident{Name: "A B", Email: "a@b.io",
				When: time.Unix(1000, 0).In(time.FixedZone("", -5*3600-1800))},
		},
		{
			name: "empty name allowed by stream grammar",
			in:   " <x@y.z> 42 +0000",
			want: Ident{Name: "", Email: "x@y.z",
				When: time.Unix(42, 0).In(time.FixedZone("", 0))},
		},
		{name: "missing angle brackets", in: "no brackets here", wantErr: true},
		{name: "reversed brackets", in: "a >b< 1 +0000", wantErr: true},
		{name: "missing timestamp", in: "N <e@x> ", wantErr: true},
		{name: "non numeric timestamp", in: "N <e@x> soon +0000", wantErr: true},
		{name: "bad zone", in: "N <e@x> 100 UTC", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIdent(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseIdent(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Name != tt.want.Name || got.Email != tt.want.Email ||
				got.When.Unix() != tt.want.When.Unix() ||
				got.When.Format("-0700") != tt.want.When.Format("-0700") {
				t.Errorf("ParseIdent(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			// String must render back the exact input bytes.
			if s := got.String(); s != tt.in {
				t.Errorf("round trip = %q, want %q", s, tt.in)
			}
		})
	}
}
