package gitx

import "testing"

func TestSplitJoinMessage(t *testing.T) {
	tests := []struct {
		name          string
		msg           string
		subject, body string
		roundTrips    bool // Join(Split(msg)) == msg
	}{
		{"single line", "Initial commit", "Initial commit", "", true},
		{"no trailing newline", "Initial", "Initial", "", true},
		{"body without blank separator", "Subject\nBody", "Subject", "", false},
		{"empty message", "", "", "", true},
		{"only newline", "\n", "", "", false},
		{"multi paragraph body", "S\n\nB1\n\nB2\n", "S", "B1\n\nB2\n", true},
		{"empty subject", "\n\nbody\n", "", "body\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject, body := SplitMessage(tt.msg)
			if subject != tt.subject || body != tt.body {
				t.Errorf("SplitMessage(%q) = (%q, %q), want (%q, %q)",
					tt.msg, subject, body, tt.subject, tt.body)
			}
			got := JoinMessage(tt.subject, tt.body)
			if tt.roundTrips && got != tt.msg {
				t.Errorf("JoinMessage round trip = %q, want %q", got, tt.msg)
			}
		})
	}
}

func TestJoinMessageEmptyBody(t *testing.T) {
	if got := JoinMessage("only", ""); got != "only" {
		t.Errorf("JoinMessage with empty body = %q, want %q", got, "only")
	}
}
