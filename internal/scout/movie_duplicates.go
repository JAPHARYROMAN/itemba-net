package scout

import (
	"net/url"
	"strings"
)

// Apple's chart and keyword catalog can supply the same storefront page with
// different referral parameters. Keep the richer listing at its first group
// position without changing the selected URL or normalizing other providers'
// query strings, which can identify different movies or signed media.
func deduplicateAppleStoreListings(candidates []CandidateSource) []CandidateSource {
	if len(candidates) == 0 {
		return candidates
	}
	out := make([]CandidateSource, 0, len(candidates))
	positions := map[string]int{}
	for _, candidate := range candidates {
		key, eligible := appleStoreDuplicateKey(candidate)
		if !eligible {
			out = append(out, candidate)
			continue
		}
		if index, found := positions[key]; found {
			if richerAppleStoreListing(candidate, out[index]) {
				out[index] = candidate
			}
			continue
		}
		positions[key] = len(out)
		out = append(out, candidate)
	}
	return out
}

func appleStoreDuplicateKey(candidate CandidateSource) (string, bool) {
	if candidate.SourceType != "Movie Store" || !candidate.ListingOnly || candidate.DirectStreamURL != "" || !isAppleMovieURL(candidate.URL) {
		return "", false
	}
	u, err := url.Parse(candidate.URL)
	if err != nil {
		return "", false
	}
	// Host and path remain exact, including any explicit port and escaped path
	// bytes. Countries remain separate even if a provider repeats a route.
	return u.Host + "\x00" + u.EscapedPath() + "\x00" + strings.ToUpper(strings.TrimSpace(candidate.Region)), true
}

func richerAppleStoreListing(a, b CandidateSource) bool {
	if aPriced, bPriced := strings.TrimSpace(a.Price) != "", strings.TrimSpace(b.Price) != ""; aPriced != bPriced {
		return aPriced
	}
	if aDuration, bDuration := mediaDurationSeconds(a.Duration) > 0, mediaDurationSeconds(b.Duration) > 0; aDuration != bDuration {
		return aDuration
	}
	return a.ReleaseYear > 0 && b.ReleaseYear <= 0
}
