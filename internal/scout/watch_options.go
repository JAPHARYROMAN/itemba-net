package scout

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Provider guidance checked 2026-10-18. Service-level evidence never establishes
// that a particular film is licensed in a country or that playback will work.
const (
	netflixMarketEvidence   = "https://help.netflix.com/en/node/14164"
	netflixAccessEvidence   = "https://help.netflix.com/en/node/24926"
	appleMarketEvidence     = "https://support.apple.com/en-us/118205"
	primeMarketEvidence     = "https://www.primevideo.com/help?nodeId=GD5REBNJD74BURF6"
	primeTanzaniaEvidence   = "https://press.aboutamazon.com/uk/2016/12/amazon-prime-video-now-available-in-more-than-200-countries-and-territories-around-the-world"
	tubiMarketEvidence      = "https://tubitv.com/help-center/About-Tubi/articles/4409969055259"
	disneyAccessEvidence    = "https://www.disneyplus.com/"
	huluAccessEvidence      = "https://www.hulu.com/welcome"
	peacockAccessEvidence   = "https://www.peacocktv.com/plans/base"
	maxAccessEvidence       = "https://help.hbomax.com/US/Answer/Detail/000002547"
	paramountAccessEvidence = "https://www.paramountplus.com/"
	fandangoAccessEvidence  = "https://athome.fandango.com/"
	rakutenAccessEvidence   = "https://support.rakuten.tv/hc/en-us/articles/360010788614-First-Steps-at-Rakuten-TV"
	plutoAccessEvidence     = "https://pluto.tv/"
	filmzieAccessEvidence   = "https://filmzie.zendesk.com/hc/en-us/articles/360006496100-How-much-does-Filmzie-cost"
)

var movieUUIDSuffix = regexp.MustCompile(`(?:^|-)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var movieLocalePart = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]{2})?$`)
var moviePositiveID = regexp.MustCompile(`^[1-9][0-9]*$`)
var movieOpaqueID = regexp.MustCompile(`^[a-z0-9_-]{8,100}$`)

func movieServiceHost(host, domain string, extra ...string) bool {
	if host == domain || host == "www."+domain {
		return true
	}
	for _, allowed := range extra {
		if host == allowed {
			return true
		}
	}
	return false
}

// Provider routes are matched as a whole, allowing only locale prefixes.
// Consequently /blog/movies/title, search pages, trailers and review subpages
// cannot become watch links merely by containing a movie-shaped path segment.
func movieProviderRoute(parts []string, route ...string) []string {
	for prefix := 0; prefix <= 2 && prefix+len(route) < len(parts); prefix++ {
		validPrefix := true
		for _, part := range parts[:prefix] {
			validPrefix = validPrefix && (movieLocalePart.MatchString(part) || part == "intl")
		}
		if !validPrefix {
			continue
		}
		matched := true
		for i, part := range route {
			matched = matched && parts[prefix+i] == part
		}
		if matched {
			tail := parts[prefix+len(route):]
			for _, part := range tail {
				if movieGenericRoute(part) || part == "trailer" || part == "trailers" || part == "clips" || part == "review" || part == "reviews" || part == "episodes" {
					return nil
				}
			}
			return tail
		}
	}
	return nil
}

func moviePrimaryOfferEvidence(c CandidateSource) bool {
	if c.AccessBasis != "page_text" || c.AccessEvidenceURL != c.URL {
		return false
	}
	switch c.AccessType {
	case "free", "rent", "buy", "paid", "subscription":
		return true
	}
	return false
}

func movieWebURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	return u, err == nil && u != nil && u.User == nil && u.Hostname() != "" && (u.Scheme == "https" || u.Scheme == "http")
}

