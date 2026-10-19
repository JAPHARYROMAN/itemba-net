package crawler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CheckSpec is a provider access check: how to recognise one of the host's
// movie pages, the requests that establish what the host says about it, and
// what each answer means. The search pipeline runs it on a free listing
// before any native playback sample. The runtime fetches, reads and applies
// the driver's rules; what a page shows and whether declared metadata names
// the listing's film are the pipeline's judgments, supplied through a
// CheckEnvironment, so the runtime never guesses at film identity.
type CheckSpec struct {
	// URL is the regular expression a listing's page URL must match.
	URL string `yaml:"url"`
	// URLTitle captures, in its first group, a title slug from a pasted URL.
	URLTitle string `yaml:"url_title"`
	// TimeoutSeconds bounds the whole check. Default 6.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// Requests run in order; each applies its rules before the next runs.
	Requests []CheckRequestSpec `yaml:"requests"`
	// Otherwise is the result when no rule fired after the last request.
	Otherwise ResultSpec `yaml:"otherwise"`
	url       *regexp.Regexp
	urlTitle  *regexp.Regexp
}

// CheckRequestSpec is one request of a check. {page} is the listing's URL;
// earlier captures are available as variables, as are status, final_url,
// final_path, final_base and parsed once the request has answered.
type CheckRequestSpec struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers"`
	// Response is json, html or text. Default json.
	Response string `yaml:"response"`
	// TimeoutSeconds bounds this request. Default 4.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MaxBytes bounds the body; a longer answer fails the request. Default 512 KiB.
	MaxBytes int64 `yaml:"max_bytes"`
	// Redirects is public (followed to any public https host, the default),
	// same_path (only to the same path on the driver's hosts) or none.
	Redirects string `yaml:"redirects"`
	// Evidence is the URL reported for results decided at this request.
	// Default: the final URL, or the page while no request has answered.
	Evidence string `yaml:"evidence"`
	// Capture reads variables; a capture that fails leaves its variable
	// absent for the rules to judge.
	Capture map[string]CaptureSpec `yaml:"capture"`
	// Identity names the title, year and duration the host declared, for
	// the pipeline to compare with the listing; PageDuration is a second
	// runtime the asset's must agree with.
	Identity *IdentitySpec `yaml:"identity"`
	// Failure is the result when the request cannot be made or answers
	// outside its bounds; Timeout, when set, replaces it for a deadline.
	Failure ResultSpec  `yaml:"failure"`
	Timeout *ResultSpec `yaml:"timeout"`
	// Rules are judged in order after the request; the first that fires
	// ends the check with its result.
	Rules []RuleSpec `yaml:"rules"`
}

// IdentitySpec names what the host declared about the film, as templates.
type IdentitySpec struct {
	Title        string `yaml:"title"`
	Year         string `yaml:"year"`
	Duration     string `yaml:"duration"`
	PageDuration string `yaml:"page_duration"`
}

// RuleSpec fires when its When holds, or when its Unless does not.
type RuleSpec struct {
	When   *WhenSpec  `yaml:"when"`
	Unless *WhenSpec  `yaml:"unless"`
	Result ResultSpec `yaml:"result"`
}

// WhenSpec is a conjunction of tests on the request's outcome.
type WhenSpec struct {
	// Status is a pattern the HTTP status code must match.
	Status string `yaml:"status"`
	// FinalURL is a pattern the final URL must match; {name} placeholders
	// expand to the variable's value, quoted.
	FinalURL string `yaml:"final_url"`
	// Vars are patterns per variable; an absent variable is empty.
	Vars map[string]string `yaml:"vars"`
	// Page are facts the pipeline reads from an HTML answer: challenge,
	// sign_in and soft_not_found (true or false) and identity (match or
	// mismatch).
	Page map[string]string `yaml:"page"`
	// Metadata is match or mismatch: whether the identity the host declared
	// names the listing's film. Runtime is agree or disagree: whether the
	// asset's duration agrees with the page's within a minute or 5%.
	Metadata string `yaml:"metadata"`
	Runtime  string `yaml:"runtime"`
	// Range requires a numeric variable within [low, high].
	Range  map[string][]float64 `yaml:"range"`
	status *regexp.Regexp
}

