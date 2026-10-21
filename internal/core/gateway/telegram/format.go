// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// As in Hermes, protect code before interpreting prose and retain a plain-text
// fallback. Goldmark supplies the Markdown parser; native Telegram entities
// avoid a second Markdown parser and its escaping rules on the wire. Offsets
// refer to the rendered UTF-16 text, never source bytes. See THIRD-PARTY-NOTICES.
var markdownParser = goldmark.New(goldmark.WithExtensions(extension.Strikethrough)).Parser()

func units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func formatMessage(source string) (string, []gotgbot.MessageEntity) {
	input := []byte(source)
	root := markdownParser.Parse(text.NewReader(input))
	var out strings.Builder
	var entities []gotgbot.MessageEntity
	offset := 0
	write := func(s string) { out.WriteString(s); offset += units(s) }
	decode := func(b []byte) string {
		return string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(b))))
	}
	add := func(kind string, start int, link, language string) {
		if start == offset {
			return
		}
		for _, e := range entities {
			// Telegram forbids formatting containing code and nested quotes.
			if int(e.Offset) >= start && (e.Type == "code" || e.Type == "pre" || e.Type == kind) {
				return
			}
		}
		entities = append(entities, gotgbot.MessageEntity{Type: kind, Offset: int64(start), Length: int64(offset - start), Url: link, Language: language})
	}
	var render func(ast.Node)
	children := func(n ast.Node, separator string) {
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if child != n.FirstChild() {
				write(separator)
			}
			render(child)
		}
	}
	render = func(n ast.Node) {
		start := offset
		switch n := n.(type) {
		case *ast.Text:
			write(decode(n.Segment.Value(input)))
			if n.SoftLineBreak() || n.HardLineBreak() {
				write("\n")
			}
		case *ast.String:
			write(string(n.Value))
		case *ast.FencedCodeBlock:
			write(string(n.Lines().Value(input)))
			add("pre", start, "", string(n.Language(input)))
		case *ast.CodeBlock:
			write(string(n.Lines().Value(input)))
			add("pre", start, "", "")
		case *ast.CodeSpan:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				if t, ok := child.(*ast.Text); ok {
					write(strings.ReplaceAll(string(t.Segment.Value(input)), "\n", " "))
				}
			}
			add("code", start, "", "")
		case *ast.Heading:
			children(n, "")
			add("bold", start, "", "")
		case *ast.Emphasis:
			children(n, "")
			kind := "italic"
			if n.Level == 2 {
				kind = "bold"
			}
			add(kind, start, "", "")
		case *extast.Strikethrough:
			children(n, "")
			add("strikethrough", start, "", "")
		case *ast.Link:
			children(n, "")
			link := decode(n.Destination)
			if u, err := url.Parse(link); err == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "mailto") {
				add("text_link", start, link, "")
			} else {
				write(" (" + link + ")")
			}
		case *ast.Image:
			children(n, "")
			write(" (" + decode(n.Destination) + ")")
		case *ast.AutoLink:
			write(string(n.Label(input))) // Telegram recognizes visible URLs itself.
		case *ast.RawHTML:
			write(string(n.Segments.Value(input))) // Never interpret model HTML.
		case *ast.HTMLBlock:
			write(string(n.Lines().Value(input)))
			if n.HasClosure() {
				write(string(n.ClosureLine.Value(input)))
			}
		case *ast.Blockquote:
			children(n, "\n\n")
			add("blockquote", start, "", "")
		case *ast.List:
			index := n.Start
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				if child != n.FirstChild() {
					write("\n")
				}
				if n.IsOrdered() {
					write(strconv.Itoa(index) + ". ")
					index++
				} else {
					write("• ")
				}
				render(child)
			}
		case *ast.Document, *ast.ListItem:
			children(n, "\n\n")
		case *ast.ThematicBreak:
			write("---")
		default:
			children(n, "")
		}
	}
	render(root)
	if len(entities) == 0 || !textFits(out.String(), 4096) {
		return source, nil
	}
	return out.String(), entities
}
