package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

// runSteps performs steps in order, adding captured variables to vars. A
// step whose captures are cached and fresh is skipped; cacheKey keeps the
// search's steps and each feed's apart. Any failure stops the caller: a host
// whose configuration cannot be confirmed is not asked with guessed values.
func (d *Driver) runSteps(ctx context.Context, fetcher *Fetcher, steps []StepSpec, cacheKey string, vars map[string]string) error {
	for i := range steps {
		step := &steps[i]
		key := fmt.Sprintf("%s:%d", cacheKey, i)
		if cached, ok := d.cachedStep(key); ok {
			for name, value := range cached {
				vars[name] = value
			}
			continue
		}
		captured, err := d.runStep(ctx, fetcher, step, vars)
		if err != nil {
			return err
		}
		for name, value := range captured {
			vars[name] = value
		}
		if step.CacheSeconds > 0 {
			d.mu.Lock()
			d.stepCache[key] = cachedStep{vars: captured, expires: time.Now().Add(time.Duration(step.CacheSeconds) * time.Second)}
			d.mu.Unlock()
		}
	}
	return nil
}

func (d *Driver) cachedStep(key string) (map[string]string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cached, ok := d.stepCache[key]
	if !ok || time.Now().After(cached.expires) {
		return nil, false
	}
	return cached.vars, true
}

// ClearStepCache forgets cached step captures, for tests and for an
// operator who knows a host changed.
func (d *Driver) ClearStepCache() {
	d.mu.Lock()
	d.stepCache = map[string]cachedStep{}
	d.mu.Unlock()
}

// fetched is one response parsed per its declared format, for steps and
// checks alike. parsed is false when the body is not what was declared.
type fetched struct {
	url    *url.URL
	status int
	body   []byte
	text   string
	html   *xhtml.Node
	json   any
	parsed bool
}

func parseFetched(response string, body []byte, final *url.URL, status int) *fetched {
	doc := &fetched{url: final, status: status, body: body, text: string(body), parsed: true}
	switch response {
	case "html":
		parsed, err := xhtml.Parse(strings.NewReader(doc.text))
		if err != nil {
			doc.parsed = false
		} else {
			doc.html = parsed
		}
	case "json":
		if err := json.Unmarshal(body, &doc.json); err != nil {
			doc.parsed = false
		}
	}
	return doc
}

