// Package crawler is Scout's autonomous side: a persistent frontier of URLs
// the system owes a fetch, host drivers that know how to search a host and
// read its listings, fetch tiers that cost more as hosts defend themselves,
// and the loop that keeps Atlas fresh without an operator typing queries.
package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/andybalholm/cascadia"
	xhtml "golang.org/x/net/html"
	"gopkg.in/yaml.v3"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// Driver is a declarative description of how to search one host and read
// its listings, and, where the host has them, how to read its catalog feeds
// and check access to one of its movie pages. Drivers are data files under
// drivers/, loaded at startup.
type Driver struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	// Kind is the object type the driver lists, one of Lens's types. The
	// search fans a query out to the drivers of its kind and the crawler
	// seeds a driver only with objects of its kind. Default video.
	Kind  string      `yaml:"kind"`
	Hosts []string    `yaml:"hosts"`
	Rate  Rate        `yaml:"rate"`
	Spec  *SearchSpec `yaml:"search"`
	// Feeds are the host's catalogs without a query, such as the shelves the
	// console shows; they share the search's steps and item rules.
	Feeds map[string]*FeedSpec `yaml:"feeds"`
	// Check is the host's access check for one of its movie pages, run by
	// the search pipeline before any playback sample.
	Check *CheckSpec `yaml:"check"`
	Path  string     `yaml:"-"`

	// stepCache holds captured variables of cacheable steps, for example a
	// host's public configuration, so every search need not refetch them.
	// Keys name the step list and index, so a feed's steps and the search's
	// stay apart.
	mu        sync.Mutex
	stepCache map[string]cachedStep
}

// Rate bounds how hard the crawler may lean on the driver's hosts.
type Rate struct {
	PerMinute   int `yaml:"per_minute"`
	Concurrency int `yaml:"concurrency"`
}

// SearchSpec describes a host's search endpoint and how to read its items.
// The endpoint answers in JSON or HTML; Steps run first when the host needs
// configuration or a guest token before it will answer.
type SearchSpec struct {
	// URL is the search endpoint. It may carry {query}, {market} and step
	// placeholders in its path; query parameters go in Params.
	URL          string            `yaml:"url"`
	Params       map[string]string `yaml:"params"`
	Headers      map[string]string `yaml:"headers"`
	RetryWithout []string          `yaml:"retry_without"`
	Markets      []string          `yaml:"markets"`
	// Response is json, html or text. Default json.
	Response string `yaml:"response"`
	// Items locates the items: a JSON path ("." is the document itself, and
	// an object at the path is read as its values) or a CSS selector.
	Items string `yaml:"items"`
	// Exclude and Within are CSS selectors for HTML items: an item inside an
	// excluded element is dropped, and an item must sit inside Within.
	Exclude string `yaml:"exclude"`
	Within  string `yaml:"within"`
	// Pages follows a bounded set of further result pages found on the first.
	Pages *PageSpec `yaml:"pages"`
	// Steps run before the search, in order, capturing variables.
	Steps []StepSpec `yaml:"steps"`
	// Require, Forbid, RequireMin, RequireEmpty and RequireMatch filter JSON
	// items by their fields. RequireMatch values are regular expressions in
	// which {field} placeholders expand to the item's own values.
	Require      map[string]string  `yaml:"require"`
	Forbid       map[string]string  `yaml:"forbid"`
	RequireMin   map[string]float64 `yaml:"require_min"`
	RequireEmpty []string           `yaml:"require_empty"`
	RequireMatch map[string]string  `yaml:"require_match"`
	// RequireDocument checks top-level JSON fields before any item is read.
	RequireDocument map[string]string `yaml:"require_document"`
	// MinDurationSeconds drops items whose read duration is known and shorter.
	MinDurationSeconds float64              `yaml:"min_duration_seconds"`
	Fields             map[string]FieldSpec `yaml:"fields"`
	Offers             *OfferSpec           `yaml:"offers"`
	Constants          map[string]string    `yaml:"constants"`
	Limit              int                  `yaml:"limit"`
	// TimeoutSeconds bounds one whole search, steps included. Default 8.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// StatusName is the provider name the console shows for this search; it
	// may use {market}. Default: the driver's provider.
	StatusName string `yaml:"status_name"`
	// RequireRelevance asks the search pipeline to keep only listings that
	// match the query: "score" keeps titles that score as the requested film,
	// "exact" keeps titles that name exactly the requested feature.
	RequireRelevance string `yaml:"require_relevance"`
	// Keep caps the listings the search pipeline keeps after relevance.
	Keep int `yaml:"keep"`
	// SameURL requires the response to come from the requested URL, so a
	// host cannot substitute another page for its catalog.
	SameURL bool `yaml:"same_url"`
	// NoRedirect refuses to follow redirects, so a guest header is never
	// forwarded to another origin.
	NoRedirect bool `yaml:"no_redirect"`

	items   cascadia.Selector
	exclude cascadia.Selector
	within  cascadia.Selector
}

// PageSpec finds further result pages on the first page of results.
type PageSpec struct {
	Select string `yaml:"select"`
	Attr   string `yaml:"attr"`
	Match  string `yaml:"match"`
	Max    int    `yaml:"max"`
	sel    cascadia.Selector
	match  *regexp.Regexp
}

// StepSpec is one request a host needs before it will answer a search: a
// page that names the current script bundle, the bundle that holds public
// identifiers, a guest token. Each step captures variables for later steps
// and for the search itself.
type StepSpec struct {
	Name        string                 `yaml:"name"`
	URL         string                 `yaml:"url"`
	Params      map[string]string      `yaml:"params"`
	Headers     map[string]string      `yaml:"headers"`
	Response    string                 `yaml:"response"`
	Capture     map[string]CaptureSpec `yaml:"capture"`
	RequireText []string               `yaml:"require_text"`
	// Assert compares captured variables: each key must equal its template.
	Assert map[string]string `yaml:"assert"`
	// CacheSeconds keeps this step's captures for later searches. Only
	// public configuration should be cached; a guest token never is.
	CacheSeconds int  `yaml:"cache_seconds"`
	SameURL      bool `yaml:"same_url"`
	NoRedirect   bool `yaml:"no_redirect"`
}

