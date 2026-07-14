package insightlinks

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const prefix = "[[insight:"

// KindInsightLink identifies an insight-link inline node in a Goldmark AST.
var KindInsightLink = ast.NewNodeKind("InsightLink")

// InsightLink is the authoritative parsed representation of an insight link.
type InsightLink struct {
	ast.BaseInline
	TargetKey string
	Label     string
}

func (n *InsightLink) Dump(_ []byte, level int) {
	fmt.Printf("%sInsightLink: target=%q label=%q\n", strings.Repeat("    ", level), n.TargetKey, n.Label)
}

func (n *InsightLink) Kind() ast.NodeKind {
	return KindInsightLink
}

type extension struct{}

func (extension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(insightLinkParser{}, 100),
	))
}

type insightLinkParser struct{}

func (insightLinkParser) Trigger() []byte {
	return []byte{'['}
}

func (insightLinkParser) Parse(_ ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if pc.IsInLinkLabel() {
		return nil
	}

	line, segment := block.PeekLine()
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return nil
	}

	remainder := line[len(prefix):]
	closing := bytes.Index(remainder, []byte("]]"))
	if closing < 0 {
		length := len(line)
		if length > 0 && line[length-1] == '\n' {
			length--
			if length > 0 && line[length-1] == '\r' {
				length--
			}
		}
		block.Advance(length)
		return ast.NewTextSegment(segment.WithStop(segment.Start + length))
	}

	if invalid := bytes.IndexByte(remainder[:closing], ']'); invalid >= 0 {
		length := len(prefix) + invalid + 1
		block.Advance(length)
		return ast.NewTextSegment(segment.WithStop(segment.Start + length))
	}

	body := remainder[:closing]
	target, label, _ := bytes.Cut(body, []byte{'|'})
	target = trimHorizontalSpace(target)
	label = trimHorizontalSpace(label)
	if len(target) == 0 || bytes.ContainsAny(target, "|]\r\n") || bytes.ContainsAny(label, "]\r\n") {
		length := len(prefix) + closing + 2
		block.Advance(length)
		return ast.NewTextSegment(segment.WithStop(segment.Start + length))
	}
	if len(label) == 0 {
		label = target
	}

	block.Advance(len(prefix) + closing + 2)
	return &InsightLink{TargetKey: string(target), Label: string(label)}
}

func trimHorizontalSpace(value []byte) []byte {
	return bytes.Trim(value, " \t")
}

var markdown = goldmark.New(goldmark.WithExtensions(extension{}))

// Parse builds the Starlogz Goldmark AST for content.
func Parse(source []byte) ast.Node {
	return markdown.Parser().Parse(text.NewReader(source))
}

// Targets returns unique insight-link targets in C-collation order.
func Targets(content string) []string {
	seen := make(map[string]struct{})
	_ = ast.Walk(Parse([]byte(content)), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && node.Kind() == KindInsightLink {
			seen[node.(*InsightLink).TargetKey] = struct{}{}
		}
		return ast.WalkContinue, nil
	})

	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}
