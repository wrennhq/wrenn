package email

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	text_template "text/template"
)

//go:embed templates/*.html templates/*.txt
var templateFS embed.FS

// templates holds the parsed HTML and plain-text template sets.
type templates struct {
	html *template.Template
	text *text_template.Template
}

// mustLoadTemplates parses all embedded templates. Panics on error
// because malformed templates are a build-time bug.
func mustLoadTemplates() *templates {
	html, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		panic(fmt.Sprintf("email: failed to parse HTML templates: %v", err))
	}

	text, err := text_template.ParseFS(templateFS, "templates/*.txt")
	if err != nil {
		panic(fmt.Sprintf("email: failed to parse text templates: %v", err))
	}

	return &templates{html: html, text: text}
}

// renderHTML executes the HTML base template with the given data.
func (t *templates) renderHTML(data EmailData) (string, error) {
	var buf bytes.Buffer
	if err := t.html.ExecuteTemplate(&buf, "base.html", data); err != nil {
		return "", fmt.Errorf("execute html template: %w", err)
	}
	return buf.String(), nil
}

// renderText executes the plain-text base template with the given data.
func (t *templates) renderText(data EmailData) (string, error) {
	var buf bytes.Buffer
	if err := t.text.ExecuteTemplate(&buf, "base.txt", data); err != nil {
		return "", fmt.Errorf("execute text template: %w", err)
	}
	return buf.String(), nil
}