// CaptureSpec reads one variable from a response: by CSS selector and
// attribute (or text) from HTML, by path from JSON, by regular expression
// group from text or from a selected element's script, or from an earlier
// variable (From) by regular expression or, when that variable holds JSON,
// by path. A path through a [key=value] filter yields one candidate per
// kept element. Match filters the candidates; Unique requires exactly one
// to remain.
type CaptureSpec struct {
	Select  string `yaml:"select"`
	Attr    string `yaml:"attr"`
	Resolve bool   `yaml:"resolve"`
	// Visible skips elements outside the page's primary visible content:
	// navigation, footers, hidden blocks, FAQ and recommendation sections.
	Visible bool     `yaml:"visible"`
	Path    string   `yaml:"path"`
	Paths   []string `yaml:"paths"`
	Regex   string   `yaml:"regex"`
	Group   int      `yaml:"group"`
	From    string   `yaml:"from"`
	// JSON keeps a non-scalar value as its JSON text, or decodes the JSON
	// value that follows a regex match, so a later capture can read it by
	// path (From with Path).
	JSON      bool   `yaml:"json"`
	Transform string `yaml:"transform"`
	Unique    bool   `yaml:"unique"`
	Match     string `yaml:"match"`
	Default   string `yaml:"default"`
	sel       cascadia.Selector
	regex     *regexp.Regexp
	match     *regexp.Regexp
}

// FieldSpec reads one listing field from an item. JSON items are read by
// path, the first of several paths, or a template over the item and the
// search variables. HTML items are read by a CSS selector relative to the
// item (or its Closest ancestor) and an attribute, or the element text. The
// value is then cleaned, transformed and checked.
type FieldSpec struct {
	Path          string    `yaml:"path"`
	Paths         []string  `yaml:"paths"`
	Template      string    `yaml:"template"`
	Select        string    `yaml:"select"`
	Attr          string    `yaml:"attr"`
	Closest       string    `yaml:"closest"`
	Resolve       *bool     `yaml:"resolve"`
	Transform     string    `yaml:"transform"`
	Default       string    `yaml:"default"`
	Match         string    `yaml:"match"`
	Reject        string    `yaml:"reject"`
	MaxLength     int       `yaml:"max_length"`
	Range         []float64 `yaml:"range"`
	Optional      bool      `yaml:"optional"`
	StripPrefix   string    `yaml:"strip_prefix"`
	StripSuffixes []string  `yaml:"strip_suffixes"`
	Truncate      int       `yaml:"truncate"`
	match         *regexp.Regexp
	reject        *regexp.Regexp
	sel           cascadia.Selector
	closest       cascadia.Selector
}

// OfferSpec reads storefront prices: a currency field and labelled price
// fields, the first non-negative of each group.
type OfferSpec struct {
	Currency         string      `yaml:"currency"`
	Prices           []PriceSpec `yaml:"prices"`
	AccessWhenPriced string      `yaml:"access_when_priced"`
	AccessOtherwise  string      `yaml:"access_otherwise"`
}

// PriceSpec is one labelled price group, for example Rent or Buy.
type PriceSpec struct {
	Label string   `yaml:"label"`
	Paths []string `yaml:"paths"`
}

type cachedStep struct {
	vars    map[string]string
	expires time.Time
}

var (
	driverName  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	captureName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	// placeholderName is what {…} may hold in a template; a brace pair that
	// starts otherwise, such as a regular expression's {2,5}, is literal.
	placeholderName = regexp.MustCompile(`^[A-Za-z_][]A-Za-z0-9_.[]*$`)
	marketCode      = regexp.MustCompile(`^[A-Z]{2}$`)
	listingKeys     = map[string]bool{
		"id": true, "title": true, "url": true, "media_url": true, "year": true, "duration": true, "description": true,
		"thumbnail": true, "access_type": true, "price": true, "market": true, "source_type": true, "provider": true,
		"access_note": true, "resolution": true, "tags": true, "catalog_date": true, "creator": true, "license": true, "pages": true, "aliases": true,
	}
	transforms = map[string]bool{
		"": true, "scalar": true, "lower": true, "upper": true, "trim": true, "year_of_date": true, "year_in_text": true,
		"duration_from_millis": true, "duration_from_seconds": true, "duration_seconds": true, "html_unescape": true, "html_text": true,
		"url_query": true, "date": true, "strip_query": true,
	}
	constantKeys = map[string]bool{
		"source_type": true, "access_type": true, "media_url": true, "price": true, "market": true, "listing_only": true,
		"access_note": true, "provider": true, "access_basis": true, "access_evidence_url": true,
	}
	responses = map[string]bool{"json": true, "html": true, "text": true}
	htmlTags  = regexp.MustCompile(`<[^>]*>`)
)

// LoadDir reads every *.yaml and *.yml driver in dir, validates each and
// returns them sorted by name. A broken file fails the whole load, because a
// crawler running with half its drivers would quietly skew the index.
func LoadDir(dir string) ([]*Driver, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("crawler: cannot read drivers from %s: %w", dir, err)
	}
	var drivers []*Driver
	names := map[string]string{}
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if entry.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		d, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if other, dup := names[d.Name]; dup {
			return nil, fmt.Errorf("crawler: drivers %s and %s both claim the name %q", other, path, d.Name)
		}
		names[d.Name] = path
		drivers = append(drivers, d)
	}
	sort.Slice(drivers, func(i, j int) bool { return drivers[i].Name < drivers[j].Name })
	return drivers, nil
}

