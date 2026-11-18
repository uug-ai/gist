// Package render turns gist files into HTML: syntax-highlighted code with
// linkable line numbers, or rendered Markdown.
package render

import (
	"bytes"
	"html/template"
	"path"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// maxHighlight is the largest file that gets highlighted; bigger files are
// shown as plain text so one view can't keep the CPU busy for long.
const maxHighlight = 512 << 10

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithASTTransformers(util.Prioritized(linkRel{}, 100)),
	),
	// Raw HTML is left out (goldmark's default without html.WithUnsafe).
)

// Lexer picks a lexer by filename, then by content, then plain text.
func Lexer(filename, content string) chroma.Lexer {
	l := lexers.Match(filename)
	if l == nil {
		l = lexers.Analyse(content)
	}
	if l == nil {
		l = lexers.Fallback
	}
	return chroma.Coalesce(l)
}

// Language is the display name of a file's language.
func Language(filename, content string) string {
	l := Lexer(filename, content)
	name := l.Config().Name
	if name == "fallback" || name == "plaintext" {
		return "Text"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// IsMarkdown reports whether a file is rendered as Markdown.
func IsMarkdown(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	}
	return false
}

// Code highlights content as a table with line numbers. Line N gets the id
// prefix+N and its number links to it.
func Code(filename, content, prefix string) template.HTML {
	lexer := Lexer(filename, content)
	if len(content) > maxHighlight {
		lexer = lexers.Fallback
	}
	f := chromahtml.New(
		chromahtml.WithClasses(true),
		chromahtml.WithLineNumbers(true),
		chromahtml.LineNumbersInTable(true),
		chromahtml.WithLinkableLineNumbers(true, prefix),
		chromahtml.TabWidth(4),
	)
	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(content) + "</pre>")
	}
	var buf bytes.Buffer
	if err := f.Format(&buf, styles.Get("github"), it); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(content) + "</pre>")
	}
	return template.HTML(buf.String())
}

// Markdown renders GitHub-flavoured Markdown without raw HTML.
func Markdown(content string) template.HTML {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(content), &buf); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(content) + "</pre>")
	}
	return template.HTML(buf.String())
}

// CSS is the stylesheet for highlighted code: GitHub's light style, and
// GitHub dark when the page is in dark mode (data-theme="dark" on <html>,
// set by the theme toggle, as on draw.uug.ai).
func CSS() string {
	var light, dark bytes.Buffer
	f := chromahtml.New(chromahtml.WithClasses(true), chromahtml.WithLineNumbers(true))
	_ = f.WriteCSS(&light, styles.Get("github"))
	_ = f.WriteCSS(&dark, styles.Get("github-dark"))
	// Each style only in its own theme, so a token one style colours and the
	// other doesn't never mixes the two.
	return scopeCSS(light.String(), ":root:not([data-theme='dark'])") +
		scopeCSS(dark.String(), ":root[data-theme='dark']")
}

// scopeCSS prefixes every selector of chroma's stylesheet, which has one
// "/* Name */ selector { … }" rule per line, with scope.
func scopeCSS(css, scope string) string {
	var b strings.Builder
	for _, line := range strings.Split(css, "\n") {
		brace := strings.Index(line, "{")
		if brace < 0 {
			continue
		}
		head, body := line[:brace], line[brace:]
		if end := strings.Index(head, "*/"); end >= 0 {
			head = head[end+2:]
		}
		selectors := strings.Split(head, ",")
		for i, s := range selectors {
			selectors[i] = scope + " " + strings.TrimSpace(s)
		}
		b.WriteString(strings.Join(selectors, ", ") + " " + body + "\n")
	}
	return b.String()
}

// Slug turns a filename into an anchor-safe slug, as GitHub does for
// #file-<slug>.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// linkRel marks links in Markdown as user content that search engines
// shouldn't follow.
type linkRel struct{}

func (linkRel) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Link, *ast.AutoLink:
			n.SetAttributeString("rel", []byte("nofollow ugc noopener"))
		}
		return ast.WalkContinue, nil
	})
}
