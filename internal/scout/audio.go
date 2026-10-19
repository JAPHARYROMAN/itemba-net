package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itemba-net/core/internal/crawler"
	"github.com/itemba-net/core/internal/lens"
)

// The audio search is the first Phase 3 domain. A query of kind audio fans
// out to the audio drivers, keeps the listings whose tags name the requested
// recording (Lens's audio identity: title, performer, duration), reads the
// Archive's item metadata for the kept listings' files and lengths, and
// checks that each file or page answers from the check location. No playback
// sample runs yet: the audio probe is a later slice, so no audio result is
// labelled playable here, and the ladder stops at identified.

const (
	// maxArchiveAudioMetadata bounds the metadata requests one search spends.
	maxArchiveAudioMetadata = 6
	// maxAudioPageChecks bounds the page checks one search spends on listings
	// without a known file.
	maxAudioPageChecks = 8
	// maxAudioCandidates caps the listings one search checks.
	maxAudioCandidates = 32
	// minAudioRelevance is the lowest score a listing may answer a query with.
	minAudioRelevance = 0.5
)

// audioExtensions are the audio containers the console's player decodes.
var audioExtensions = map[string]string{
	".mp3": "Direct MP3", ".flac": "Direct FLAC", ".ogg": "Direct Ogg audio", ".oga": "Direct Ogg audio", ".opus": "Direct Opus",
	".m4a": "Direct AAC", ".wav": "Direct WAV",
}

// audioWording turns the movie page check's wording into the recording's.
// Longer phrases come first because the replacer prefers the earliest
// pattern that matches at a position.
var audioWording = strings.NewReplacer(
	"Movie-page access and playback", "Access and playback",
	"public HTTP or HTTPS movie page", "public HTTP or HTTPS page",
	"Movie licensing", "Rights",
	"requested movie", "requested recording",
	"movie HTML page", "HTML page",
	"movie-page response", "page response",
	"movie access", "access",
	"movie link", "link",
	"movie page", "page",
	"The movie", "The recording",
	"movie", "recording",
)