// LoadFile reads and validates one driver file. Unknown keys are refused so
// a typo cannot silently disable a rule.
func LoadFile(path string) (*Driver, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("crawler: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var d Driver
	if err := decoder.Decode(&d); err != nil {
		return nil, fmt.Errorf("crawler: %s: %w", path, err)
	}
	d.Path = path
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("crawler: %s: %w", path, err)
	}
	return &d, nil
}

// Validate checks the driver and fills defaults.
func (d *Driver) Validate() error {
	if !driverName.MatchString(d.Name) {
		return fmt.Errorf("name %q must be lower-case letters, digits and dashes", d.Name)
	}
	if strings.TrimSpace(d.Provider) == "" {
		return errors.New("provider is required")
	}
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	if d.Kind == "" {
		d.Kind = lens.TypeVideo
	}
	if !lens.KnownType(d.Kind) {
		return fmt.Errorf("kind %q is not an object type", d.Kind)
	}
	if len(d.Hosts) == 0 {
		return errors.New("at least one host is required")
	}
	for i, host := range d.Hosts {
		d.Hosts[i] = strings.ToLower(strings.TrimSpace(host))
		if d.Hosts[i] == "" || strings.ContainsAny(d.Hosts[i], "/: ") {
			return fmt.Errorf("host %q must be a bare hostname", host)
		}
	}
	if d.Rate.PerMinute <= 0 {
		d.Rate.PerMinute = 30
	}
	if d.Rate.Concurrency <= 0 {
		d.Rate.Concurrency = 2
	}
	if d.Spec == nil && d.Check == nil {
		return errors.New("a search or check section is required")
	}
	d.stepCache = map[string]cachedStep{}
	if d.Spec != nil {
		if d.Spec.StatusName == "" {
			d.Spec.StatusName = d.Provider
		}
		if err := d.Spec.validate(); err != nil {
			return err
		}
	}
	if len(d.Feeds) > 0 && d.Spec == nil {
		return errors.New("feeds need a search section, whose steps and item rules they share")
	}
	for name, feed := range d.Feeds {
		if !feedName.MatchString(name) || feed == nil {
			return fmt.Errorf("feeds.%s needs a simple name and a body", name)
		}
		if err := feed.validate(d.Spec); err != nil {
			return fmt.Errorf("feeds.%s: %w", name, err)
		}
	}
	if d.Check != nil {
		if err := d.Check.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s *SearchSpec) validate() error {
	probe := expand(s.URL, func(string) string { return "x" })
	u, err := url.Parse(probe)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" {
		return fmt.Errorf("search.url %q must be an https URL without a query string; put the query in params", s.URL)
	}
	if s.Response == "" {
		s.Response = "json"
	}
	if !responses[s.Response] || s.Response == "text" {
		return fmt.Errorf("search.response %q is not supported; use json or html", s.Response)
	}
	if strings.TrimSpace(s.Items) == "" {
		return errors.New("search.items is required")
	}
	if s.Response == "html" {
		if s.items, err = cascadia.Compile(s.Items); err != nil {
			return fmt.Errorf("search.items: %w", err)
		}
		if s.Exclude != "" {
			if s.exclude, err = cascadia.Compile(s.Exclude); err != nil {
				return fmt.Errorf("search.exclude: %w", err)
			}
		}
		if s.Within != "" {
			if s.within, err = cascadia.Compile(s.Within); err != nil {
				return fmt.Errorf("search.within: %w", err)
			}
		}
		if s.Pages != nil {
			if s.Pages.Select == "" || s.Pages.Max <= 0 {
				return errors.New("search.pages needs a select and a positive max")
			}
			if s.Pages.sel, err = cascadia.Compile(s.Pages.Select); err != nil {
				return fmt.Errorf("search.pages.select: %w", err)
			}
			if s.Pages.Attr == "" {
				s.Pages.Attr = "href"
			}
			if s.Pages.Match != "" {
				if s.Pages.match, err = regexp.Compile(s.Pages.Match); err != nil {
					return fmt.Errorf("search.pages.match: %w", err)
				}
			}
		}
		if len(s.Require) > 0 || len(s.Forbid) > 0 || len(s.RequireMin) > 0 || len(s.RequireEmpty) > 0 || len(s.RequireMatch) > 0 || len(s.RequireDocument) > 0 || s.Offers != nil || len(s.RetryWithout) > 0 {
			return errors.New("html searches support fields, exclude, within and pages; item rules and offers are for json")
		}
	} else if s.Exclude != "" || s.Within != "" || s.Pages != nil {
		return errors.New("search.exclude, within and pages apply to html searches only")
	}
	for _, key := range s.RetryWithout {
		if _, ok := s.Params[key]; !ok {
			return fmt.Errorf("search.retry_without names %q, which is not a param", key)
		}
	}
	for i, market := range s.Markets {
		s.Markets[i] = strings.ToUpper(strings.TrimSpace(market))
		if !marketCode.MatchString(s.Markets[i]) {
			return fmt.Errorf("search.markets entry %q must be a two-letter code", market)
		}
	}
	if s.Limit <= 0 {
		s.Limit = 20
	}
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 8
	}
	switch s.RequireRelevance {
	case "", "score", "exact":
	default:
		return fmt.Errorf("search.require_relevance %q must be score or exact", s.RequireRelevance)
	}
	if s.Keep < 0 {
		return errors.New("search.keep must be positive")
	}
	if _, ok := s.Fields["title"]; !ok {
		return errors.New("search.fields.title is required")
	}
	if _, ok := s.Fields["url"]; !ok {
		return errors.New("search.fields.url is required")
	}
	for key, spec := range s.Fields {
		if !listingKeys[key] {
			return fmt.Errorf("search.fields.%s is not a listing field", key)
		}
		if err := spec.validate(s.Response); err != nil {
			return fmt.Errorf("search.fields.%s: %w", key, err)
		}
		s.Fields[key] = spec
	}
	for key := range s.Constants {
		if !constantKeys[key] {
			return fmt.Errorf("search.constants.%s is not a constant listing field", key)
		}
	}
	for key, pattern := range s.RequireMatch {
		if key == "" || pattern == "" {
			return errors.New("search.require_match needs a field and a pattern")
		}
	}
	if s.Offers != nil {
		if s.Offers.Currency == "" || len(s.Offers.Prices) == 0 {
			return errors.New("search.offers needs a currency path and at least one price group")
		}
		for _, price := range s.Offers.Prices {
			if price.Label == "" || len(price.Paths) == 0 {
				return errors.New("search.offers.prices entries need a label and paths")
			}
		}
		if s.Offers.AccessWhenPriced == "" {
			s.Offers.AccessWhenPriced = "paid"
		}
		if s.Offers.AccessOtherwise == "" {
			s.Offers.AccessOtherwise = "unknown"
		}
	}
	for i := range s.Steps {
		if err := s.Steps[i].validate(); err != nil {
			return fmt.Errorf("search.steps[%d]: %w", i, err)
		}
	}
	return nil
}

