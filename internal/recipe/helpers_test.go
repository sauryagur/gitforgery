package recipe

import (
	"bytes"
	"os"
	"testing"
	"text/template"
)

// renderTemplate executes tmpl against data and returns the output.
func renderTemplate(t *testing.T, tmpl *template.Template, data TmplData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	return buf.String()
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
