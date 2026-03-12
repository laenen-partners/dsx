package codeview

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Highlight takes source code and a language name and returns syntax-highlighted HTML.
// Uses Chroma for tokenization with DaisyUI color variables as inline styles.
// Supported languages: "go", "templ", "html", "js", "css", "sql", "json", "yaml", "sh".
func Highlight(code, lang string) string {
	if lang == "templ" {
		return highlightTempl(code)
	}

	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return escapeHTML(code)
	}

	var b strings.Builder
	for _, token := range iterator.Tokens() {
		text := escapeHTML(token.Value)
		color := tokenColor(token.Type)
		if color != "" {
			fmt.Fprintf(&b, `<span style="color:%s">%s</span>`, color, text)
		} else {
			b.WriteString(text)
		}
	}
	return b.String()
}

// templToken matches ordered from highest to lowest priority.
var templTokens = regexp.MustCompile(
	`(?m)` +
		`//[^\n]*` + // line comments
		`|` + "`[^`]*`" + // backtick strings
		"|" + `"[^"]*"` + // double-quoted strings
		`|@[\w]+(?:\.[\w]+)*` + // @component.Call
		`|\b(?:package|import|func|return|if|else|for|range|switch|case|default|var|const|type|struct|interface|map|chan|go|defer|select|break|continue|fallthrough|templ)\b` + // keywords
		`|\b(?:true|false|nil)\b` + // boolean/nil literals
		`|\b\d+(?:\.\d+)?\b` + // numbers
		`|\b[A-Z]\w*\b`, // capitalized identifiers (types)
)

// highlightTempl applies regex-based highlighting for templ syntax.
// Uses a single-pass find-all approach on raw code (before HTML escaping)
// to avoid placeholder corruption issues.
func highlightTempl(code string) string {
	matches := templTokens.FindAllStringIndex(code, -1)
	if len(matches) == 0 {
		return escapeHTML(code)
	}

	var b strings.Builder
	prev := 0
	for _, loc := range matches {
		// Write unhighlighted text between matches
		if loc[0] > prev {
			b.WriteString(escapeHTML(code[prev:loc[0]]))
		}
		token := code[loc[0]:loc[1]]
		color := templTokenColor(token)
		if color != "" {
			fmt.Fprintf(&b, `<span style="color:%s">%s</span>`, color, escapeHTML(token))
		} else {
			b.WriteString(escapeHTML(token))
		}
		prev = loc[1]
	}
	// Write remaining text after last match
	if prev < len(code) {
		b.WriteString(escapeHTML(code[prev:]))
	}
	return b.String()
}

var (
	templKeywordRe = regexp.MustCompile(`^(?:package|import|func|return|if|else|for|range|switch|case|default|var|const|type|struct|interface|map|chan|go|defer|select|break|continue|fallthrough|templ)$`)
	templBoolRe    = regexp.MustCompile(`^(?:true|false|nil)$`)
	templNumberRe  = regexp.MustCompile(`^\d`)
	templTypeRe    = regexp.MustCompile(`^[A-Z]`)
)

func templTokenColor(token string) string {
	switch {
	case strings.HasPrefix(token, "//"):
		return "color-mix(in oklab, var(--color-base-content) 50%, transparent)"
	case strings.HasPrefix(token, "`") || strings.HasPrefix(token, `"`):
		return "var(--color-success)"
	case strings.HasPrefix(token, "@"):
		return "var(--color-secondary)"
	case templKeywordRe.MatchString(token):
		return "var(--color-primary)"
	case templBoolRe.MatchString(token):
		return "var(--color-info)"
	case templNumberRe.MatchString(token):
		return "var(--color-info)"
	case templTypeRe.MatchString(token):
		return "var(--color-warning)"
	default:
		return ""
	}
}

// tokenColor returns a CSS color value using DaisyUI CSS variables.
// We use inline styles instead of Tailwind classes because the highlighted
// HTML is injected via templ.Raw() and invisible to Tailwind's purger.
func tokenColor(t chroma.TokenType) string {
	switch t {
	case chroma.Keyword:
	case chroma.KeywordDeclaration:
	case chroma.KeywordNamespace:
	case chroma.KeywordType:
	case chroma.KeywordReserved:
	case chroma.KeywordConstant:
		return "var(--color-primary)"
	case chroma.NameBuiltin:
	case chroma.NameBuiltinPseudo:
		return "var(--color-primary)"
	case chroma.LiteralString:
	case chroma.LiteralStringDouble:
	case chroma.LiteralStringSingle:
	case chroma.LiteralStringBacktick:
	case chroma.LiteralStringChar:
	case chroma.LiteralStringEscape:
	case chroma.LiteralStringInterpol:
	case chroma.LiteralStringAffix:
		return "var(--color-success)"
	case chroma.Comment:
	case chroma.CommentSingle:
	case chroma.CommentMultiline:
	case chroma.CommentPreproc:
	case chroma.CommentSpecial:
		return "color-mix(in oklab, var(--color-base-content) 50%, transparent)"
	case chroma.LiteralNumber:
	case chroma.LiteralNumberFloat:
	case chroma.LiteralNumberHex:
	case chroma.LiteralNumberInteger:
	case chroma.LiteralNumberOct:
	case chroma.LiteralNumberBin:
		return "var(--color-info)"
	case chroma.NameFunction:
	case chroma.NameFunctionMagic:
	case chroma.NameOther:
		return "var(--color-secondary)"
	case chroma.Name:
		return "var(--color-secondary)"
	case chroma.NameClass:
	case chroma.NameException:
	case chroma.NameDecorator:
		return "var(--color-warning)"
	case chroma.Operator:
	case chroma.OperatorWord:
		return "var(--color-primary)"
	case chroma.Punctuation:
		return "color-mix(in oklab, var(--color-base-content) 70%, transparent)"
	default:
		return ""
	}

	return ""
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
