package scout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

func TestFawesomeCatalogFollowsOnlyReturnedNewArrivalFeed(t *testing.T) {
	base := "https://fawesome.tv/home/new/v467/api/"
	data := []byte(`{"status":"ok","channels":[{"name":"New Arrivals TV","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=111"},{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=222&sort_type=&order=0"}]}`)
	params, ok := fawesomeNewArrivalFeed(data, base)
	if !ok || params.Get("keys") != "222" || params.Get("searchType") != "new-qid" {
		t.Fatal("provider's named movie feed was not followed exactly")
	}
	for _, bad := range []string{
		strings.Replace(string(data), "https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=222", "https://unknown.example/recipes.php?searchType=new-qid&keys=222", 1),
		strings.Replace(string(data), "https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=222", "https://rapi.ifood.tv/private.php?searchType=new-qid&keys=222", 1),
		strings.Replace(string(data), `"New Arrivals"`, `"Latest Releases"`, 1),
		strings.Replace(string(data), `"status":"ok"`, `"status":"fail"`, 1),
		`{"status":"ok","channels":[{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?keys=1"},{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?keys=2"}]}`,
	} {
		if _, ok := fawesomeNewArrivalFeed([]byte(bad), base); ok {
			t.Fatal("unidentified, off-site or ambiguous feed accepted")
		}
	}
}

func TestFawesomeCatalogObservedNewArrivalMixedContentFeed(t *testing.T) {
	// Values follow the actual current public New Arrivals row. The category
	// and navigation IDs are fixtures; the implementation never supplies one.
	data := []byte(`{"status":"ok","channels":[{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=mixedcontent&keys=1234&order=2&rowType=mixedcontent&dltype=0&gridstyle=flat-movie&highlight=0&akp=1-1234&userType=new&version=sv9.0&sort_type=&appId=9&siteId=1&auth-token=fixture&deviceId=&session-id="}]}`)
	params, ok := fawesomeNewArrivalFeed(data, "https://fawesome.tv/home/new/v467/api/")
	if !ok || params.Get("keys") != "1234" || params.Get("searchType") != "mixedcontent" || params.Get("order") != "2" || params.Get("rowType") != "mixedcontent" || params.Get("dltype") != "0" {
		t.Fatal("observed provider New Arrivals feed was changed")
	}
	for _, key := range []string{"auth-token", "appId", "siteId", "deviceId", "session-id"} {
		if _, found := params[key]; found {
			t.Fatal("feed-supplied client identity was forwarded")
		}
	}
	for _, change := range []struct{ before, after string }{{"rowType=mixedcontent", "rowType=private"}, {"dltype=0", "dltype=9"}, {"searchType=mixedcontent", "searchType=nodeid"}} {
		if _, ok := fawesomeNewArrivalFeed([]byte(strings.Replace(string(data), change.before, change.after, 1)), "https://fawesome.tv/home/new/v467/api/"); ok {
			t.Fatal("unobserved catalog control value accepted")
		}
	}
}

func TestFawesomeCatalogDoesNotInventReleaseYearOrAvailability(t *testing.T) {
	source := CandidateSource{Title: "Example Movie", URL: "https://fawesome.tv/movies/12345/example-movie", AccessType: "free", Duration: "5400", ReleaseYear: 2026}
	data := []byte(`{"results":[{"node_id":12345,"date":"Oct. 02, 2026","release_date":"02 October 2026 - 12:00 am","updated":"Oct. 18, 2026"}]}`)
	items := fawesomeCatalogItems([]CandidateSource{source, source}, data)
	if len(items) != 1 || items[0].Query != "Example Movie" || items[0].ReleaseYear != 0 || items[0].ReleaseDate != "" || items[0].CatalogDate != "2026-10-02" || len(items[0].Markets) != 0 || items[0].SourceURL != source.URL {
		t.Fatalf("catalog date became original year, a region claim or a duplicate: %+v", items)
	}
	source.Duration = ""
	if len(fawesomeCatalogItems([]CandidateSource{source}, data)) != 0 {
		t.Fatal("unknown runtime was advertised as a full-length catalog film")
	}
}