func (d *Driver) runStep(ctx context.Context, fetcher *Fetcher, step *StepSpec, vars map[string]string) (map[string]string, error) {
	resolve := func(name string) string { return vars[name] }
	label := step.Name
	if label == "" {
		label = step.URL
	}
	target := expand(step.URL, resolve)
	if len(step.Params) > 0 {
		values := url.Values{}
		for key, template := range step.Params {
			values.Set(key, expand(template, resolve))
		}
		target += "?" + values.Encode()
	}
	headers := map[string]string{}
	for key, template := range step.Headers {
		headers[key] = expand(template, resolve)
	}
	body, final, err := d.fetch(ctx, fetcher, target, headers, step.SameURL, step.NoRedirect)
	if err != nil {
		return nil, fmt.Errorf("%s step %s: %w", d.Name, label, err)
	}
	text := string(body)
	for _, required := range step.RequireText {
		if !strings.Contains(text, required) {
			return nil, fmt.Errorf("crawler: %s step %s: the response no longer carries %q", d.Name, label, required)
		}
	}
	doc := parseFetched(step.Response, body, final, 200)
	if !doc.parsed {
		return nil, fmt.Errorf("crawler: %s step %s: response is not %s", d.Name, label, strings.ToUpper(step.Response))
	}
	captured, problems := readCaptures(step.Capture, doc, vars)
	if len(problems) > 0 {
		names := make([]string, 0, len(problems))
		for name := range problems {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("crawler: %s step %s: %s %s", d.Name, label, names[0], problems[names[0]])
	}
	all := func(name string) string {
		if v, ok := captured[name]; ok {
			return v
		}
		return vars[name]
	}
	for name, template := range step.Assert {
		if all(name) != expand(template, all) {
			return nil, fmt.Errorf("crawler: %s step %s: %s does not agree with the rest of the configuration", d.Name, label, name)
		}
	}
	return captured, nil
}

// readCaptures evaluates captures against a response. It returns the
// variables found and, per capture that could not be settled, why: a step
// treats any problem as fatal, a check leaves the variable absent and lets
// its rules decide what that means.
func readCaptures(specs map[string]CaptureSpec, doc *fetched, vars map[string]string) (map[string]string, map[string]string) {
	captured := map[string]string{}
	problems := map[string]string{}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	// Captures that derive from other variables run after the rest.
	sort.Slice(names, func(i, j int) bool {
		a, b := specs[names[i]].From != "", specs[names[j]].From != ""
		if a != b {
			return !a
		}
		return names[i] < names[j]
	})
	all := func(name string) string {
		if v, ok := captured[name]; ok {
			return v
		}
		return vars[name]
	}
	for _, name := range names {
		capture := specs[name]
		var kept []string
		for _, value := range capture.candidates(doc, all) {
			value = strings.TrimSpace(transform(capture.Transform, value))
			if value == "" || (capture.match != nil && !capture.match.MatchString(value)) {
				continue
			}
			kept = append(kept, value)
		}
		switch {
		case len(kept) == 0 && capture.Default != "":
			captured[name] = capture.Default
		case len(kept) == 0:
			problems[name] = "was not found"
		case capture.Unique && len(kept) > 1:
			problems[name] = "is ambiguous"
		default:
			captured[name] = kept[0]
		}
	}
	return captured, problems
}

// candidates reads a capture's raw candidates from the response or from an
// earlier variable, before transforms and the match filter.
func (c CaptureSpec) candidates(doc *fetched, all func(string) string) []string {
	switch {
	case c.From != "" && (c.Path != "" || len(c.Paths) > 0):
		var decoded any
		if json.Unmarshal([]byte(all(c.From)), &decoded) != nil {
			return nil
		}
		return c.pathCandidates(decoded)
	case c.From != "":
		return c.regexCandidates(all(c.From))
	case c.sel != nil:
		if doc.html == nil {
			return nil
		}
		var out []string
		for _, node := range c.sel.MatchAll(doc.html) {
			if c.Visible && !visibleNode(node) {
				continue
			}
			switch {
			case c.regex != nil:
				out = append(out, c.regexCandidates(rawText(node))...)
			case c.Attr != "":
				value := attribute(node, c.Attr)
				if value != "" && c.Resolve && doc.url != nil {
					if ref, err := url.Parse(value); err == nil {
						value = doc.url.ResolveReference(ref).String()
					}
				}
				out = append(out, value)
			case c.Visible:
				out = append(out, VisibleText(node))
			default:
				out = append(out, nodeText(node))
			}
		}
		return out
	case c.Path != "" || len(c.Paths) > 0:
		if doc.json == nil {
			return nil
		}
		return c.pathCandidates(doc.json)
	case c.regex != nil:
		return c.regexCandidates(doc.text)
	}
	return nil
}

func (c CaptureSpec) pathCandidates(document any) []string {
	paths := c.Paths
	if c.Path != "" {
		paths = []string{c.Path}
	}
	for _, path := range paths {
		if out := valueCandidates(lookup(document, path), c.JSON); len(out) > 0 {
			return out
		}
	}
	return nil
}

func (c CaptureSpec) regexCandidates(text string) []string {
	var out []string
	if c.JSON {
		// The JSON value that follows each match, as the page's own script
		// assigned it; anything after the value (a semicolon, more script)
		// is left alone.
		for _, loc := range c.regex.FindAllStringIndex(text, -1) {
			var value any
			if json.NewDecoder(strings.NewReader(text[loc[1]:])).Decode(&value) != nil || value == nil {
				continue
			}
			if encoded, err := json.Marshal(value); err == nil {
				out = append(out, string(encoded))
			}
		}
		return out
	}
	for _, match := range c.regex.FindAllStringSubmatch(text, -1) {
		out = append(out, match[c.Group])
	}
	return out
}

// valueCandidates renders a looked-up JSON value as capture candidates: a
// scalar is one candidate, a list is one per element, and with keepJSON an
// object or nested list is its compact JSON text for a later capture to
// read by path.
func valueCandidates(value any, keepJSON bool) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, element := range v {
			switch element.(type) {
			case map[string]any, []any:
				if keepJSON {
					if encoded, err := json.Marshal(element); err == nil {
						out = append(out, string(encoded))
					}
				}
			default:
				if s := scalarString(element); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case map[string]any:
		if keepJSON {
			if encoded, err := json.Marshal(v); err == nil {
				return []string{string(encoded)}
			}
		}
		return nil
	}
	if s := scalarString(value); s != "" {
		return []string{s}
	}
	return nil
}