// ResultSpec is what a check reports: the playback check's status and
// reason, a detail for the operator, the evidence URL and, for a host that
// handed over the main media, a binding for the protected sampler.
type ResultSpec struct {
	Status   string    `yaml:"status"`
	Reason   string    `yaml:"reason"`
	Detail   string    `yaml:"detail"`
	Evidence string    `yaml:"evidence"`
	Bind     *BindSpec `yaml:"bind"`
}

// BindSpec binds the listing to its main media. Media and Evidence are
// templates; SSAI is "true" when the stream carries server-inserted ads,
// which a sample cannot tell from the film.
type BindSpec struct {
	Media       string `yaml:"media"`
	Evidence    string `yaml:"evidence"`
	SSAI        string `yaml:"ssai"`
	SourceType  string `yaml:"source_type"`
	AccessBasis string `yaml:"access_basis"`
	AccessNote  string `yaml:"access_note"`
}

// CheckEnvironment supplies the pipeline's judgments to a check.
type CheckEnvironment interface {
	// PageFacts reads an HTML answer: challenge, sign_in and soft_not_found
	// as "true" or "false", identity as "match" or "mismatch".
	PageFacts(body []byte, final *url.URL) map[string]string
	// MetadataMatches reports whether a declared title, year and duration
	// name the listing's film.
	MetadataMatches(title string, year int, duration float64) bool
}

// CheckResult is a check's verdict.
type CheckResult struct {
	Status      string
	Reason      string
	Detail      string
	EvidenceURL string
	// Requests counts the requests attempted.
	Requests int
	// Metadata is what the host declared, adopted once it matched the listing.
	Metadata *CheckMetadata
	// Bind is set when the host handed over the main media.
	Bind *CheckBinding
}

// CheckMetadata is a film identity a host declared.
type CheckMetadata struct {
	Title    string
	Year     int
	Duration float64
}

// CheckBinding is the main media a host handed over for a listing.
type CheckBinding struct {
	Media       string
	Evidence    string
	SSAI        bool
	SourceType  string
	AccessBasis string
	AccessNote  string
}

var (
	checkStatuses = map[string]bool{"unconfirmed": true, "not_started": true}
	checkReasons  = map[string]bool{"content_unconfirmed": true, "region_unavailable": true, "sign_in_required": true, "trailer_only": true, "drm_required": true, "timeout": true}
	pageFacts     = map[string]bool{"challenge": true, "sign_in": true, "soft_not_found": true, "identity": true}
)

func (c *CheckSpec) validate() error {
	var err error
	if c.URL == "" {
		return errors.New("check.url is required")
	}
	if c.url, err = regexp.Compile(c.URL); err != nil {
		return fmt.Errorf("check.url: %w", err)
	}
	if c.URLTitle != "" {
		if c.urlTitle, err = regexp.Compile(c.URLTitle); err != nil {
			return fmt.Errorf("check.url_title: %w", err)
		}
		if c.urlTitle.NumSubexp() < 1 {
			return errors.New("check.url_title needs a capturing group")
		}
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 6
	}
	if len(c.Requests) == 0 {
		return errors.New("check.requests needs at least one request")
	}
	for i := range c.Requests {
		if err := c.Requests[i].validate(); err != nil {
			return fmt.Errorf("check.requests[%d]: %w", i, err)
		}
	}
	if err := c.Otherwise.validate(); err != nil {
		return fmt.Errorf("check.otherwise: %w", err)
	}
	return nil
}