// searchAudio answers an audio query. The region is kept for the report and
// the check location; audio drivers have no storefront markets yet.
func (s *Scout) searchAudio(ctx context.Context, query, region string, start time.Time) SearchReport {
	q := lens.ParseAs(query, lens.TypeAudio)
	report := SearchReport{Query: query, Kind: lens.TypeAudio, Region: region, Sources: []VerifiedSource{}, VerifiedSources: []VerifiedSource{}, PrunedSources: []VerifiedSource{}, Providers: []ProviderStatus{}}
	if q.TitleKey == "" {
		report.Providers = append(report.Providers, ProviderStatus{Name: "Audio search", Status: "empty", Detail: "Enter a recording's title, with its performer if known; audio pages are not inspected by URL yet."})
		report.DurationMs = time.Since(start).Milliseconds()
		return report
	}

	type checkLocation struct{ country, detail string }
	locationResult := make(chan checkLocation, 1)
	go func() {
		country, detail := s.availabilityCheckCountry(ctx)
		locationResult <- checkLocation{country, detail}
	}()

	candidates, providers := s.runDriverSearches(ctx, lens.TypeAudio, region, func(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, market string) ([]CandidateSource, ProviderStatus) {
		return s.queryAudioDriver(ctx, fetcher, d, q, market)
	})
	if len(providers) == 0 {
		providers = []ProviderStatus{{Name: "Audio catalogs", Status: "not_configured", Detail: "No audio driver is loaded."}}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	candidates = rankAudioCandidates(q, candidates)
	candidates = s.resolveArchiveAudio(ctx, candidates)

	location := checkLocation{}
	select {
	case location = <-locationResult:
	case <-ctx.Done():
		location.detail = "Check location could not be confirmed before the search deadline."
	}
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	pageChecks := map[int]bool{}
	for i, candidate := range candidates {
		if candidate.DirectStreamURL == "" && len(pageChecks) < maxAudioPageChecks {
			pageChecks[i] = true
		}
	}

	results := make([]VerifiedSource, len(candidates))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, candidate := range candidates {
		wg.Add(1)
		go func(idx int, c CandidateSource) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			checked := time.Now()
			if c.DirectStreamURL != "" {
				result := verifyFastHTTPWithClient(ctx, s.shield, c, client)
				if result.Verified {
					result.StreamFormat = audioStreamFormat(c.DirectStreamURL)
				} else {
					result.VerificationStatus = "listing"
					result.VerificationMode = "Listing; file check failed"
				}
				result.AvailabilityCheck = audioMediaAvailability(result, region, location.country)
				results[idx] = result
			} else {
				result := VerifiedSource{CandidateSource: c, DetectedManifests: []string{}, VerificationStatus: "listing", VerificationMode: "Provider listing", CheckedAt: checked.UTC().Format(time.RFC3339)}
				if pageChecks[idx] && ctx.Err() == nil {
					check := checkMoviePageAvailability(ctx, client, c, region, location.country)
					check.Detail = audioWording.Replace(check.Detail)
					result.AvailabilityCheck = &check
					result.VerifyDurationMs = time.Since(checked).Milliseconds()
					if check.Status == "page_reachable" {
						result.VerificationStatus = "page_reachable"
					}
				} else {
					result.AvailabilityCheck = uncheckedMovieAvailability(region, location.country, "Not checked within this search's bounded page-check budget. Open the page to check access.")
				}
				results[idx] = result
			}
			results[idx].RelevanceScore = int(audioRelevance(q, c)*100 + 0.5)
			if results[idx].CheckedAt == "" {
				results[idx].CheckedAt = checked.UTC().Format(time.RFC3339)
			}
		}(i, candidate)
	}
	wg.Wait()

	sources := make([]VerifiedSource, 0, len(results))
	verified := []VerifiedSource{}
	for _, result := range results {
		if result.Verified {
			verified = append(verified, result)
		}
		sources = append(sources, result)
	}
	// What a check proved, then relevance, then the drivers' own order.
	sort.SliceStable(sources, func(i, j int) bool {
		if a, b := audioStatusRank(sources[i]), audioStatusRank(sources[j]); a != b {
			return a > b
		}
		return sources[i].RelevanceScore > sources[j].RelevanceScore
	})
	report.Sources, report.VerifiedSources = sources, verified
	report.TotalCandidates, report.ListedCount, report.VerifiedCount = len(candidates), len(sources), len(verified)
	report.Providers = providers
	report.AvailabilityChecks = movieAvailabilityCoverage(region, location.country, location.detail, sources)
	report.DurationMs = time.Since(start).Milliseconds()
	return report
}

// queryAudioDriver runs one audio driver's search for the recording's title
// and keeps the listings whose tags answer the query.
func (s *Scout) queryAudioDriver(ctx context.Context, fetcher *crawler.Fetcher, d *crawler.Driver, q lens.ObjectQuery, market string) ([]CandidateSource, ProviderStatus) {
	status := ProviderStatus{Name: d.StatusName(market), Status: "ok"}
	listings, err := d.Search(ctx, fetcher, q.Title, market)
	if err != nil {
		status.Status, status.Detail = "error", "Provider unavailable; results may be incomplete."
		return nil, status
	}
	candidates := make([]CandidateSource, 0, len(listings))
	for _, listing := range listings {
		candidate := candidateFromListing(d, listing)
		if candidate.Kind == "" {
			candidate.Kind = lens.TypeAudio
		}
		if audioRelevance(q, candidate) < minAudioRelevance {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if d.Spec.Keep > 0 && len(candidates) > d.Spec.Keep {
		candidates = rankAudioCandidates(q, candidates)[:d.Spec.Keep]
	}
	status.Detail = fmt.Sprintf("%d catalog entries returned; access may vary", len(candidates))
	if len(candidates) == 0 {
		status.Status = "empty"
	}
	return candidates, status
}

// audioRelevance scores a candidate against the query by its tags.
func audioRelevance(q lens.ObjectQuery, c CandidateSource) float64 {
	return lens.AudioRelevance(q, lens.AudioListing(c.Title, c.Creator, c.Duration, c.ReleaseYear))
}

// rankAudioCandidates orders candidates by relevance, drops repeated URLs
// and keeps the cap. Equal relevance keeps the drivers' own order, which for
// the Archive is by downloads, so a popular edition precedes an obscure one.
func rankAudioCandidates(q lens.ObjectQuery, candidates []CandidateSource) []CandidateSource {
	sort.SliceStable(candidates, func(i, j int) bool {
		return audioRelevance(q, candidates[i]) > audioRelevance(q, candidates[j])
	})
	seen := map[string]bool{}
	out := make([]CandidateSource, 0, len(candidates))
	for _, c := range candidates {
		key := strings.TrimRight(c.URL, "/")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) == maxAudioCandidates {
			break
		}
	}
	return out
}

// audioStatusRank orders sources by what their check proved.
func audioStatusRank(source VerifiedSource) int {
	switch source.VerificationStatus {
	case "media_reachable":
		return 2
	case "page_reachable":
		return 1
	}
	return 0
}

// audioStreamFormat names a direct audio file's container for the console.
func audioStreamFormat(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if label, ok := audioExtensions[strings.ToLower(path.Ext(u.Path))]; ok {
			return label
		}
	}
	return "Direct audio"
}