func (f *FieldSpec) validate(response string) error {
	sources := 0
	if f.Path != "" {
		sources++
	}
	if len(f.Paths) > 0 {
		sources++
	}
	if f.Template != "" {
		sources++
	}
	if f.Select != "" || f.Attr != "" || f.Closest != "" {
		sources++
	}
	if sources > 1 {
		return errors.New("use one of path, paths, template, or select/attr")
	}
	if sources == 0 && f.Default == "" {
		return errors.New("needs a path, paths, template, select or default")
	}
	if response == "html" && (f.Path != "" || len(f.Paths) > 0) {
		return errors.New("html items are read by select and attr, not path")
	}
	if response == "json" && (f.Select != "" || f.Closest != "" || f.Attr != "") {
		return errors.New("json items are read by path, not select")
	}
	name, _, _ := strings.Cut(f.Transform, ":")
	if !transforms[name] {
		return fmt.Errorf("unknown transform %q", f.Transform)
	}
	if f.Truncate < 0 || f.MaxLength < 0 {
		return errors.New("truncate and max_length must be positive")
	}
	if len(f.Range) != 0 && (len(f.Range) != 2 || f.Range[0] > f.Range[1]) {
		return errors.New("range needs two numbers, low then high")
	}
	var err error
	if f.Match != "" {
		if f.match, err = regexp.Compile(f.Match); err != nil {
			return fmt.Errorf("match: %w", err)
		}
	}
	if f.Reject != "" {
		if f.reject, err = regexp.Compile(f.Reject); err != nil {
			return fmt.Errorf("reject: %w", err)
		}
	}
	if f.Select != "" && f.Select != "." {
		if f.sel, err = cascadia.Compile(f.Select); err != nil {
			return fmt.Errorf("select: %w", err)
		}
	}
	if f.Closest != "" {
		if f.closest, err = cascadia.Compile(f.Closest); err != nil {
			return fmt.Errorf("closest: %w", err)
		}
	}
	return nil
}

func (st *StepSpec) validate() error {
	if strings.TrimSpace(st.URL) == "" {
		return errors.New("url is required")
	}
	if st.Response == "" {
		st.Response = "json"
	}
	if !responses[st.Response] {
		return fmt.Errorf("response %q must be json, html or text", st.Response)
	}
	if len(st.Capture) == 0 && len(st.RequireText) == 0 {
		return errors.New("a step must capture a variable or require text")
	}
	for name, capture := range st.Capture {
		if !captureName.MatchString(name) {
			return fmt.Errorf("capture %q needs a simple name", name)
		}
		if err := capture.validate(st.Response); err != nil {
			return fmt.Errorf("capture %s: %w", name, err)
		}
		st.Capture[name] = capture
	}
	if st.CacheSeconds < 0 {
		return errors.New("cache_seconds must be positive")
	}
	return nil
}

func (c *CaptureSpec) validate(response string) error {
	var err error
	if c.Path != "" && len(c.Paths) > 0 {
		return errors.New("use path or paths, not both")
	}
	switch {
	case c.From != "":
		if c.Regex == "" && c.Path == "" && len(c.Paths) == 0 {
			return errors.New("from needs a regex or a path")
		}
	case c.Select != "":
		if response != "html" {
			return errors.New("select reads html responses only")
		}
		if c.sel, err = cascadia.Compile(c.Select); err != nil {
			return fmt.Errorf("select: %w", err)
		}
	case c.Path != "" || len(c.Paths) > 0:
		if response != "json" {
			return errors.New("path reads json responses only")
		}
	case c.Regex != "":
		if response != "text" && response != "html" {
			return errors.New("regex reads text responses only")
		}
	default:
		return errors.New("needs select, path, regex or from")
	}
	if c.Visible && c.Select == "" {
		return errors.New("visible applies to select")
	}
	if name, _, _ := strings.Cut(c.Transform, ":"); !transforms[name] {
		return fmt.Errorf("unknown transform %q", c.Transform)
	}
	if c.Regex != "" {
		if c.regex, err = regexp.Compile(c.Regex); err != nil {
			return fmt.Errorf("regex: %w", err)
		}
		if c.Group < 0 || c.Group > c.regex.NumSubexp() {
			return fmt.Errorf("group %d is not in the regex", c.Group)
		}
		if c.Group == 0 && c.regex.NumSubexp() > 0 {
			c.Group = 1
		}
	}
	if c.Match != "" {
		if c.match, err = regexp.Compile(c.Match); err != nil {
			return fmt.Errorf("match: %w", err)
		}
	}
	return nil
}

