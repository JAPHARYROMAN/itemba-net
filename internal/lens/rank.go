package lens

import (
	"sort"
	"time"
)

// Candidate is one location competing for a slot in the hundred.
type Candidate struct {
	URL        string
	Host       string
	Level      int
	ObservedAt time.Time
	Relevance  float64
	// Payload travels with the candidate untouched.
	Payload any
}

// DefaultLimit is the hundred: the most locations a query returns.
const DefaultLimit = 100

// DefaultMaxPerHost keeps a single host from filling the list.
const DefaultMaxPerHost = 10

// Rank orders candidates by level, then relevance, then freshness, removes
// duplicate URLs keeping the strongest, enforces host diversity and applies
// the cap. Candidates displaced by diversity fill remaining slots at the end.
func Rank(candidates []Candidate, limit, maxPerHost int) []Candidate {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if maxPerHost <= 0 {
		maxPerHost = DefaultMaxPerHost
	}
	byURL := make(map[string]Candidate, len(candidates))
	for _, c := range candidates {
		if c.URL == "" {
			continue
		}
		if existing, seen := byURL[c.URL]; !seen || stronger(c, existing) {
			byURL[c.URL] = c
		}
	}
	unique := make([]Candidate, 0, len(byURL))
	for _, c := range byURL {
		unique = append(unique, c)
	}
	sort.SliceStable(unique, func(i, j int) bool { return stronger(unique[i], unique[j]) })

	perHost := make(map[string]int)
	ranked := make([]Candidate, 0, limit)
	var displaced []Candidate
	for _, c := range unique {
		if len(ranked) >= limit {
			break
		}
		if perHost[c.Host] >= maxPerHost {
			displaced = append(displaced, c)
			continue
		}
		perHost[c.Host]++
		ranked = append(ranked, c)
	}
	for _, c := range displaced {
		if len(ranked) >= limit {
			break
		}
		ranked = append(ranked, c)
	}
	return ranked
}

func stronger(a, b Candidate) bool {
	if a.Level != b.Level {
		return a.Level > b.Level
	}
	if a.Relevance != b.Relevance {
		return a.Relevance > b.Relevance
	}
	if !a.ObservedAt.Equal(b.ObservedAt) {
		return a.ObservedAt.After(b.ObservedAt)
	}
	return a.URL < b.URL
}