// audioMediaAvailability reports a direct file's check in the audio's words.
func audioMediaAvailability(source VerifiedSource, region, country string) *AvailabilityCheck {
	check := uncheckedMovieAvailability(region, country, "The audio file did not answer with recognisable audio; access is unconfirmed.")
	check.Method, check.CheckedAt, check.EvidenceURL = "http_media_probe", source.CheckedAt, source.DirectStreamURL
	if source.VerificationStatus == "media_reachable" {
		check.Status = "media_reachable"
		check.Detail = "The audio file answered from the check location with recognisable audio. Rights and full playback remain unconfirmed."
	} else if source.PrunedReason != "" {
		check.Detail = "The audio file could not be confirmed: " + source.PrunedReason + ". Open the page to check access."
	}
	return check
}

// resolveArchiveAudio reads the Archive item metadata of the first kept
// Archive audio listings, in parallel and under the request budget, so a
// listing becomes a recording with files and a length.
func (s *Scout) resolveArchiveAudio(ctx context.Context, candidates []CandidateSource) []CandidateSource {
	client := s.availabilityHTTPClient
	if client == nil {
		client = newMovieAvailabilityClient()
	}
	var wg sync.WaitGroup
	budget := 0
	for i := range candidates {
		if candidates[i].SourceType != "Archive audio" || budget == maxArchiveAudioMetadata {
			continue
		}
		page, err := url.Parse(candidates[i].URL)
		if err != nil || page.Hostname() != "archive.org" || !strings.HasPrefix(page.Path, "/details/") {
			continue
		}
		budget++
		wg.Add(1)
		go func(index int, identifier string) {
			defer wg.Done()
			if resolved, ok := resolveArchiveAudioSource(ctx, client, candidates[index], identifier); ok {
				candidates[index] = resolved
			}
		}(i, strings.TrimPrefix(page.Path, "/details/"))
	}
	wg.Wait()
	return candidates
}

// archiveAudioFile is one playable file of an Archive audio item.
type archiveAudioFile struct {
	name     string
	duration float64
	label    string
}