func TestFawesomeCatalogCacheEchoesRegionWithoutChangingGlobalItems(t *testing.T) {
	requests := 0
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected cached catalog request")
	})}, discoveryCache: map[string]cachedMovieDiscovery{"fawesome:recent": {
		at: time.Now(), report: MovieDiscoveryReport{Mode: "recent", Region: "ALL", Items: []MovieDiscoveryItem{{Title: "Example", Query: "Example", Markets: []string{}}}, Providers: []ProviderStatus{{Name: "Fawesome public catalog", Status: "ok"}}},
	}}}
	report := s.DiscoverFawesomeMovies(context.Background(), "recent", "TZ")
	if !report.Cached || report.Region != "TZ" || len(report.Items) != 1 || report.Items[0].Query != "Example" || len(report.Items[0].Markets) != 0 {
		t.Fatal("cached guest catalog was country-filtered or failed to echo requested region")
	}
	report.Items[0].Title = "Mutated"
	report.Providers[0].Status = "changed"
	if s.discoveryCache["fawesome:recent"].report.Items[0].Title != "Example" {
		t.Fatal("caller changed shared catalog cache")
	}
	second := s.DiscoverFawesomeMovies(context.Background(), "recent", "US")
	if requests != 0 || !second.Cached || second.Region != "US" || second.Items[0].Title != "Example" || second.Providers[0].Status != "ok" {
		t.Fatal("region change fetched or mutated the shared global catalog")
	}
}

func TestFawesomeCatalogRejectsUnexpectedFeedIdentityAndLocation(t *testing.T) {
	for _, suffix := range []string{"&country=US", "&session-id=someone", "&deviceId=someone", "&redirect=https%3A%2F%2Fexample.com", "&keys=2", "&platform_id=1&platform_id=2"} {
		data := []byte(`{"status":"ok","channels":[{"name":"New Arrivals","url":"https://rapi.ifood.tv/recipes.php?searchType=new-qid&keys=1` + suffix + `"}]}`)
		if _, ok := fawesomeNewArrivalFeed(data, "https://fawesome.tv/home/new/v467/api/"); ok {
			t.Fatalf("unexpected returned feed query was accepted: %s", suffix)
		}
	}
}

func TestFawesomeCatalogExpiredSuccessDoesNotRemainAfterError(t *testing.T) {
	requests := 0
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: searchFixtureTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("fixture public catalog unavailable")
	})}, discoveryCache: map[string]cachedMovieDiscovery{"fawesome:popular": {
		at: time.Now().Add(-movieDiscoveryCacheTTL - time.Second), report: MovieDiscoveryReport{Mode: "popular", Region: "ALL", Items: []MovieDiscoveryItem{{Title: "Old catalog"}}, Providers: []ProviderStatus{{Status: "ok"}}},
	}}}
	report := s.DiscoverFawesomeMovies(context.Background(), "popular", "ALL")
	if report.Cached || len(report.Items) != 0 || requests != 1 || len(report.Providers) != 1 || report.Providers[0].Status != "error" || fawesomeCatalogTTL(report) != time.Minute {
		t.Fatal("expired catalog success survived an unavailable refresh")
	}
}

