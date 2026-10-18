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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/itemba-net/core/internal/atlas"
	"github.com/itemba-net/core/internal/lens"
)

// Driver is a declarative description of how to search one host and read
// its listings. Drivers are data files under drivers/, loaded at startup.
type Driver struct {
	Name     string      `yaml:"name"`
	Provider string      `yaml:"provider"`
	Hosts    []string    `yaml:"hosts"`
	Rate     Rate        `yaml:"rate"`
	Spec     *SearchSpec `yaml:"search"`
	Path     string      `yaml:"-"`
}

// Rate bounds how hard the crawler may lean on the driver's hosts.
type Rate struct {
	PerMinute   int `yaml:"per_minute"`
	Concurrency int `yaml:"concurrency"`
}

// SearchSpec describes a host's search endpoint and how to read its items.
type SearchSpec struct {
	URL          string               `yaml:"url"`
	Params       map[string]string    `yaml:"params"`
	Headers      map[string]string    `yaml:"headers"`
	RetryWithout []string             `yaml:"retry_without"`
	Markets      []string             `yaml:"markets"`
	Response     string               `yaml:"response"`
	Items        string               `yaml:"items"`
	Require      map[string]string    `yaml:"require"`
	Fields       map[string]FieldSpec `yaml:"fields"`
	Offers       *OfferSpec           `yaml:"offers"`
	Constants    map[string]string    `yaml:"constants"`
	Limit        int                  `yaml:"limit"`
}