// resolveArchiveAudioSource reads one item's public metadata. The item must
// be audio, public and not dark, and hold at least one audio file; the
// listing then carries the item's performer when its own was blank, the
// summed length of its files, and the first file in a container the console
// plays as its direct URL. The source stays a listing otherwise.
func resolveArchiveAudioSource(ctx context.Context, client *http.Client, source CandidateSource, identifier string) (CandidateSource, bool) {
	if !freeArchiveIdentifier.MatchString(identifier) {
		return source, false
	}
	metadataURL := "https://archive.org/metadata/" + url.PathEscape(identifier)
	data, final, err := fetchFreeSourceDocument(ctx, client, metadataURL)
	if err != nil || final.Hostname() != "archive.org" || final.Path != "/metadata/"+identifier {
		return source, false
	}
	var payload struct {
		IsDark   bool `json:"is_dark"`
		Metadata struct {
			Identifier string `json:"identifier"`
			Title      any    `json:"title"`
			MediaType  string `json:"mediatype"`
			Creator    any    `json:"creator"`
			Year       any    `json:"year"`
			Date       string `json:"date"`
			Restricted any    `json:"access-restricted-item"`
		} `json:"metadata"`
		Files []struct {
			Name    string `json:"name"`
			Format  string `json:"format"`
			Length  any    `json:"length"`
			Private any    `json:"private"`
		} `json:"files"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.IsDark || payload.Metadata.Identifier != identifier || payload.Metadata.MediaType != "audio" {
		return source, false
	}
	if restricted := scalarMetadataString(payload.Metadata.Restricted); strings.EqualFold(restricted, "true") || restricted == "1" || payload.Metadata.Restricted == true {
		return source, false
	}
	var files []archiveAudioFile
	total := 0.0
	for _, file := range payload.Files {
		ext := strings.ToLower(path.Ext(file.Name))
		label, playable := audioExtensions[ext]
		if !playable || file.Name == "" || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "\\") || path.Clean(file.Name) != file.Name || strings.HasPrefix(file.Name, "../") {
			continue
		}
		if file.Private == true || strings.EqualFold(scalarMetadataString(file.Private), "true") || scalarMetadataString(file.Private) == "1" {
			continue
		}
		duration := mediaDurationSeconds(scalarMetadataString(file.Length))
		files = append(files, archiveAudioFile{file.Name, duration, label})
	}
	if len(files) == 0 {
		return source, false
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].name < files[j].name })
	// One container per track: the item often holds the same tracks as MP3
	// and FLAC, so count and sum the preferred container only.
	preferred := ""
	for _, ext := range []string{".mp3", ".ogg", ".oga", ".opus", ".m4a", ".flac", ".wav"} {
		for _, file := range files {
			if strings.ToLower(path.Ext(file.name)) == ext {
				preferred = ext
				break
			}
		}
		if preferred != "" {
			break
		}
	}
	var tracks []archiveAudioFile
	for _, file := range files {
		if strings.ToLower(path.Ext(file.name)) == preferred {
			tracks = append(tracks, file)
			total += file.duration
		}
	}
	first := tracks[0]
	stream := &url.URL{Scheme: "https", Host: "archive.org", Path: "/download/" + identifier + "/" + first.name}
	source.DirectStreamURL, source.KnownManifests = stream.String(), []string{stream.String()}
	source.AccessType, source.AccessBasis, source.AccessEvidenceURL = "free", "archive_metadata", metadataURL
	source.AccessNote = fmt.Sprintf("%d public %s file(s) are listed in this item's Archive metadata. Rights vary by item; playback is not sampled yet.", len(tracks), strings.TrimPrefix(first.label, "Direct "))
	source.ListingOnly = false
	if total > 0 {
		// Summed track lengths carry float noise; hundredths are plenty.
		source.Duration = strconv.FormatFloat(math.Round(total*100)/100, 'f', -1, 64)
	}
	if source.Creator == "" {
		source.Creator = scalarMetadataString(payload.Metadata.Creator)
	}
	if source.ReleaseYear == 0 {
		if match := movieYearPattern.FindString(scalarMetadataString(payload.Metadata.Year)); match != "" {
			source.ReleaseYear, _ = strconv.Atoi(match)
		} else if match := movieYearPattern.FindString(payload.Metadata.Date); match != "" {
			source.ReleaseYear, _ = strconv.Atoi(match)
		}
	}
	if len(tracks) > 1 {
		source.Description = fmt.Sprintf("%d tracks, %s in all, from the Internet Archive item. The first track plays; the queue player arrives with a later slice.", len(tracks), lens.FormatDuration(total))
	} else {
		source.Description = fmt.Sprintf("One audio file, %s, from the Internet Archive item.", lens.FormatDuration(total))
	}
	return source, true
}