func TestLiveFawesomeCatalog(t *testing.T) {
	mode := os.Getenv("ITEMBA_LIVE_FAWESOME_CATALOG")
	if mode != "popular" && mode != "recent" && mode != "diagnostic" {
		t.Skip("opt-in bounded public catalog request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), fawesomeDiscoveryDeadline)
	defer cancel()
	client := newMovieAvailabilityClient()
	transport := client.Transport
	var rowNames []string
	var rowFeedSummary string
	requests := 0
	client.Transport = searchFixtureTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		response, err := transport.RoundTrip(req)
		if response == nil {
			return response, err
		}
		t.Logf("public guest endpoint=%s HTTP status=%d", path.Base(req.URL.Path), response.StatusCode)
		if path.Base(req.URL.Path) == "sub-categories.php" {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, availabilityPageLimit+1))
			response.Body.Close()
			response.Body = io.NopCloser(bytes.NewReader(data))
			if readErr == nil {
				var payload struct {
					Channels []struct {
						Name string `json:"name"`
						URL  string `json:"url"`
					} `json:"channels"`
				}
				if json.Unmarshal(data, &payload) == nil {
					for _, row := range payload.Channels {
						rowNames = append(rowNames, row.Name)
						if row.Name == "New Arrivals" {
							u, err := url.Parse(row.URL)
							if err == nil {
								values := u.Query()
								for key := range values {
									lower := strings.ToLower(key)
									if strings.Contains(lower, "token") || strings.Contains(lower, "session") || strings.Contains(lower, "device") || lower == "appid" || lower == "siteid" || lower == "platform_id" {
										delete(values, key)
									}
								}
								rowFeedSummary = u.Scheme + "://" + u.Host + u.Path + "?" + values.Encode()
							}
						}
					}
				}
			}
		}
		return response, err
	})
	s := &Scout{availabilityHTTPClient: client}
	if mode == "diagnostic" {
		guest, err := s.newFawesomeGuest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		popular, err := guest.fetch(ctx, "recipes.php", url.Values{"searchType": {"popular"}})
		if err != nil {
			t.Fatal(err)
		}
		var summary struct {
			Status  string                  `json:"status"`
			Type    string                  `json:"type"`
			Results []fawesomeDiscoveryItem `json:"results"`
		}
		_ = json.Unmarshal(popular, &summary)
		t.Logf("popular response status=%s type=%s records=%d", summary.Status, summary.Type, len(summary.Results))
		for i, row := range summary.Results {
			if i >= 6 {
				break
			}
			t.Logf("popular title=%q type=%q/%q/%q page=%q runtime=%.0f", row.Title, row.ItemType, row.FeedType, row.ActionType, row.WebURL, row.Runtime)
		}
		rows, err := guest.fetch(ctx, "sub-categories.php", url.Values{"parent": {"home"}})
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Status   string `json:"status"`
			Channels []struct {
				Name       string `json:"name"`
				URL        string `json:"url"`
				FeedType   string `json:"feed_type"`
				ActionType string `json:"actiontype"`
			} `json:"channels"`
		}
		if err := json.Unmarshal(rows, &root); err != nil {
			t.Fatal("root row response shape was unrecognized")
		}
		t.Logf("home row status=%s count=%d", root.Status, len(root.Channels))
		for _, row := range root.Channels {
			u, err := url.Parse(row.URL)
			if err != nil {
				continue
			}
			var keys []string
			for key := range u.Query() {
				keys = append(keys, key)
			}
			t.Logf("home row=%q type=%q/%q endpoint=%s querykeys=%v searchType=%q", row.Name, row.FeedType, row.ActionType, path.Base(u.Path), keys, u.Query().Get("searchType"))
		}
		return
	}
	candidates, data, err := s.fetchFawesomeCatalog(ctx, mode)
	if err != nil {
		t.Logf("available public home rows: %q", rowNames)
		t.Logf("public new-arrivals feed contract without client identifiers: %s", rowFeedSummary)
		t.Fatal(err)
	}
	items := fawesomeCatalogItems(candidates, data)
	if len(items) == 0 {
		t.Fatal("public catalog returned no full-length movie listings")
	}
	proof := struct {
		Mode       string               `json:"mode"`
		CheckedAt  string               `json:"checkedAt"`
		Requests   int                  `json:"requests"`
		Items      []MovieDiscoveryItem `json:"items"`
		Candidates []struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Duration string `json:"runtimeSeconds"`
		} `json:"candidates"`
	}{Mode: mode, CheckedAt: time.Now().UTC().Format(time.RFC3339), Requests: requests, Items: items}
	for i, candidate := range candidates {
		if i >= 10 {
			break
		}
		proof.Candidates = append(proof.Candidates, struct {
			Title    string `json:"title"`
			URL      string `json:"url"`
			Duration string `json:"runtimeSeconds"`
		}{candidate.Title, candidate.URL, candidate.Duration})
	}
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile("../../.itemba-runtime/fawesome-"+mode+"-catalog-proof.json", encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("dynamic %s catalog returned %d full-length listings in %d public requests", mode, len(items), requests)
	for _, candidate := range proof.Candidates {
		t.Logf("title=%s primary=%s runtime=%s", candidate.Title, candidate.URL, candidate.Duration)
	}
}