// Markets returns the storefront markets to search, or one empty market when
// the host has no storefronts.
func (d *Driver) Markets() []string {
	if d.Spec == nil || len(d.Spec.Markets) == 0 {
		return []string{""}
	}
	return d.Spec.Markets
}

// HasMarkets reports whether the host is a storefront searched per market.
func (d *Driver) HasMarkets() bool {
	return d.Spec != nil && len(d.Spec.Markets) > 0
}

// Host is the hostname of the search endpoint, which frontier budgets key
// on, or the first host of a driver that only checks pages.
func (d *Driver) Host() string {
	if d.Spec == nil {
		if len(d.Hosts) > 0 {
			return d.Hosts[0]
		}
		return ""
	}
	return lens.Hostname(expand(d.Spec.URL, func(string) string { return "" }))
}

// Timeout bounds one whole search, steps included.
func (d *Driver) Timeout() time.Duration {
	if d.Spec == nil {
		return 0
	}
	return time.Duration(d.Spec.TimeoutSeconds) * time.Second
}

// StatusName is the provider name shown for a search in a market.
func (d *Driver) StatusName(market string) string {
	if d.Spec == nil {
		return d.Provider
	}
	vars := searchVars("", market)
	return expand(d.Spec.StatusName, func(name string) string { return vars[name] })
}

// SearchURL builds the search request for a query and market, without any
// step variables; it is what the frontier records.
func (d *Driver) SearchURL(query, market string) string {
	if d.Spec == nil {
		return ""
	}
	return d.searchURL(searchVars(query, market), nil)
}

