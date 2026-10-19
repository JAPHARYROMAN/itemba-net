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

// runSteps performs the driver's pre-search steps in order, adding captured
// variables to vars. A step whose captures are cached and fresh is skipped.
// Any failure stops the search: a host whose configuration cannot be
// confirmed is not searched with guessed values.
func (d *Driver) runSteps(ctx context.Context, fetcher *Fetcher, vars map[string]string) error {
	for i := range d.Spec.Steps {
		step := &d.Spec.Steps[i]
		if cached, ok := d.cachedStep(i); ok {
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
			d.stepCache[i] = cachedStep{vars: captured, expires: time.Now().Add(time.Duration(step.CacheSeconds) * time.Second)}
			d.mu.Unlock()
		}
	}
	return nil
}

func (d *Driver) cachedStep(index int) (map[string]string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cached, ok := d.stepCache[index]
	if !ok || time.Now().After(cached.expires) {
		return nil, false
	}
	return cached.vars, true
}

// ClearStepCache forgets cached step captures, for tests and for an
// operator who knows a host changed.
func (d *Driver) ClearStepCache() {
	d.mu.Lock()
	d.stepCache = map[int]cachedStep{}
	d.mu.Unlock()
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

	var doc *xhtml.Node
	var decoded any
	switch step.Response {
	case "html":
		if doc, err = xhtml.Parse(strings.NewReader(text)); err != nil {
			return nil, fmt.Errorf("crawler: %s step %s: response is not HTML", d.Name, label)
		}
	case "json":
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("crawler: %s step %s: response is not JSON", d.Name, label)
		}
	}

	captured := map[string]string{}
	names := make([]string, 0, len(step.Capture))
	for name := range step.Capture {
		names = append(names, name)
	}
	// Captures that derive from other variables run after the rest.
	sort.Slice(names, func(i, j int) bool {
		a, b := step.Capture[names[i]].From != "", step.Capture[names[j]].From != ""
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
		capture := step.Capture[name]
		var candidates []string
		switch {
		case capture.From != "":
			for _, match := range capture.regex.FindAllStringSubmatch(all(capture.From), -1) {
				candidates = append(candidates, match[capture.Group])
			}
		case capture.sel != nil:
			for _, node := range capture.sel.MatchAll(doc) {
				value := nodeText(node)
				if capture.Attr != "" {
					value = attribute(node, capture.Attr)
					if value != "" && capture.Resolve {
						if ref, err := url.Parse(value); err == nil {
							value = final.ResolveReference(ref).String()
						}
					}
				}
				candidates = append(candidates, value)
			}
		case capture.Path != "":
			candidates = append(candidates, scalarString(lookup(decoded, capture.Path)))
		case capture.regex != nil:
			for _, match := range capture.regex.FindAllStringSubmatch(text, -1) {
				candidates = append(candidates, match[capture.Group])
			}
		}
		var kept []string
		for _, value := range candidates {
			value = strings.TrimSpace(value)
			if value == "" || (capture.match != nil && !capture.match.MatchString(value)) {
				continue
			}
			kept = append(kept, value)
		}
		switch {
		case len(kept) == 0 && capture.Default != "":
			captured[name] = capture.Default
		case len(kept) == 0:
			return nil, fmt.Errorf("crawler: %s step %s: %s was not found", d.Name, label, name)
		case capture.Unique && len(kept) > 1:
			return nil, fmt.Errorf("crawler: %s step %s: %s is ambiguous", d.Name, label, name)
		default:
			captured[name] = kept[0]
		}
	}
	for name, template := range step.Assert {
		if all(name) != expand(template, all) {
			return nil, fmt.Errorf("crawler: %s step %s: %s does not agree with the rest of the configuration", d.Name, label, name)
		}
	}
	return captured, nil
}
