package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// execute runs the root command with args and returns its combined output.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()

	buf := &bytes.Buffer{}
	root := newRootCmd()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err := root.Execute()
	return buf.String(), err
}

func TestExecute(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOut string   // exact output; empty skips the check
		wantIn  []string // substrings required in output
		wantErr bool     // expect a non-nil error
		errIn   string   // substring required in error; empty skips
	}{
		{
			name:    "version prints module and version",
			args:    []string{"version"},
			wantOut: "gitforgery dev\n",
		},
		{
			name:    "version flag prints module and version",
			args:    []string{"--version"},
			wantOut: "gitforgery dev\n",
		},
		{
			name: "no args prints help",
			wantIn: []string{
				"gitforgery crafts git history",
				"Available Commands:",
				"version",
			},
		},
		{
			name:   "help flag lists version",
			args:   []string{"--help"},
			wantIn: []string{"Available Commands:", "version"},
		},
		{
			name:    "unknown command errors",
			args:    []string{"bogus"},
			wantErr: true,
			errIn:   `unknown command "bogus" for "gitforgery"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := execute(t, tt.args...)

			if tt.wantErr != (err != nil) {
				t.Fatalf("Execute(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if err != nil && tt.errIn != "" && !strings.Contains(err.Error(), tt.errIn) {
				t.Errorf("Execute(%v) error = %q, want substring %q", tt.args, err, tt.errIn)
			}
			if tt.wantOut != "" && out != tt.wantOut {
				t.Errorf("Execute(%v) output = %q, want %q", tt.args, out, tt.wantOut)
			}
			for _, sub := range tt.wantIn {
				if !strings.Contains(out, sub) {
					t.Errorf("Execute(%v) output = %q, want substring %q", tt.args, out, sub)
				}
			}
		})
	}
}

func TestVersionDefault(t *testing.T) {
	if version != "dev" {
		t.Errorf("version = %q, want default %q", version, "dev")
	}
}