func (d *Driver) searchURL(vars map[string]string, without map[string]bool) string {
	resolve := func(name string) string { return vars[name] }
	base := expand(d.Spec.URL, resolve)
	values := url.Values{}
	for key, template := range d.Spec.Params {
		if without[key] {
			continue
		}
		values.Set(key, expand(template, resolve))
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

func searchVars(query, market string) map[string]string {
	phrase := strings.NewReplacer(`\`, " ", `"`, " ", "\r", " ", "\n", " ").Replace(query)
	return map[string]string{
		"query":        query,
		"query_phrase": strings.Join(strings.Fields(phrase), " "),
		"market":       market,
		"market_lower": strings.ToLower(market),
	}
}

// Search runs one search against the host and returns its listings: the
// steps first, then the search request, then any further result pages.
func (d *Driver) Search(ctx context.Context, fetcher *Fetcher, query, market string) ([]atlas.Listing, error) {
	if d.Spec == nil {
		return nil, fmt.Errorf("crawler: driver %s cannot search", d.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, d.Timeout())
	defer cancel()
	vars := searchVars(query, market)
	if err := d.runSteps(ctx, fetcher, d.Spec.Steps, "search", vars); err != nil {
		return nil, err
	}
	first, err := d.fetchDocument(ctx, fetcher, d.searchURL(vars, nil), vars)
	if err != nil {
		return nil, err
	}
	documents := []*document{first}
	if d.Spec.Response == "json" && len(first.items(d.Spec.Items)) == 0 && len(d.Spec.RetryWithout) > 0 {
		without := map[string]bool{}
		for _, key := range d.Spec.RetryWithout {
			without[key] = true
		}
		if first, err = d.fetchDocument(ctx, fetcher, d.searchURL(vars, without), vars); err != nil {
			return nil, err
		}
		documents = []*document{first}
	}
	if d.Spec.Pages != nil {
		for _, page := range first.pages(d.Spec.Pages) {
			next, err := d.fetchDocument(ctx, fetcher, page, vars)
			if err != nil {
				continue
			}
			documents = append(documents, next)
		}
	}
	seen := map[string]bool{}
	var listings []atlas.Listing
	for _, doc := range documents {
		for _, raw := range doc.items(d.Spec.Items) {
			if len(listings) >= d.Spec.Limit {
				return listings, nil
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
			listing, ok := d.listingFrom(it, market, d.Spec.Fields)
			if !ok || seen[listing.URL] {
				continue
			}
			seen[listing.URL] = true
			listings = append(listings, listing)
		}
	}
	return listings, nil
}

// document is one fetched search response, parsed per its format.
type document struct {
	url  *url.URL
	json any
	html *xhtml.Node
	text string
	spec *SearchSpec
}

func (d *Driver) fetchDocument(ctx context.Context, fetcher *Fetcher, target string, vars map[string]string) (*document, error) {
	return d.fetchDocumentWith(ctx, fetcher, target, d.Spec.Headers, vars)
}

// fetchDocumentWith fetches a search or feed response with the given header
// templates and parses it per the search's response format and checks.
func (d *Driver) fetchDocumentWith(ctx context.Context, fetcher *Fetcher, target string, headerTemplates map[string]string, vars map[string]string) (*document, error) {
	resolve := func(name string) string { return vars[name] }
	headers := map[string]string{}
	for key, template := range headerTemplates {
		headers[key] = expand(template, resolve)
	}
	body, final, err := d.fetch(ctx, fetcher, target, headers, d.Spec.SameURL, d.Spec.NoRedirect)
	if err != nil {
		return nil, err
	}
	doc := &document{url: final, spec: d.Spec}
	switch d.Spec.Response {
	case "html":
		parsed, err := xhtml.Parse(strings.NewReader(string(body)))
		if err != nil {
			return nil, fmt.Errorf("crawler: %s: response is not HTML", d.Name)
		}
		doc.html = parsed
	default:
		if err := json.Unmarshal(body, &doc.json); err != nil {
			return nil, fmt.Errorf("crawler: %s: response is not JSON: %w", d.Name, err)
		}
		for key, want := range d.Spec.RequireDocument {
			if scalarString(lookup(doc.json, key)) != want {
				return nil, fmt.Errorf("crawler: %s: response did not pass the document checks", d.Name)
			}
		}
	}
	return doc, nil
}

// fetch performs one driver request under the driver's rules and returns
// the body and the URL it came from. Errors name the host, never a token.
func (d *Driver) fetch(ctx context.Context, fetcher *Fetcher, target string, headers map[string]string, sameURL, noRedirect bool) ([]byte, *url.URL, error) {
	requested, err := url.Parse(target)
	if err != nil || requested.Scheme != "https" || requested.Host == "" {
		return nil, nil, fmt.Errorf("crawler: %s: %q is not an https URL", d.Name, target)
	}
	response, err := fetcher.Fetch(ctx, target, headers, FetchOptions{NoRedirect: noRedirect})
	if err != nil {
		return nil, nil, fmt.Errorf("crawler: %s: %w", d.Name, err)
	}
	if response.Status < 200 || response.Status > 299 {
		return nil, nil, fmt.Errorf("crawler: %s: HTTP %d from %s", d.Name, response.Status, requested.Hostname())
	}
	final, err := url.Parse(response.FinalURL)
	if err != nil || final.Host == "" {
		final = requested
	}
	if sameURL && final.String() != requested.String() {
		return nil, nil, fmt.Errorf("crawler: %s: %s answered from a different page", d.Name, requested.Hostname())
	}
	return response.Body, final, nil
}

// items returns the raw items of a document: decoded JSON values, or HTML
// element nodes that pass the exclude and within rules.
func (doc *document) items(path string) []any {
	if doc.html != nil {
		var out []any
		for _, node := range doc.spec.items.MatchAll(doc.html) {
			if doc.spec.exclude != nil && hasAncestorOrSelf(node, doc.spec.exclude) {
				continue
			}
			if doc.spec.within != nil && !hasAncestor(node, doc.spec.within) {
				continue
			}
			out = append(out, node)
		}
		return out
	}
	return itemsAt(doc.json, path)
}

// pages returns the further result pages linked from an HTML document.
func (doc *document) pages(spec *PageSpec) []string {
	if doc.html == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, node := range spec.sel.MatchAll(doc.html) {
		raw := attribute(node, spec.Attr)
		if raw == "" {
			continue
		}
		ref, err := url.Parse(raw)
		if err != nil {
			continue
		}
		target := doc.url.ResolveReference(ref).String()
		if seen[target] || (spec.match != nil && !spec.match.MatchString(target)) {
			continue
		}
		seen[target] = true
		out = append(out, target)
		if len(out) >= spec.Max {
			break
		}
	}
	return out
}

// itemsAt reads the item list at path: the document itself for ".", a list
// as is, and an object as its values in key order.
func itemsAt(document any, path string) []any {
	value := document
	if path != "." {
		value = lookup(document, path)
	}
	switch v := value.(type) {
	case []any:
		return v
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]any, 0, len(keys))
		for _, key := range keys {
			items = append(items, v[key])
		}
		return items
	}
	return nil
}

// item is one candidate listing in a document, JSON or HTML.
type item struct {
	json map[string]any
	node *xhtml.Node
	page *url.URL
	vars map[string]string
}

// resolve answers template placeholders: search and step variables first,
// then the item's own JSON fields.
func (it item) resolve(name string) string {
	if v, ok := it.vars[name]; ok {
		return v
	}
	if it.json != nil {
		return scalarString(lookup(it.json, name))
	}
	return ""
}

func (d *Driver) itemAllowed(it item) bool {
	if it.json == nil {
		return true
	}
	for key, want := range d.Spec.Require {
		if scalarString(lookup(it.json, key)) != want {
			return false
		}
	}
	for key, forbidden := range d.Spec.Forbid {
		if scalarString(lookup(it.json, key)) == forbidden {
			return false
		}
	}
	for _, key := range d.Spec.RequireEmpty {
		if scalarString(lookup(it.json, key)) != "" {
			return false
		}
	}
	for key, min := range d.Spec.RequireMin {
		number, ok := lookup(it.json, key).(float64)
		if !ok || number < min {
			return false
		}
	}
	for key, template := range d.Spec.RequireMatch {
		pattern := expand(template, func(name string) string { return regexp.QuoteMeta(it.resolve(name)) })
		re, err := regexp.Compile(pattern)
		if err != nil || !re.MatchString(scalarString(lookup(it.json, key))) {
			return false
		}
	}
	return true
}

func (d *Driver) listingFrom(it item, market string, fields map[string]FieldSpec) (atlas.Listing, bool) {
	values := map[string]string{}
	var tags, aliases []string
	for key, spec := range fields {
		if key == "tags" {
			tags = spec.readTags(it)
			continue
		}
		if key == "aliases" {
			// Other names the host gives the object, read like tags.
			aliases = spec.readTags(it)
			continue
		}
		value, ok := spec.read(it)
		if !ok {
			return atlas.Listing{}, false
		}
		values[key] = value
	}
	for key, value := range d.Spec.Constants {
		if values[key] == "" {
			values[key] = expand(value, it.resolve)
		}
	}
	if strings.TrimSpace(values["title"]) == "" || values["url"] == "" {
		return atlas.Listing{}, false
	}
	if u, err := url.Parse(values["url"]); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return atlas.Listing{}, false
	}
	if d.Spec.MinDurationSeconds > 0 {
		if seconds := lens.DurationSeconds(values["duration"]); seconds > 0 && seconds < d.Spec.MinDurationSeconds {
			return atlas.Listing{}, false
		}
	}
	listing := atlas.Listing{
		ID:                values["id"],
		Kind:              d.Kind,
		Title:             strings.TrimSpace(values["title"]),
		Creator:           strings.TrimSpace(values["creator"]),
		URL:               values["url"],
		MediaURL:          values["media_url"],
		Provider:          firstNonEmpty(values["provider"], d.Provider),
		SourceType:        values["source_type"],
		AccessType:        values["access_type"],
		AccessBasis:       values["access_basis"],
		AccessEvidenceURL: values["access_evidence_url"],
		AccessNote:        values["access_note"],
		Price:             values["price"],
		Market:            firstNonEmpty(values["market"], market),
		Duration:          values["duration"],
		Description:       values["description"],
		Thumbnail:         values["thumbnail"],
		Resolution:        values["resolution"],
		License:           values["license"],
		CatalogDate:       values["catalog_date"],
		Tags:              tags,
		Aliases:           aliases,
		ListingOnly:       values["media_url"] == "",
	}
	if year, err := strconv.Atoi(values["year"]); err == nil {
		listing.Year = year
	}
	if pages, err := strconv.Atoi(strings.TrimSpace(values["pages"])); err == nil && pages > 0 {
		listing.Pages = pages
	}
	if d.Spec.Offers != nil && it.json != nil {
		listing.Price, listing.AccessType = d.Spec.Offers.read(it.json)
	}
	if listing.AccessType == "" {
		listing.AccessType = "unknown"
	}
	if raw, ok := values["listing_only"]; ok && raw != "" {
		listing.ListingOnly = raw == "true"
	}
	return listing, true
}

// read evaluates a field. It reports false when a required rule fails,
// which drops the item rather than recording a wrong value; an optional
// field that fails its rules is blank instead.
func (f FieldSpec) read(it item) (string, bool) {
	var value string
	switch {
	case f.Path != "":
		value = scalarString(lookup(it.json, f.Path))
	case len(f.Paths) > 0:
		for _, path := range f.Paths {
			if value = scalarString(lookup(it.json, path)); value != "" {
				break
			}
		}
	case f.Template != "":
		value = expand(f.Template, it.resolve)
	case it.node != nil && (f.Select != "" || f.Attr != "" || f.Closest != ""):
		value = f.readNode(it)
	}
	value = transform(f.Transform, value)
	if f.StripPrefix != "" {
		value = strings.TrimSpace(strings.TrimPrefix(value, f.StripPrefix))
	}
	for _, suffix := range f.StripSuffixes {
		if strings.HasSuffix(strings.ToLower(value), strings.ToLower(suffix)) && len(value) > len(suffix) {
			value = strings.TrimSpace(value[:len(value)-len(suffix)])
			break
		}
	}
	if f.Truncate > 0 {
		if runes := []rune(value); len(runes) > f.Truncate {
			value = strings.TrimSpace(string(runes[:f.Truncate])) + "…"
		}
	}
	if value == "" {
		value = expand(f.Default, it.resolve)
	}
	if !f.accepts(value) {
		if f.Optional {
			return "", true
		}
		return "", false
	}
	return value, true
}

// accepts applies the match, reject, length and range rules to a value.
func (f FieldSpec) accepts(value string) bool {
	if f.match != nil && !f.match.MatchString(value) {
		return false
	}
	if f.reject != nil && value != "" && f.reject.MatchString(value) {
		return false
	}
	if f.MaxLength > 0 && len(value) > f.MaxLength {
		return false
	}
	if len(f.Range) == 2 && value != "" {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || number < f.Range[0] || number > f.Range[1] {
			return false
		}
	}
	return true
}

// readNode reads an HTML field: the selected element's attribute or text,
// with URL attributes resolved against the page unless resolve is false.
func (f FieldSpec) readNode(it item) string {
	scope := it.node
	if f.closest != nil {
		scope = closest(it.node, f.closest)
		if scope == nil {
			return ""
		}
	}
	target := scope
	if f.sel != nil {
		if target = f.sel.MatchFirst(scope); target == nil {
			return ""
		}
	}
	if f.Attr == "" {
		return nodeText(target)
	}
	value := attribute(target, f.Attr)
	resolve := f.Resolve == nil && (strings.EqualFold(f.Attr, "href") || strings.EqualFold(f.Attr, "src")) || f.Resolve != nil && *f.Resolve
	if value != "" && resolve && it.page != nil {
		if ref, err := url.Parse(value); err == nil {
			value = it.page.ResolveReference(ref).String()
		}
	}
	return value
}

// readTags reads a list field, accepting a single scalar as a one-item list.
func (f FieldSpec) readTags(it item) []string {
	var raw any
	if f.Path != "" && it.json != nil {
		raw = lookup(it.json, f.Path)
	}
	var tags []string
	switch v := raw.(type) {
	case []any:
		for _, element := range v {
			if tag := scalarString(element); tag != "" {
				tags = append(tags, tag)
			}
		}
	default:
		if tag := scalarString(v); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func (o *OfferSpec) read(item map[string]any) (string, string) {
	currency := strings.TrimSpace(scalarString(lookup(item, o.Currency)))
	if currency == "" {
		// A bare amount without its currency is not a meaningful offer.
		return "", o.AccessOtherwise
	}
	var labels []string
	for _, group := range o.Prices {
		for _, path := range group.Paths {
			amount, ok := lookup(item, path).(float64)
			if ok && amount >= 0 {
				labels = append(labels, fmt.Sprintf("%s %s %.2f", group.Label, currency, amount))
				break
			}
		}
	}
	if len(labels) == 0 {
		return "", o.AccessOtherwise
	}
	return strings.Join(labels, " · "), o.AccessWhenPriced
}

func transform(spec, value string) string {
	name, arg, _ := strings.Cut(spec, ":")
	switch name {
	case "lower":
		return strings.ToLower(value)
	case "upper":
		return strings.ToUpper(value)
	case "trim", "scalar", "":
		return strings.TrimSpace(value)
	case "year_of_date":
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
			return strconv.Itoa(t.Year())
		}
		return lens.YearPattern.FindString(value)
	case "year_in_text":
		return lens.YearPattern.FindString(value)
	case "duration_from_millis":
		millis, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || millis <= 0 {
			return ""
		}
		minutes := int64(millis) / 60000
		if minutes <= 0 {
			return ""
		}
		return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
	case "duration_from_seconds":
		seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return ""
		}
		return lens.FormatDuration(seconds)
	case "duration_seconds":
		seconds := lens.DurationSeconds(value)
		if seconds <= 0 {
			return ""
		}
		return strconv.FormatFloat(seconds, 'f', -1, 64)
	case "html_unescape":
		return strings.TrimSpace(html.UnescapeString(value))
	case "html_text":
		return strings.Join(strings.Fields(html.UnescapeString(htmlTags.ReplaceAllString(value, " "))), " ")
	case "date":
		// A host's listed date in the layout the host uses, kept as a day;
		// a date before cinema or more than a year ahead is noise.
		layout := arg
		if layout == "" {
			layout = time.RFC3339
		}
		t, err := time.Parse(layout, strings.TrimSpace(value))
		if err != nil || t.Year() < 1880 || t.After(time.Now().AddDate(1, 0, 0)) {
			return ""
		}
		return t.Format("2006-01-02")
	case "url_query":
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(u.Query().Get(arg))
	case "strip_query":
		// A file URL without the tracking parameters a host appends to it.
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil {
			return ""
		}
		u.RawQuery, u.Fragment = "", ""
		return u.String()
	}
	return value
}

// lookup walks a decoded JSON document by a dotted path with optional
// [index] steps, for example response.docs or items[0].title, and
// [key=value] filters that keep the list elements whose key says value
// (case and spacing aside); the rest of the path then applies to each kept
// element, so channels[name=New Arrivals].url is every such channel's URL.
func lookup(document any, path string) any {
	return lookupSteps(document, strings.Split(path, "."))
}

func lookupSteps(document any, steps []string) any {
	current := document
	for i, step := range steps {
		if step == "" {
			return nil
		}
		key, brackets, ok := splitBrackets(step)
		if !ok {
			return nil
		}
		if key != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = object[key]
		}
		for j, bracket := range brackets {
			if index, err := strconv.Atoi(bracket); err == nil {
				list, ok := current.([]any)
				if !ok || index < 0 || index >= len(list) {
					return nil
				}
				current = list[index]
				continue
			}
			name, want, found := strings.Cut(bracket, "=")
			list, ok := current.([]any)
			if !found || !ok || j != len(brackets)-1 {
				return nil
			}
			var matches []any
			for _, element := range list {
				if object, ok := element.(map[string]any); ok && normalizedEqual(scalarString(object[name]), want) {
					matches = append(matches, element)
				}
			}
			rest := steps[i+1:]
			if len(matches) == 0 {
				return nil
			}
			if len(rest) == 0 {
				return matches
			}
			var out []any
			for _, match := range matches {
				if value := lookupSteps(match, rest); !emptyValue(value) {
					out = append(out, value)
				}
			}
			if len(out) == 0 {
				return nil
			}
			return out
		}
	}
	return current
}

// splitBrackets separates items[0][name=x] into its key and bracket contents.
func splitBrackets(step string) (string, []string, bool) {
	key := step
	var brackets []string
	for strings.HasSuffix(key, "]") {
		open := strings.LastIndex(key, "[")
		if open < 0 {
			return "", nil, false
		}
		brackets = append([]string{key[open+1 : len(key)-1]}, brackets...)
		key = key[:open]
	}
	return key, brackets, true
}

func emptyValue(value any) bool {
	if value == nil {
		return true
	}
	list, ok := value.([]any)
	return ok && len(list) == 0
}

func normalizedEqual(a, b string) bool {
	normalize := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return normalize(a) == normalize(b)
}

// scalarString renders a JSON scalar as text. A one-element array is its
// element, because Archive metadata wraps single values that way.
func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		if len(v) == 1 {
			return scalarString(v[0])
		}
	}
	return ""
}

