package crawler

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/itemba-net/core/internal/atlas"
)

// FeedSpec is a catalog the host publishes without a query: the shelves the
// console shows, such as what is popular or newly added. A feed reuses the
// search's steps, endpoint, headers, response format and item rules, and
// declares only what differs: its parameters, steps of its own, a limit, and
// fields the feed adds, such as the date the catalog lists an item under.
type FeedSpec struct {
	URL     string            `yaml:"url"`
	Params  map[string]string `yaml:"params"`
	Headers map[string]string `yaml:"headers"`
	// Steps run after the search's steps, which supply configuration and
	// any guest token, and before the feed request.
	Steps []StepSpec `yaml:"steps"`
	// ParamsFrom takes parameters from a URL the host itself handed out.
	ParamsFrom *ParamsFromSpec `yaml:"params_from"`
	// Fields add to or override the search's fields for this feed.
	Fields         map[string]FieldSpec `yaml:"fields"`
	Limit          int                  `yaml:"limit"`
	TimeoutSeconds int                  `yaml:"timeout_seconds"`
}

// ParamsFromSpec reads a feed's parameters from a URL captured by a step,
// under an allowlist: every query key must be allowed, with its value
// matching the key's pattern, or dropped; the required keys must be present.
// Identifiers the host put in the URL are dropped and replaced by the feed's
// own fresh ones, so a returned feed can never introduce a session, device,
// location or another catalog's identity.
type ParamsFromSpec struct {
	Var     string            `yaml:"var"`
	Allow   map[string]string `yaml:"allow"`
	Drop    []string          `yaml:"drop"`
	Require []string          `yaml:"require"`
	allow   map[string]*regexp.Regexp
	drop    map[string]bool
}

var feedName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (f *FeedSpec) validate(search *SearchSpec) error {
	if f.URL != "" {
		probe := expand(f.URL, func(string) string { return "x" })
		if u, err := url.Parse(probe); err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" {
			return fmt.Errorf("url %q must be an https URL without a query string; put the query in params", f.URL)
		}
	}
	for i := range f.Steps {
		if err := f.Steps[i].validate(); err != nil {
			return fmt.Errorf("steps[%d]: %w", i, err)
		}
	}
	if f.ParamsFrom != nil {
		if err := f.ParamsFrom.validate(); err != nil {
			return fmt.Errorf("params_from: %w", err)
		}
		captured := false
		for _, steps := range [][]StepSpec{search.Steps, f.Steps} {
			for _, step := range steps {
				if _, ok := step.Capture[f.ParamsFrom.Var]; ok {
					captured = true
				}
			}
		}
		if !captured {
			return fmt.Errorf("params_from.var %q is not captured by any step", f.ParamsFrom.Var)
		}
	}
	for key, spec := range f.Fields {
		if !listingKeys[key] {
			return fmt.Errorf("fields.%s is not a listing field", key)
		}
		if err := spec.validate(search.Response); err != nil {
			return fmt.Errorf("fields.%s: %w", key, err)
		}
		f.Fields[key] = spec
	}
	if f.Limit <= 0 {
		f.Limit = search.Limit
	}
	if f.TimeoutSeconds <= 0 {
		f.TimeoutSeconds = search.TimeoutSeconds
	}
	return nil
}

func (p *ParamsFromSpec) validate() error {
	if p.Var == "" {
		return errors.New("var is required")
	}
	if len(p.Allow) == 0 {
		return errors.New("allow needs at least one parameter pattern")
	}
	p.allow = map[string]*regexp.Regexp{}
	for key, pattern := range p.Allow {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("allow.%s: %w", key, err)
		}
		p.allow[key] = re
	}
	p.drop = map[string]bool{}
	for _, key := range p.Drop {
		p.drop[key] = true
	}
	for _, key := range p.Require {
		if _, ok := p.Allow[key]; !ok {
			return fmt.Errorf("require names %q, which is not allowed", key)
		}
	}
	return nil
}