func (r *CheckRequestSpec) validate() error {
	if strings.TrimSpace(r.URL) == "" {
		return errors.New("url is required")
	}
	if r.Response == "" {
		r.Response = "json"
	}
	if !responses[r.Response] {
		return fmt.Errorf("response %q must be json, html or text", r.Response)
	}
	if r.TimeoutSeconds <= 0 {
		r.TimeoutSeconds = 4
	}
	if r.MaxBytes <= 0 {
		r.MaxBytes = 512 << 10
	}
	switch r.Redirects {
	case "", "public":
		r.Redirects = "public"
	case "none", "same_path":
	default:
		return fmt.Errorf("redirects %q must be public, same_path or none", r.Redirects)
	}
	for name, capture := range r.Capture {
		if !captureName.MatchString(name) {
			return fmt.Errorf("capture %q needs a simple name", name)
		}
		if err := capture.validate(r.Response); err != nil {
			return fmt.Errorf("capture %s: %w", name, err)
		}
		r.Capture[name] = capture
	}
	if r.Identity != nil && (r.Identity.Title == "" || r.Identity.Duration == "") {
		return errors.New("identity needs title and duration templates")
	}
	if err := r.Failure.validate(); err != nil {
		return fmt.Errorf("failure: %w", err)
	}
	if r.Timeout != nil {
		if err := r.Timeout.validate(); err != nil {
			return fmt.Errorf("timeout: %w", err)
		}
	}
	for i := range r.Rules {
		rule := &r.Rules[i]
		if (rule.When == nil) == (rule.Unless == nil) {
			return fmt.Errorf("rules[%d] needs when or unless", i)
		}
		condition := rule.When
		if condition == nil {
			condition = rule.Unless
		}
		if err := condition.validate(); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		if err := rule.Result.validate(); err != nil {
			return fmt.Errorf("rules[%d].result: %w", i, err)
		}
	}
	return nil
}

func (w *WhenSpec) validate() error {
	var err error
	tests := 0
	if w.Status != "" {
		tests++
		if w.status, err = regexp.Compile(w.Status); err != nil {
			return fmt.Errorf("status: %w", err)
		}
	}
	if w.FinalURL != "" {
		tests++
		if _, err = compileWith(w.FinalURL, nil); err != nil {
			return fmt.Errorf("final_url: %w", err)
		}
	}
	for name, pattern := range w.Vars {
		tests++
		if _, err = compileWith(pattern, nil); err != nil {
			return fmt.Errorf("vars.%s: %w", name, err)
		}
	}
	for name, value := range w.Page {
		tests++
		if !pageFacts[name] {
			return fmt.Errorf("page.%s is not a fact the pipeline reports", name)
		}
		if value == "" {
			return fmt.Errorf("page.%s needs a value", name)
		}
	}
	if w.Metadata != "" {
		tests++
		if w.Metadata != "match" && w.Metadata != "mismatch" {
			return errors.New("metadata must be match or mismatch")
		}
	}
	if w.Runtime != "" {
		tests++
		if w.Runtime != "agree" && w.Runtime != "disagree" && w.Runtime != "unknown" {
			return errors.New("runtime must be agree, disagree or unknown")
		}
	}
	for name, bounds := range w.Range {
		tests++
		if len(bounds) != 2 || bounds[0] > bounds[1] {
			return fmt.Errorf("range.%s needs two numbers, low then high", name)
		}
	}
	if tests == 0 {
		return errors.New("a condition needs something to test")
	}
	return nil
}

func (r *ResultSpec) validate() error {
	if r.Status == "" {
		r.Status = "unconfirmed"
	}
	if !checkStatuses[r.Status] {
		return fmt.Errorf("status %q must be unconfirmed or not_started", r.Status)
	}
	if r.Reason == "" {
		r.Reason = "content_unconfirmed"
	}
	if !checkReasons[r.Reason] {
		return fmt.Errorf("reason %q is not one the pipeline reports", r.Reason)
	}
	if r.Bind != nil && r.Bind.Media == "" {
		return errors.New("bind needs a media template")
	}
	return nil
}