// expand replaces {name} and {name|filter|filter} in a template. Filters:
// lower, upper, path (URL path escaping), path_words (slashes become word
// separators before path escaping), query (URL query escaping) and
// strip_article (drop a leading the, an or a). An unknown name expands to
// nothing.
func expand(template string, resolve func(string) string) string {
	var b strings.Builder
	for {
		open := strings.Index(template, "{")
		if open < 0 {
			b.WriteString(template)
			return b.String()
		}
		close := strings.Index(template[open:], "}")
		if close < 0 {
			b.WriteString(template)
			return b.String()
		}
		b.WriteString(template[:open])
		parts := strings.Split(template[open+1:open+close], "|")
		name := strings.TrimSpace(parts[0])
		if !placeholderName.MatchString(name) {
			// Not a placeholder: a regular expression's {2,5}, for instance.
			b.WriteString(template[open : open+close+1])
			template = template[open+close+1:]
			continue
		}
		value := resolve(name)
		for _, filter := range parts[1:] {
			switch strings.TrimSpace(filter) {
			case "lower":
				value = strings.ToLower(value)
			case "upper":
				value = strings.ToUpper(value)
			case "path":
				value = url.PathEscape(value)
			case "path_words":
				words := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' || unicode.IsSpace(r) })
				value = url.PathEscape(strings.Join(words, " "))
			case "query":
				value = url.QueryEscape(value)
			case "strip_article":
				lowered := strings.ToLower(strings.TrimSpace(value))
				for _, article := range []string{"the ", "an ", "a "} {
					if strings.HasPrefix(lowered, article) && len(lowered) > len(article) {
						value = strings.TrimSpace(value[len(article):])
						break
					}
				}
			}
		}
		b.WriteString(value)
		template = template[open+close+1:]
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
