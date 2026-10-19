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

// rawText is every text node under an element, scripts included, for a
// capture that reads a script's contents.
func rawText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return b.String()
}

// IgnoredNode reports whether a node lies outside a page's primary visible
// content: comments, scripts, styles, templates, SVG, navigation, footers,
// asides, closed details and dialogs, hidden elements, and sections marked
// as FAQs, recommendations or trailers. The search pipeline and the drivers
// share this one judgment of what a visitor would actually read, so a
// provider's "not available" heading counts only where a visitor sees it.
func IgnoredNode(node *html.Node) bool {
	if node.Type == html.CommentNode {
		return true
	}
	if node.Type != html.ElementNode {
		return false
	}
	switch node.Data {
	case "script", "style", "noscript", "template", "svg", "nav", "footer", "aside", "details":
		return true
	}
	if node.Data == "dialog" {
		opened := false
		for _, attr := range node.Attr {
			opened = opened || attr.Key == "open"
		}
		if !opened {
			return true
		}
	}
	for _, attr := range node.Attr {
		key, value := strings.ToLower(attr.Key), strings.ToLower(attr.Val)
		if key == "hidden" || key == "aria-hidden" && value == "true" || key == "style" && (strings.Contains(strings.ReplaceAll(value, " ", ""), "display:none") || strings.Contains(strings.ReplaceAll(value, " ", ""), "visibility:hidden")) {
			return true
		}
		if key == "role" && (value == "navigation" || value == "complementary" || value == "contentinfo") {
			return true
		}
		if key == "class" || key == "id" || key == "aria-label" {
			value = strings.NewReplacer("_", " ", "-", " ").Replace(value)
			for _, word := range strings.Fields(value) {
				switch word {
				case "hidden", "invisible", "faq", "faqs", "recommendation", "recommendations", "recommended", "related", "similar", "suggestions", "trailer", "trailers", "teaser", "teasers":
					return true
				}
			}
		}
	}
	// Some sites mark a recommendation/FAQ section only with its heading.
	if node.Data == "section" || node.Data == "div" {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.TextNode && strings.TrimSpace(child.Data) == "" {
				continue
			}
			if child.Type == html.ElementNode && (child.Data == "h2" || child.Data == "h3") {
				label := strings.ToLower(VisibleText(child))
				for _, phrase := range []string{"frequently asked", "faqs", "more like this", "you may also", "recommended", "related movies", "similar movies", "trailers"} {
					if strings.Contains(label, phrase) {
						return true
					}
				}
			}
			break
		}
	}
	return false
}

// VisibleText is the text of a node with ignored descendants left out and
// whitespace collapsed.
func VisibleText(node *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if IgnoredNode(n) {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(text.String()), " ")
}

// visibleNode reports whether a node and all of its ancestors are part of
// the page's primary visible content.
func visibleNode(node *html.Node) bool {
	for n := node; n != nil; n = n.Parent {
		if IgnoredNode(n) {
			return false
		}
	}
	return true
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