func movieHostIs(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func movieGuideHost(host string) bool {
	for _, domain := range []string{
		"imdb.com", "imdb.to", "wikipedia.org", "wikidata.org", "justwatch.com", "reelgood.com",
		"flicks.com", "flicks.com.au", "flicks.co.uk", "flicks.co.nz", "useflickd.com", "useflickd.app",
		"ontvtonight.com", "rottentomatoes.com", "metacritic.com", "letterboxd.com", "themoviedb.org",
		"tmdb.org", "trakt.tv", "moviefone.com", "decider.com", "watchmode.com", "yidio.com", "tvguide.com", "playpilot.com",
	} {
		if movieHostIs(host, domain) {
			return true
		}
	}
	return false
}

func movieCatalogEvidence(c CandidateSource) bool {
	switch c.SourceType {
	case "Movie Store", "Archive video", "Public Domain", "Public Domain feature", "Open media", "HLS Reference", "Federated PeerTube":
		return true
	}
	return strings.HasPrefix(c.ID, "ref-")
}

func movieMediaEvidence(c CandidateSource) bool {
	if _, ok := movieWebURL(c.DirectStreamURL); ok {
		return true
	}
	for _, stream := range c.KnownManifests {
		if _, ok := movieWebURL(stream); ok {
			return true
		}
	}
	return false
}

func movieGenericRoute(part string) bool {
	switch part {
	case "search", "browse", "home", "shop", "shopping", "store", "storefront", "tickets", "ticket", "showtimes", "products", "product", "catalog", "collections", "categories", "genres", "schedule":
		return true
	}
	return false
}

func movieRoute(parts []string, routes ...string) bool {
	for i, part := range parts {
		for _, route := range routes {
			if part == route && i+1 < len(parts) && !movieGenericRoute(parts[i+1]) {
				return true
			}
		}
	}
	return false
}

func movieAmazonHost(host string) bool {
	for _, domain := range []string{"amazon.com", "amazon.co.uk", "amazon.de", "amazon.fr", "amazon.it", "amazon.es", "amazon.co.jp", "amazon.in", "amazon.com.br", "amazon.ca", "amazon.com.au", "amazon.com.mx", "amazon.nl", "amazon.pl", "amazon.se", "amazon.com.be", "amazon.com.tr", "amazon.ae", "amazon.sa", "amazon.eg", "amazon.sg", "amazon.co.za"} {
		if movieHostIs(host, domain) {
			return true
		}
	}
	return false
}

// A clear individual title/player route is required. A general catalog, guide,
// product page, or search engine result is not a movie destination.
func movieDestination(c CandidateSource, u *url.URL) (string, bool) {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	parts := strings.FieldsFunc(strings.ToLower(u.Path), func(r rune) bool { return r == '/' })
	if movieGuideHost(host) || movieHostIs(host, "showmax.com") || movieHostIs(host, "showmax.tv") {
		return "", false
	}
	if host == "amctheatres.com" || host == "www.amctheatres.com" {
		return "", false
	}
	if movieHostIs(host, "dstv.com") && strings.Contains("/"+strings.Join(parts, "/")+"/", "/showmax/") {
		return "", false
	}
	if _, supported := additionalFreeResolutionTarget(u); supported {
		// Exact movie routes remain usable listings if bounded metadata
		// resolution fails. Access stays unknown until separately evidenced.
		return "catalog", true
	}
	switch {
	case movieHostIs(host, "netflix.com"):
		return "netflix", movieRoute(parts, "title", "watch")
	case movieHostIs(host, "primevideo.com"):
		return "prime", movieRoute(parts, "detail")
	case movieAmazonHost(host):
		for i, part := range parts {
			if part == "video" && i+1 < len(parts) && !movieGenericRoute(parts[i+1]) && movieRoute(parts[i+1:], "detail", "d") {
				return "prime", true
			}
		}
		return "", false
	case host == "tv.apple.com" || host == "itunes.apple.com":
		return "apple", movieRoute(parts, "movie")
	case movieHostIs(host, "youtube.com"):
		return "youtube", len(parts) == 1 && parts[0] == "watch" && strings.TrimSpace(u.Query().Get("v")) != "" || movieRoute(parts, "embed", "live")
	case host == "youtu.be":
		return "youtube", len(parts) == 1 && !movieGenericRoute(parts[0])
	case movieHostIs(host, "archive.org"):
		return "archive", movieRoute(parts, "embed", "details") || movieCatalogEvidence(c) && movieMediaEvidence(c)
	case movieHostIs(host, "tubitv.com"):
		return "tubi", movieRoute(parts, "movies")
	case movieHostIs(host, "disneyplus.com"):
		if !movieServiceHost(host, "disneyplus.com") {
			return "disney", false
		}
		if tail := movieProviderRoute(parts, "browse"); len(tail) == 1 && strings.HasPrefix(tail[0], "entity-") && movieUUIDSuffix.MatchString(strings.TrimPrefix(tail[0], "entity-")) {
			return "disney", true
		}
		tail := movieProviderRoute(parts, "movies")
		return "disney", len(tail) == 2 && movieOpaqueID.MatchString(tail[1])
	case movieHostIs(host, "hulu.com"):
		tail := movieProviderRoute(parts, "movie")
		return "hulu", movieServiceHost(host, "hulu.com") && len(tail) == 1 && movieUUIDSuffix.MatchString(tail[0])
	case movieHostIs(host, "peacocktv.com"):
		if !movieServiceHost(host, "peacocktv.com") {
			return "peacock", false
		}
		if tail := movieProviderRoute(parts, "stream-movies"); len(tail) == 1 {
			return "peacock", true
		}
		tail := movieProviderRoute(parts, "watch-online", "movies")
		return "peacock", len(tail) == 3 && movieUUIDSuffix.MatchString(tail[2])
	case movieHostIs(host, "hbomax.com") || movieHostIs(host, "max.com"):
		if !movieServiceHost(host, "hbomax.com", "play.hbomax.com") && !movieServiceHost(host, "max.com", "play.max.com") {
			return "max", false
		}
		for _, route := range []string{"movie", "movies"} {
			if tail := movieProviderRoute(parts, route); (len(tail) == 1 || len(tail) == 2) && movieUUIDSuffix.MatchString(tail[len(tail)-1]) {
				return "max", true
			}
		}
		return "max", false
	case movieHostIs(host, "paramountplus.com"):
		if !movieServiceHost(host, "paramountplus.com") {
			return "paramount", false
		}
		if tail := movieProviderRoute(parts, "movies", "video"); len(tail) == 1 && movieOpaqueID.MatchString(tail[0]) {
			return "paramount", true
		}
		tail := movieProviderRoute(parts, "movies")
		return "paramount", len(tail) == 2 && movieUUIDSuffix.MatchString(tail[1])
	case movieHostIs(host, "athome.fandango.com") || movieHostIs(host, "vudu.com"):
		tail := movieProviderRoute(parts, "content", "browse", "details")
		return "fandango", (movieServiceHost(host, "athome.fandango.com") || movieServiceHost(host, "vudu.com")) && len(tail) == 2 && moviePositiveID.MatchString(tail[1])
	case movieHostIs(host, "fandango.com"):
		return "", false // The theater-ticket site is separate from Fandango at Home.
	case movieHostIs(host, "rakuten.tv"):
		tail := movieProviderRoute(parts, "movies")
		return "rakuten", movieServiceHost(host, "rakuten.tv") && len(tail) == 1
	case movieHostIs(host, "pluto.tv"):
		if !movieServiceHost(host, "pluto.tv") {
			return "pluto", false
		}
		for _, route := range [][]string{{"on-demand", "movies"}, {"watch", "on-demand", "movies"}} {
			if tail := movieProviderRoute(parts, route...); len(tail) == 1 || len(tail) == 2 && tail[1] == "details" {
				return "pluto", true
			}
		}
		return "pluto", false
	case movieHostIs(host, "filmzie.com"):
		tail := movieProviderRoute(parts, "content")
		return "filmzie", movieServiceHost(host, "filmzie.com") && len(tail) == 1
	case host == "plus.nasa.gov":
		return "nasa", nasaMovieTarget(c)
	case host == "fawesome.tv" || host == "www.fawesome.tv":
		return "fawesome", fawesomeMovieTarget(c)
	case host == "nfb.ca" || host == "www.nfb.ca" || host == "onf.ca" || host == "www.onf.ca":
		return "nfb", nfbMovieTarget(c)
	case host == "sedermasochism.com" || host == "www.sedermasochism.com" || host == "catsuka.com" || host == "www.catsuka.com":
		return "creator", creatorMovieTarget(c)
	case movieHostIs(host, "mubi.com"):
		return "mubi", movieRoute(parts, "films")
	case movieHostIs(host, "hotstar.com") || movieHostIs(host, "jiohotstar.com"):
		return "hotstar", movieRoute(parts, "movies")
	case movieHostIs(host, "dstv.com"):
		return "dstv", movieRoute(parts, "watch") && movieRoute(parts, "movie", "movies")
	case movieHostIs(host, "plex.tv"):
		// A /movie page can be an external-service guide or a rental. Only
		// exact primary offer evidence distinguishes an actual Plex watch option.
		tail := movieProviderRoute(parts, "movie")
		return "plex", host == "watch.plex.tv" && len(tail) == 1 && (movieMediaEvidence(c) || c.AccessType != "subscription" && moviePrimaryOfferEvidence(c))
	}
	if movieCatalogEvidence(c) && movieMediaEvidence(c) {
		return "catalog", true
	}
	for _, part := range parts {
		if movieGenericRoute(part) {
			return "", false
		}
	}
	return "unknown", movieRoute(parts, "watch", "movie", "movies", "video", "videos", "player")
}

func movieCountry(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	// ISO country codes distinguish a localized market from a language-only
	// route such as /en/. Country search hints and a site's TLD are not evidence.
	const codes = " AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG EH ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS YE YT ZA ZM ZW "
	if len(value) == 2 && strings.Contains(codes, " "+value+" ") {
		return value
	}
	return ""
}

func movieURLMarket(u *url.URL, brand string) string {
	if brand != "apple" && brand != "netflix" && brand != "mubi" && brand != "dstv" && brand != "tubi" && brand != "hotstar" && brand != "disney" && brand != "hulu" && brand != "peacock" && brand != "max" && brand != "paramount" && brand != "rakuten" && brand != "pluto" && brand != "filmzie" && brand != "plex" {
		return ""
	}
	parts := strings.FieldsFunc(strings.ToLower(u.Path), func(r rune) bool { return r == '/' })
	for i, part := range parts {
		if part == "title" || part == "watch" || part == "movie" || part == "movies" || part == "films" || part == "browse" || part == "content" || part == "stream-movies" || part == "on-demand" {
			break
		}
		// Mubi and Tubi use language-only prefixes such as /de/ and /es/;
		// those are not evidence of Germany or Spain as the offered market.
		languagePrefix := brand == "mubi" || brand == "tubi" || brand == "disney" || brand == "hulu" || brand == "peacock" || brand == "max" || brand == "paramount" || brand == "filmzie" || brand == "plex"
		languageOnly := languagePrefix && i == 0 && strings.Contains(" en es fr de pt it tr ar ja ko zh hi sw ru nl pl sv da no fi ", " "+part+" ")
		if part == "uk" && (brand == "rakuten" || brand == "pluto") {
			return "GB"
		}
		if market := movieCountry(part); market != "" && !languageOnly {
			return market
		}
		locale := strings.Split(part, "-")
		if len(locale) == 2 {
			if market := movieCountry(locale[1]); market != "" {
				return market
			}
			if market := movieCountry(locale[0]); market != "" {
				return market
			}
		}
	}
	return ""
}

func movieRegionalRestriction(description string) bool {
	description = strings.ToLower(description)
	for _, phrase := range []string{"not available in your country", "not available in your region", "unavailable in your country", "unavailable in your region", "not available for viewing in your location"} {
		if strings.Contains(description, phrase) {
			return true
		}
	}
	return false
}

func enrichMovieAccess(c *CandidateSource, brand string) {
	if c.AccessType == "free" && providerPlaybackBinding(*c) {
		return
	}
	if c.AccessType == "free" && (c.AccessBasis == "archive_metadata" || c.AccessBasis == "peertube_api" || c.AccessBasis == "arcoiris_video_metadata") {
		page, pageOK := safeAvailabilityURL(c.URL)
		media, mediaOK := safeAvailabilityURL(c.DirectStreamURL)
		if pageOK && mediaOK && resolvedFreePlaybackPair(*c, page, media) {
			// Retain the exact public metadata document associating this movie
			// page with its media. Playback eligibility validates it again.
			return
		}
	}
	if movieCatalogEvidence(*c) || c.Price != "" {
		c.AccessBasis, c.AccessEvidenceURL = "catalog", c.URL
		if c.AccessType == "" {
			c.AccessType = "unknown"
		}
		return
	}
	if moviePrimaryOfferEvidence(*c) {
		return
	}
	c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "unknown", "unknown", ""
	switch brand {
	case "netflix":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "subscription", "provider_model", netflixAccessEvidence
		c.AccessNote = "Subscription service. This title's inclusion and the applicable plan are unconfirmed."
	case "prime":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "paid", "provider_model", primeMarketEvidence
		c.AccessNote = "May require a subscription, rental, or purchase. This title's offer and price are unconfirmed."
	case "apple":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "paid", "provider_model", appleMarketEvidence
		c.AccessNote = "A movie purchase or subscription may be required. This title's offer and price are unconfirmed; rental availability is not established."
	case "tubi":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "free", "provider_model", tubiMarketEvidence
		c.AccessNote = "Free service supported by ads. This title's inclusion and regional access are unconfirmed."
	case "disney", "hulu", "peacock", "max", "paramount":
		evidence := map[string]string{"disney": disneyAccessEvidence, "hulu": huluAccessEvidence, "peacock": peacockAccessEvidence, "max": maxAccessEvidence, "paramount": paramountAccessEvidence}
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "subscription", "provider_model", evidence[brand]
		c.AccessNote = "Subscription service. This title's current offer, applicable plan, regional access and playback are unconfirmed."
		if brand == "hulu" {
			c.AccessNote = "Subscription service; some movies require an add-on or Live TV plan. This title's offer, regional access and playback are unconfirmed."
		}
	case "pluto", "filmzie":
		evidence := plutoAccessEvidence
		if brand == "filmzie" {
			evidence = filmzieAccessEvidence
		}
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "free", "provider_model", evidence
		c.AccessNote = "Free service supported by ads. This title's current inclusion, regional access and playback are unconfirmed."
	case "nasa":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "free", "provider_model", "https://www.nasa.gov/ways-to-watch/"
		c.AccessNote = "NASA+ is free without a subscription. This video's full movie identity and playback are checked separately."
	case "fawesome":
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "free", "provider_model", "https://fawesome.tv/"
		c.AccessNote = "Free service supported by ads. The matching full movie and protected playback are checked separately."
	case "fandango", "rakuten":
		evidence := fandangoAccessEvidence
		if brand == "rakuten" {
			evidence = rakutenAccessEvidence
		}
		c.AccessType, c.AccessBasis, c.AccessEvidenceURL = "unknown", "provider_model", evidence
		c.AccessNote = "Offers rentals and purchases, with selected movies free with ads. This title's free or paid offer, price, regional access and playback are unconfirmed."
	case "youtube", "plex":
		c.AccessNote = "Individual source page. Free or paid access, full-movie availability, and playback are unconfirmed."
	}
}

func enrichMovieAvailability(c *CandidateSource, u *url.URL, brand, region string) {
	c.AvailabilityStatus, c.AvailabilityNote, c.AvailabilityEvidenceURL = "unknown", "Regional title availability and playback are unconfirmed.", ""
	market := movieCountry(region)
	sourceMarket, urlMarket := movieCountry(c.Region), movieURLMarket(u, brand)
	if sourceMarket == "" && urlMarket != "" {
		// Preserve an explicit localized URL's market for display. This only
		// identifies the offer's context, not whether the film plays there.
		c.Region = urlMarket
	}
	if market == "" {
		// Preserve each offer's market for display without giving it a local
		// preference, or treating a global search as worldwide availability.
		return
	}
	if market == "TZ" && brand == "tubi" {
		c.AvailabilityStatus, c.AvailabilityEvidenceURL = "unavailable", tubiMarketEvidence
		c.AvailabilityNote = "Tanzania is outside Tubi's supported markets; this source is not a confirmed local watch option."
		return
	}
	if sourceMarket != "" && sourceMarket != market || urlMarket != "" && urlMarket != market {
		foreign := sourceMarket
		if foreign == "" || foreign == market {
			foreign = urlMarket
		}
		c.AvailabilityStatus, c.AvailabilityEvidenceURL = "other_region", c.URL
		c.AvailabilityNote = "Source targets " + foreign + "; access in " + market + " is unconfirmed."
		return
	}
	if sourceMarket == market && movieCatalogEvidence(*c) {
		c.AvailabilityStatus, c.AvailabilityEvidenceURL = "regional_listing", c.URL
		c.AvailabilityNote = "Catalog listing for " + market + ". This title's current access and playback are unverified."
		return
	}
	if market != "TZ" {
		return
	}
	switch brand {
	case "netflix":
		c.AvailabilityStatus, c.AvailabilityEvidenceURL = "service_available", netflixMarketEvidence
		c.AvailabilityNote = "Service operates in Tanzania; this title is unconfirmed."
	case "apple":
		c.AvailabilityStatus, c.AvailabilityEvidenceURL = "service_available", appleMarketEvidence
		c.AvailabilityNote = "Apple TV service and movie purchases are supported in Tanzania; this title and its offer are unconfirmed. Rental availability is not established."
	case "prime":
		c.AvailabilityEvidenceURL = primeMarketEvidence
		c.AvailabilityNote = "Prime Video serves many markets; Tanzania access and this title's availability remain unconfirmed."
		if movieHostIs(strings.ToLower(u.Hostname()), "primevideo.com") && !strings.Contains(strings.ToLower(u.Path), "/region/") {
			c.AvailabilityStatus, c.AvailabilityEvidenceURL = "service_available", primeTanzaniaEvidence
			c.AvailabilityNote = "Amazon lists Tanzania among Prime Video's service markets. This title, its offer and playback are unconfirmed; historical membership prices are not current movie offers."
		}
	}
	if c.AvailabilityStatus == "service_available" && movieRegionalRestriction(c.Description) {
		c.AvailabilityStatus = "unknown"
		c.AvailabilityNote = "A search excerpt mentions a regional restriction; the affected country and this title's Tanzania availability are unconfirmed."
	}
}

func watchOptionPriority(c CandidateSource) int {
	switch c.AvailabilityStatus {
	case "regional_listing":
		return 0
	case "service_available":
		return 1
	case "other_region":
		return 3
	case "unavailable":
		return 4
	default:
		return 2
	}
}

func prepareMovieWatchOptions(query, region string, candidates []CandidateSource) []CandidateSource {
	out := make([]CandidateSource, 0, len(candidates))
	for _, candidate := range candidates {
		u, valid := movieWebURL(candidate.URL)
		if !valid {
			continue
		}
		brand, watch := movieDestination(candidate, u)
		if !watch {
			continue
		}
		candidate.DestinationType = "watch"
		enrichMovieAccess(&candidate, brand)
		enrichMovieAvailability(&candidate, u, brand, region)
		if candidate.AvailabilityStatus == "unavailable" {
			continue
		}
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := watchOptionPriority(out[i]), watchOptionPriority(out[j]); a != b {
			return a < b
		}
		if a, b := relevanceScore(query, out[i]), relevanceScore(query, out[j]); a != b {
			return a > b
		}
		return out[i].Title+out[i].Provider+out[i].Region+out[i].URL < out[j].Title+out[j].Provider+out[j].Region+out[j].URL
	})
	return out
}
