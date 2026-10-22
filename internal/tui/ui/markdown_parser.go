// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

func decodedMarkdown(s []byte) string {
	return sanitize.Display(string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(s)))))
}

func allowedLink(raw []byte) string {
	target := decodedMarkdown(raw)
	if strings.ContainsAny(target, "\n\r\t ") {
		return ""
	}
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return ""
		}
	case "mailto":
		if u.Opaque == "" {
			return ""
		}
	default:
		return ""
	}
	return target
}

// Goldmark also closes a fence node at EOF. Only Continue returning Close
// proves a matching fence was consumed (including inside a list or quote).
type closedFenceParser struct{ parser.BlockParser }

func (p closedFenceParser) Continue(node ast.Node, reader text.Reader, context parser.Context) parser.State {
	state := p.BlockParser.Continue(node, reader, context)
	if state == parser.Close {
		node.SetAttributeString("lina.closed", true)
	}
	return state
}

var markdownParser = goldmark.New(goldmark.WithParserOptions(parser.WithBlockParsers(
	// The default fenced-code parser has priority 700.
	util.Prioritized(closedFenceParser{parser.NewFencedCodeBlockParser()}, 699),
))).Parser()

func parseMarkdown(source, bullet string) []render.MarkdownBlock {
	data := []byte(source)
	doc := markdownParser.Parse(text.NewReader(data))
	var blocks []render.MarkdownBlock
	var inline func(ast.Node, render.InlineStyle) []render.Span
	inline = func(n ast.Node, style render.InlineStyle) []render.Span {
		switch node := n.(type) {
		case *ast.Text:
			value := string(node.Segment.Value(data))
			if !style.Code && !node.IsRaw() {
				value = decodedMarkdown([]byte(value))
			}
			if node.SoftLineBreak() || node.HardLineBreak() {
				value += "\n"
			}
			return []render.Span{{Text: value, Style: style}}
		case *ast.String:
			return []render.Span{{Text: sanitize.Display(string(node.Value)), Style: style}}
		case *ast.RawHTML:
			return []render.Span{{Text: sanitize.Display(string(node.Segments.Value(data))), Style: style}}
		case *ast.Emphasis:
			if node.Level == 2 {
				style.Bold = true
			} else {
				style.Italic = true
			}
		case *ast.CodeSpan:
			style.Code = true
		case *ast.Link:
			style.Link, style.URL = true, allowedLink(node.Destination)
		case *ast.AutoLink:
			style.Link, style.URL = true, allowedLink(node.URL(data))
			return []render.Span{{Text: decodedMarkdown(node.Label(data)), Style: style}}
		}
		var spans []render.Span
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			spans = append(spans, inline(child, style)...)
		}
		if _, ok := n.(*ast.CodeSpan); ok {
			for i := range spans {
				spans[i].Text = strings.ReplaceAll(spans[i].Text, "\n", " ")
			}
		}
		return spans
	}
	var visit func(ast.Node, string, int)
	visit = func(n ast.Node, prefix string, indent int) {
		switch node := n.(type) {
		case *ast.List:
			number := node.Start
			for item := node.FirstChild(); item != nil; item = item.NextSibling() {
				label := bullet + " "
				if node.IsOrdered() {
					label = strconv.Itoa(number) + ". "
					number++
				}
				first := true
				for child := item.FirstChild(); child != nil; child = child.NextSibling() {
					p := ""
					if first {
						p = label
					}
					childIndent := indent
					if !first {
						childIndent += (render.Unicode{}).Width(label)
					}
					visit(child, p, childIndent)
					first = false
				}
			}
		case *ast.HTMLBlock:
			var raw strings.Builder
			raw.Write(node.Lines().Value(data))
			if node.HasClosure() {
				raw.Write(node.ClosureLine.Value(data))
			}
			value := sanitize.Display(strings.TrimSuffix(raw.String(), "\n"))
			blocks = append(blocks, render.MarkdownBlock{Spans: []render.Span{{Text: value}}, Prefix: prefix, Indent: indent})
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			var code strings.Builder
			for i := 0; i < n.Lines().Len(); i++ {
				line := n.Lines().At(i)
				code.Write(line.Value(data))
			}
			value := strings.TrimSuffix(code.String(), "\n")
			spans := []render.Span{{Text: value, Style: render.InlineStyle{Code: true}}}
			if fence, ok := node.(*ast.FencedCodeBlock); ok {
				if closed, _ := fence.AttributeString("lina.closed"); closed == true {
					spans = highlightCode(value, string(fence.Language(data)))
				}
			}
			blocks = append(blocks, render.MarkdownBlock{Spans: spans, Prefix: prefix, Indent: indent, Code: true})
		case *ast.Paragraph, *ast.TextBlock, *ast.Heading:
			if n.FirstChild() == nil {
				return
			}
			_, heading := n.(*ast.Heading)
			blocks = append(blocks, render.MarkdownBlock{Spans: inline(n, render.InlineStyle{Bold: heading}), Prefix: prefix, Indent: indent})
		default:
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				visit(child, prefix, indent)
			}
		}
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		before := len(blocks)
		if before > 0 {
			blocks = append(blocks, render.MarkdownBlock{})
		}
		visit(n, "", 0)
		if before > 0 && len(blocks) == before+1 {
			blocks = blocks[:before]
		}
	}
	return blocks
}