// compileWith compiles a pattern whose {name} placeholders stand for the
// variables' values, quoted, so a captured value is matched literally.
func compileWith(pattern string, vars map[string]string) (*regexp.Regexp, error) {
	return regexp.Compile(expand(pattern, func(name string) string { return regexp.QuoteMeta(vars[name]) }))
}

// Checks reports whether the driver's check applies to a page URL.
func (d *Driver) Checks(page string) bool {
	return d.Check != nil && d.Check.url.MatchString(page)
}

// URLTitle is the title slug the check reads from a pasted URL, if any.
func (d *Driver) URLTitle(page string) string {
	if d.Check == nil || d.Check.urlTitle == nil {
		return ""
	}
	match := d.Check.urlTitle.FindStringSubmatch(page)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

type checkState struct {
	vars     map[string]string
	facts    map[string]string
	metadata string
	runtime  string
}

// RunCheck performs the access check on a page and returns its verdict. It
// never errors: a request that cannot be made is itself a verdict.
func (d *Driver) RunCheck(ctx context.Context, fetcher *Fetcher, page string, env CheckEnvironment) CheckResult {
	spec := d.Check
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()
	pageURL, err := url.Parse(page)
	if err != nil {
		pageURL = &url.URL{}
	}
	state := checkState{vars: map[string]string{"page": page, "page_path": pageURL.Path, "page_host": pageURL.Hostname(), "page_base": path.Base(pageURL.Path)}}
	resolve := func(name string) string { return state.vars[name] }
	var metadata *CheckMetadata
	requests := 0
	for i := range spec.Requests {
		req := &spec.Requests[i]
		for _, name := range []string{"status", "final_url", "final_path", "final_base", "parsed"} {
			delete(state.vars, name)
		}
		for name := range req.Capture {
			delete(state.vars, name)
		}
		target := expand(req.URL, resolve)
		state.vars["url"] = target
		if u, err := url.Parse(target); err != nil || u.Scheme != "https" || u.Host == "" {
			return d.checkResult(req.Failure, req, state, metadata, requests)
		}
		headers := map[string]string{}
		for key, template := range req.Headers {
			headers[key] = expand(template, resolve)
		}
		opts := FetchOptions{MaxBody: req.MaxBytes, MaxRedirects: 5}
		switch req.Redirects {
		case "none":
			opts = FetchOptions{MaxBody: req.MaxBytes, NoRedirect: true}
		case "same_path":
			opts = FetchOptions{MaxBody: req.MaxBytes, MaxRedirects: 3, SamePath: true, Hosts: d.Hosts}
		}
		requests++
		reqCtx, cancelReq := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
		response, err := fetcher.Fetch(reqCtx, target, headers, opts)
		cancelReq()
		if err != nil {
			result := req.Failure
			if req.Timeout != nil && timedOut(err) {
				result = *req.Timeout
			}
			return d.checkResult(result, req, state, metadata, requests)
		}
		final, err := url.Parse(response.FinalURL)
		if err != nil || final.Host == "" {
			final, _ = url.Parse(target)
		}
		state.vars["status"] = strconv.Itoa(response.Status)
		state.vars["final_url"] = final.String()
		state.vars["final_path"] = final.Path
		state.vars["final_base"] = path.Base(final.Path)
		doc := parseFetched(req.Response, response.Body, final, response.Status)
		state.vars["parsed"] = strconv.FormatBool(doc.parsed)
		captured, _ := readCaptures(req.Capture, doc, state.vars)
		for name, value := range captured {
			state.vars[name] = value
		}
		state.facts = nil
		if env != nil && req.Response == "html" && doc.parsed {
			state.facts = env.PageFacts(response.Body, final)
		}
		state.metadata, state.runtime = "", ""
		var declared *CheckMetadata
		if req.Identity != nil {
			declared = &CheckMetadata{Title: strings.TrimSpace(expand(req.Identity.Title, resolve))}
			declared.Year, _ = strconv.Atoi(expand(req.Identity.Year, resolve))
			declared.Duration, _ = strconv.ParseFloat(expand(req.Identity.Duration, resolve), 64)
			state.metadata = "mismatch"
			if env != nil && declared.Title != "" && env.MetadataMatches(declared.Title, declared.Year, declared.Duration) {
				state.metadata = "match"
			}
			state.runtime = "unknown"
			if req.Identity.PageDuration != "" {
				pageDuration, _ := strconv.ParseFloat(expand(req.Identity.PageDuration, resolve), 64)
				if pageDuration > 0 && declared.Duration > 0 {
					state.runtime = "disagree"
					if math.Abs(declared.Duration-pageDuration) <= math.Max(60, pageDuration*0.05) {
						state.runtime = "agree"
					}
				}
			}
		}
		for _, rule := range req.Rules {
			fired := false
			if rule.When != nil {
				fired = rule.When.holds(state)
			} else {
				fired = !rule.Unless.holds(state)
			}
			if fired {
				return d.checkResult(rule.Result, req, state, metadata, requests)
			}
		}
		// The host's identity is adopted once it matched and nothing in this
		// request stopped the check; a later restriction keeps it.
		if declared != nil && state.metadata == "match" {
			metadata = declared
		}
	}
	last := &spec.Requests[len(spec.Requests)-1]
	return d.checkResult(spec.Otherwise, last, state, metadata, requests)
}

func (d *Driver) checkResult(spec ResultSpec, req *CheckRequestSpec, state checkState, metadata *CheckMetadata, requests int) CheckResult {
	resolve := func(name string) string { return state.vars[name] }
	evidence := spec.Evidence
	if evidence == "" {
		evidence = req.Evidence
	}
	if evidence == "" {
		evidence = "{final_url}"
	}
	result := CheckResult{Status: spec.Status, Reason: spec.Reason, Detail: expand(spec.Detail, resolve), EvidenceURL: expand(evidence, resolve), Requests: requests, Metadata: metadata}
	if result.EvidenceURL == "" {
		result.EvidenceURL = state.vars["page"]
	}
	if spec.Bind != nil {
		media := expand(spec.Bind.Media, resolve)
		if u, err := url.Parse(media); err == nil && u.Scheme == "https" && u.Host != "" {
			bindEvidence := spec.Bind.Evidence
			if bindEvidence == "" {
				bindEvidence = evidence
			}
			result.Bind = &CheckBinding{
				Media: media, Evidence: expand(bindEvidence, resolve), SSAI: expand(spec.Bind.SSAI, resolve) == "true",
				SourceType: spec.Bind.SourceType, AccessBasis: spec.Bind.AccessBasis, AccessNote: expand(spec.Bind.AccessNote, resolve),
			}
		} else if result.Detail == "" {
			result.Detail = "The provider did not supply a public https stream for the film."
		}
	}
	return result
}

func (w *WhenSpec) holds(state checkState) bool {
	if w.status != nil && !w.status.MatchString(state.vars["status"]) {
		return false
	}
	if w.FinalURL != "" {
		re, err := compileWith(w.FinalURL, state.vars)
		if err != nil || !re.MatchString(state.vars["final_url"]) {
			return false
		}
	}
	for name, pattern := range w.Vars {
		re, err := compileWith(pattern, state.vars)
		if err != nil || !re.MatchString(state.vars[name]) {
			return false
		}
	}
	for name, want := range w.Page {
		if state.facts[name] != want {
			return false
		}
	}
	if w.Metadata != "" && state.metadata != w.Metadata {
		return false
	}
	if w.Runtime != "" && state.runtime != w.Runtime {
		return false
	}
	for name, bounds := range w.Range {
		number, err := strconv.ParseFloat(state.vars[name], 64)
		if err != nil || number < bounds[0] || number > bounds[1] {
			return false
		}
	}
	return true
}

func timedOut(err error) bool {
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
}