// FieldSpec reads one listing field from an item: a path, the first of
// several paths, or a template over the item and the search variables, then
// an optional transform and a pattern the result must match.
type FieldSpec struct {
	Path      string   `yaml:"path"`
	Paths     []string `yaml:"paths"`
	Template  string   `yaml:"template"`
	Transform string   `yaml:"transform"`
	Default   string   `yaml:"default"`
	Match     string   `yaml:"match"`
	match     *regexp.Regexp
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

var (
	driverName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	marketCode   = regexp.MustCompile(`^[A-Z]{2}$`)
	listingKeys  = map[string]bool{"title": true, "url": true, "media_url": true, "year": true, "duration": true, "description": true, "thumbnail": true, "access_type": true, "price": true, "market": true, "source_type": true}
	transforms   = map[string]bool{"": true, "scalar": true, "lower": true, "upper": true, "trim": true, "year_of_date": true, "year_in_text": true, "duration_from_millis": true}
	constantKeys = map[string]bool{"source_type": true, "access_type": true, "media_url": true, "price": true, "market": true}
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
	if d.Spec == nil {
		return errors.New("a search section is required in this phase")
	}
	return d.Spec.validate()
}

func (s *SearchSpec) validate() error {
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" {
		return fmt.Errorf("search.url %q must be an https URL without a query string; put the query in params", s.URL)
	}
	if s.Response == "" {
		s.Response = "json"
	}
	if s.Response != "json" {
		return fmt.Errorf("search.response %q is not supported; only json is in this phase", s.Response)
	}
	if strings.TrimSpace(s.Items) == "" {
		return errors.New("search.items is required")
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
		sources := 0
		if spec.Path != "" {
			sources++
		}
		if len(spec.Paths) > 0 {
			sources++
		}
		if spec.Template != "" {
			sources++
		}
		if sources > 1 {
			return fmt.Errorf("search.fields.%s must use one of path, paths or template", key)
		}
		if sources == 0 && spec.Default == "" {
			return fmt.Errorf("search.fields.%s needs a path, paths, template or default", key)
		}
		if !transforms[spec.Transform] {
			return fmt.Errorf("search.fields.%s uses unknown transform %q", key, spec.Transform)
		}
		if spec.Match != "" {
			re, err := regexp.Compile(spec.Match)
			if err != nil {
				return fmt.Errorf("search.fields.%s match: %w", key, err)
			}
			spec.match = re
		}
		s.Fields[key] = spec
	}
	for key := range s.Constants {
		if !constantKeys[key] {
			return fmt.Errorf("search.constants.%s is not a constant listing field", key)
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

// Host is the hostname of the search endpoint, which frontier budgets key on.
func (d *Driver) Host() string {
	return lens.Hostname(d.Spec.URL)
}

// SearchURL builds the search request for a query and market.
func (d *Driver) SearchURL(query, market string) string {
	return d.searchURL(query, market, nil)
}

func (d *Driver) searchURL(query, market string, without map[string]bool) string {
	vars := searchVars(query, market)
	values := url.Values{}
	for key, template := range d.Spec.Params {
		if without[key] {
			continue
		}
		values.Set(key, expand(template, func(name string) string { return vars[name] }))
	}
	if len(values) == 0 {
		return d.Spec.URL
	}
	return d.Spec.URL + "?" + values.Encode()
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

// Search runs one search against the host and returns its listings.
func (d *Driver) Search(ctx context.Context, fetcher *Fetcher, query, market string) ([]atlas.Listing, error) {
	if d.Spec == nil {
		return nil, fmt.Errorf("crawler: driver %s cannot search", d.Name)
	}
	items, err := d.fetchItems(ctx, fetcher, d.searchURL(query, market, nil))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && len(d.Spec.RetryWithout) > 0 {
		without := map[string]bool{}
		for _, key := range d.Spec.RetryWithout {
			without[key] = true
		}
		if items, err = d.fetchItems(ctx, fetcher, d.searchURL(query, market, without)); err != nil {
			return nil, err
		}
	}
	vars := searchVars(query, market)
	seen := map[string]bool{}
	var listings []atlas.Listing
	for _, raw := range items {
		if len(listings) >= d.Spec.Limit {
			break
		}
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if !d.requirementsMet(item) {
			continue
		}
		listing, ok := d.listingFrom(item, vars, market)
		if !ok || seen[listing.URL] {
			continue
		}
		seen[listing.URL] = true
		listings = append(listings, listing)
	}
	return listings, nil
}

func (d *Driver) fetchItems(ctx context.Context, fetcher *Fetcher, target string) ([]any, error) {
	response, err := fetcher.Get(ctx, target, d.Spec.Headers)
	if err != nil {
		return nil, fmt.Errorf("crawler: %s: %w", d.Name, err)
	}
	if response.Status < 200 || response.Status > 299 {
		return nil, fmt.Errorf("crawler: %s: HTTP %d from %s", d.Name, response.Status, lens.Hostname(target))
	}
	var document any
	if err := json.Unmarshal(response.Body, &document); err != nil {
		return nil, fmt.Errorf("crawler: %s: response is not JSON: %w", d.Name, err)
	}
	items, _ := lookup(document, d.Spec.Items).([]any)
	return items, nil
}

func (d *Driver) requirementsMet(item map[string]any) bool {
	for key, want := range d.Spec.Require {
		if scalarString(lookup(item, key)) != want {
			return false
		}
	}
	return true
}

func (d *Driver) listingFrom(item map[string]any, vars map[string]string, market string) (atlas.Listing, bool) {
	resolve := func(name string) string {
		if v, ok := vars[name]; ok {
			return v
		}
		return scalarString(lookup(item, name))
	}
	values := map[string]string{}
	for key, spec := range d.Spec.Fields {
		value, ok := spec.read(item, resolve)
		if !ok {
			return atlas.Listing{}, false
		}
		values[key] = value
	}
	for key, value := range d.Spec.Constants {
		if values[key] == "" {
			values[key] = value
		}
	}
	if strings.TrimSpace(values["title"]) == "" || values["url"] == "" {
		return atlas.Listing{}, false
	}
	if u, err := url.Parse(values["url"]); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return atlas.Listing{}, false
	}
	listing := atlas.Listing{
		Title:       strings.TrimSpace(values["title"]),
		URL:         values["url"],
		MediaURL:    values["media_url"],
		Provider:    d.Provider,
		SourceType:  values["source_type"],
		AccessType:  values["access_type"],
		Price:       values["price"],
		Market:      firstNonEmpty(values["market"], market),
		Duration:    values["duration"],
		Description: values["description"],
		Thumbnail:   values["thumbnail"],
	}
	if year, err := strconv.Atoi(values["year"]); err == nil {
		listing.Year = year
	}
	if d.Spec.Offers != nil {
		listing.Price, listing.AccessType = d.Spec.Offers.read(item)
	}
	if listing.AccessType == "" {
		listing.AccessType = "unknown"
	}
	return listing, true
}

// read evaluates a field. It reports false when a required match fails,
// which drops the item rather than recording a wrong URL.
func (f FieldSpec) read(item map[string]any, resolve func(string) string) (string, bool) {
	var value string
	switch {
	case f.Path != "":
		value = scalarString(lookup(item, f.Path))
	case len(f.Paths) > 0:
		for _, path := range f.Paths {
			if value = scalarString(lookup(item, path)); value != "" {
				break
			}
		}
	case f.Template != "":
		value = expand(f.Template, resolve)
	}
	value = transform(f.Transform, value)
	if value == "" {
		value = f.Default
	}
	if f.match != nil && !f.match.MatchString(value) {
		return "", false
	}
	return value, true
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

func transform(name, value string) string {
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
	}
	return value
}

// lookup walks a decoded JSON document by a dotted path with optional
// [index] steps, for example response.docs or items[0].title.
func lookup(document any, path string) any {
	current := document
	for _, step := range strings.Split(path, ".") {
		if step == "" {
			return nil
		}
		key, indices := step, []int(nil)
		for strings.HasSuffix(key, "]") {
			open := strings.LastIndex(key, "[")
			if open < 0 {
				return nil
			}
			index, err := strconv.Atoi(key[open+1 : len(key)-1])
			if err != nil {
				return nil
			}
			indices = append([]int{index}, indices...)
			key = key[:open]
		}
		if key != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = object[key]
		}
		for _, index := range indices {
			list, ok := current.([]any)
			if !ok || index < 0 || index >= len(list) {
				return nil
			}
			current = list[index]
		}
	}
	return current
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

// expand replaces {name} and {name|filter} in a template. Filters: lower,
// upper, path (URL path escaping) and query (URL query escaping). An unknown
// name expands to nothing.
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
		name, filter, _ := strings.Cut(template[open+1:open+close], "|")
		value := resolve(strings.TrimSpace(name))
		switch strings.TrimSpace(filter) {
		case "lower":
			value = strings.ToLower(value)
		case "upper":
			value = strings.ToUpper(value)
		case "path":
			value = url.PathEscape(value)
		case "query":
			value = url.QueryEscape(value)
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