// read validates the captured URL's query against the allowlist and returns
// the parameters to send. Errors name a key, never a value.
func (p *ParamsFromSpec) read(vars map[string]string) (map[string]string, error) {
	u, err := url.Parse(vars[p.Var])
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%s is not an https URL", p.Var)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) == 0 {
		return nil, fmt.Errorf("%s carries no usable query", p.Var)
	}
	out := map[string]string{}
	for key, entries := range query {
		if len(entries) != 1 || len(entries[0]) > 160 || strings.ContainsAny(entries[0], "\r\n\x00") {
			return nil, fmt.Errorf("%s repeats or overfills parameter %s", p.Var, key)
		}
		// A key may be both allowed and dropped: its value is then checked
		// but not forwarded, as with an identity field that must be empty.
		re, allowed := p.allow[key]
		if allowed && !re.MatchString(entries[0]) || !allowed && !p.drop[key] {
			return nil, fmt.Errorf("%s carries parameter %s, which the driver does not allow", p.Var, key)
		}
		if p.drop[key] {
			continue
		}
		out[key] = entries[0]
	}
	for _, key := range p.Require {
		if _, ok := out[key]; !ok {
			return nil, fmt.Errorf("%s lacks parameter %s", p.Var, key)
		}
	}
	return out, nil
}

// HasFeed reports whether the driver declares the named feed.
func (d *Driver) HasFeed(name string) bool {
	_, ok := d.Feeds[name]
	return ok
}

// FeedNames lists the driver's feeds, sorted.
func (d *Driver) FeedNames() []string {
	names := make([]string, 0, len(d.Feeds))
	for name := range d.Feeds {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Feed reads one of the host's catalogs: the search's steps first, then the
// feed's own, then the feed request, read with the search's item rules.
func (d *Driver) Feed(ctx context.Context, fetcher *Fetcher, name string) ([]atlas.Listing, error) {
	feed, ok := d.Feeds[name]
	if !ok || d.Spec == nil {
		return nil, fmt.Errorf("crawler: driver %s has no %s feed", d.Name, name)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(feed.TimeoutSeconds)*time.Second)
	defer cancel()
	vars := searchVars("", "")
	if err := d.runSteps(ctx, fetcher, d.Spec.Steps, "search", vars); err != nil {
		return nil, err
	}
	if err := d.runSteps(ctx, fetcher, feed.Steps, "feed:"+name, vars); err != nil {
		return nil, err
	}
	resolve := func(name string) string { return vars[name] }
	params := map[string]string{}
	if feed.ParamsFrom != nil {
		passed, err := feed.ParamsFrom.read(vars)
		if err != nil {
			return nil, fmt.Errorf("crawler: %s feed %s: %w", d.Name, name, err)
		}
		for key, value := range passed {
			params[key] = value
		}
	}
	// The feed's own parameters win: fresh identifiers replace any the
	// host's URL carried.
	for key, template := range feed.Params {
		params[key] = expand(template, resolve)
	}
	target := expand(firstNonEmpty(feed.URL, d.Spec.URL), resolve)
	if len(params) > 0 {
		values := url.Values{}
		for key, value := range params {
			values.Set(key, value)
		}
		target += "?" + values.Encode()
	}
	headers := feed.Headers
	if headers == nil {
		headers = d.Spec.Headers
	}
	doc, err := d.fetchDocumentWith(ctx, fetcher, target, headers, vars)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]FieldSpec, len(d.Spec.Fields)+len(feed.Fields))
	for key, spec := range d.Spec.Fields {
		fields[key] = spec
	}
	for key, spec := range feed.Fields {
		fields[key] = spec
	}
	seen := map[string]bool{}
	var listings []atlas.Listing
	for _, raw := range doc.items(d.Spec.Items) {
		if len(listings) >= feed.Limit {
			break
		}
		it := item{page: doc.url, vars: vars}
		switch v := raw.(type) {
		case map[string]any:
			it.json = v
		case *xhtml.Node:
			it.node = v
		default:
			continue
		}
		if !d.itemAllowed(it) {
			continue
		}
		listing, ok := d.listingFrom(it, "", fields)
		if !ok || seen[listing.URL] {
			continue
		}
		seen[listing.URL] = true
		listings = append(listings, listing)
	}
	return listings, nil
}
