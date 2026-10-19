package crawler

import (
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// attribute returns an element's attribute value, matching the name without
// regard to case, or an empty string.
func attribute(node *html.Node, key string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

// nodeText is the visible text of an element: its text nodes joined with
// whitespace collapsed, skipping scripts and styles.
func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			b.WriteByte(' ')
		case html.ElementNode:
			if n.Data == "script" || n.Data == "style" {
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(b.String()), " ")
}

// closest returns the nearest ancestor-or-self element matching the selector.
func closest(node *html.Node, sel cascadia.Selector) *html.Node {
	for n := node; n != nil; n = n.Parent {
		if n.Type == html.ElementNode && sel.Match(n) {
			return n
		}
	}
	return nil
}

// hasAncestorOrSelf reports whether the node or an ancestor matches.
func hasAncestorOrSelf(node *html.Node, sel cascadia.Selector) bool {
	return closest(node, sel) != nil
}

// hasAncestor reports whether a strict ancestor matches.
func hasAncestor(node *html.Node, sel cascadia.Selector) bool {
	if node == nil {
		return false
	}
	return closest(node.Parent, sel) != nil
}
