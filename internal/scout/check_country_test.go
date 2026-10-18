package scout

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countryCheckTransport func(*http.Request) (*http.Response, error)

func (f countryCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func countryCheckResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func assertCountryDetailContainsNoTrace(t *testing.T, detail string) {
	t.Helper()
	for _, private := range []string{"203.0.113.42", "198.51.100.99", "ip=", "colo=", "trace-private-sentinel"} {
		if strings.Contains(detail, private) {
			t.Fatal("country detail exposed trace fields or a network-error address")
		}
	}
}

func TestAvailabilityCountryUsesOnlyValidatedLocAndSafeClientRoute(t *testing.T) {
	var calls atomic.Int32
	base := &http.Client{Timeout: 5500 * time.Millisecond, Transport: countryCheckTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.String() != availabilityCountryEndpoint || req.Method != http.MethodGet || req.Header.Get("Accept") != "text/plain" {
			t.Error("country probe changed the official endpoint or request contract")
		}
		deadline, bounded := req.Context().Deadline()
		if !bounded || time.Until(deadline) > availabilityCountryTimeout {
			t.Error("country probe was not bounded to two seconds")
		}
		return countryCheckResponse(200, "fl=trace-private-sentinel\nip=203.0.113.42\ncolo=DAR\nloc=TZ\n"), nil
	})}
	s := &Scout{availabilityHTTPClient: base, httpClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		t.Error("country probe used the discovery client's different network route")
		return nil, errors.New("wrong route")
	})}}
	country, detail := s.availabilityCheckCountry(context.Background())
	if country != "TZ" || calls.Load() != 1 || s.country != "TZ" || s.countryAt.IsZero() {
		t.Fatal("country probe did not reduce the trace to its country observation")
	}
	if base.Timeout != 5500*time.Millisecond {
		t.Fatal("country probe mutated the shared availability client's timeout")
	}
	assertCountryDetailContainsNoTrace(t, detail)
}

func TestAvailabilityCountryCachesOneMinuteAndRefreshesChangedEgress(t *testing.T) {
	var calls atomic.Int32
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return countryCheckResponse(200, "ip=203.0.113.42\nloc=US\n"), nil
		}
		return countryCheckResponse(200, "ip=198.51.100.99\nloc=KE\n"), nil
	})}}
	first, _ := s.availabilityCheckCountry(context.Background())
	cached, detail := s.availabilityCheckCountry(context.Background())
	if first != "US" || cached != "US" || calls.Load() != 1 {
		t.Fatal("recent observed egress country was not cached")
	}
	assertCountryDetailContainsNoTrace(t, detail)
	s.countryMu.Lock()
	s.countryAt = time.Now().Add(-availabilityCountryTTL - time.Second)
	s.countryMu.Unlock()
	refreshed, detail := s.availabilityCheckCountry(context.Background())
	if refreshed != "KE" || calls.Load() != 2 {
		t.Fatal("expired country observation was reused instead of refreshed")
	}
	assertCountryDetailContainsNoTrace(t, detail)
	other := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		return countryCheckResponse(200, "loc=TZ\n"), nil
	})}}
	if country, _ := other.availabilityCheckCountry(context.Background()); country != "TZ" {
		t.Fatal("egress-country cache leaked across Scout instances and client routes")
	}
}

func TestAvailabilityCountryFailuresRemainUnconfirmedAndDoNotLeakIP(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		err  error
	}{
		{"HTTP error", 503, "ip=203.0.113.42\nloc=TZ\n", nil},
		{"missing loc", 200, "ip=203.0.113.42\ncolo=DAR\n", nil},
		{"unknown loc", 200, "loc=XX\n", nil},
		{"non-country loc", 200, "loc=T1\n", nil},
		{"invalid loc", 200, "loc=unknown\n", nil},
		{"wrong field", 200, "country=TZ\nip=203.0.113.42\n", nil},
		{"ambiguous loc", 200, "loc=TZ\nloc=US\n", nil},
		{"oversized trace", 200, "loc=TZ\n" + strings.Repeat("x", availabilityCountryMaxBody), nil},
		{"non UTF8", 200, "loc=TZ\n\xff", nil},
		{"transport error", 0, "", errors.New("dial tcp 198.51.100.99:443: trace-private-sentinel")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if tc.err != nil {
					return nil, tc.err
				}
				return countryCheckResponse(tc.code, tc.body), nil
			})}}
			for i := 0; i < 2; i++ {
				country, detail := s.availabilityCheckCountry(context.Background())
				if country != "" || !strings.Contains(detail, "unconfirmed") || s.country != "" {
					t.Fatal("failed location check invented or cached a country")
				}
				assertCountryDetailContainsNoTrace(t, detail)
			}
			if calls.Load() != 2 {
				t.Fatal("a failed observation was cached as an observed country")
			}
		})
	}
}

func TestAvailabilityCountryDoesNotReuseExpiredCountryWhenRefreshFails(t *testing.T) {
	s := &Scout{
		country:   "TZ",
		countryAt: time.Now().Add(-availabilityCountryTTL - time.Second),
		availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network changed: 203.0.113.42")
		})},
	}
	country, detail := s.availabilityCheckCountry(context.Background())
	if country != "" || !strings.Contains(detail, "unconfirmed") {
		t.Fatal("failed refresh reused a stale country observation")
	}
	assertCountryDetailContainsNoTrace(t, detail)
}

func TestAvailabilityCountryRejectsHTMLAndHonorsCancellation(t *testing.T) {
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		response := countryCheckResponse(200, "loc=TZ\n")
		response.Header.Set("Content-Type", "text/html")
		return response, nil
	})}}
	if country, _ := s.availabilityCheckCountry(context.Background()); country != "" {
		t.Fatal("an HTML response was treated as a valid country trace")
	}
	var calls atomic.Int32
	s.availabilityHTTPClient = &http.Client{Transport: countryCheckTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if country, detail := s.availabilityCheckCountry(canceled); country != "" || calls.Load() != 0 {
		t.Fatal("already canceled country check made a network request")
	} else {
		assertCountryDetailContainsNoTrace(t, detail)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if country, detail := s.availabilityCheckCountry(short); country != "" || time.Since(started) > time.Second {
		t.Fatal("country check ignored caller cancellation")
	} else {
		assertCountryDetailContainsNoTrace(t, detail)
	}
}

func TestAvailabilityCountryRedirectsStayOnOfficialHTTPSHostAndAreBounded(t *testing.T) {
	for _, target := range []string{
		"https://geo.example/trace", "http://www.cloudflare.com/cdn-cgi/trace", "https://www.cloudflare.com:444/cdn-cgi/trace", "https://user:password@www.cloudflare.com/cdn-cgi/trace",
	} {
		t.Run(target, func(t *testing.T) {
			var calls atomic.Int32
			s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				response := countryCheckResponse(302, "")
				response.Header.Set("Location", target)
				return response, nil
			})}}
			country, detail := s.availabilityCheckCountry(context.Background())
			if country != "" || calls.Load() != 1 {
				t.Fatal("country probe followed a redirect outside its official HTTPS endpoint")
			}
			assertCountryDetailContainsNoTrace(t, detail)
		})
	}
	var calls atomic.Int32
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		response := countryCheckResponse(302, "")
		response.Header.Set("Location", availabilityCountryEndpoint+"?next=1")
		return response, nil
	})}}
	if country, _ := s.availabilityCheckCountry(context.Background()); country != "" || calls.Load() > 6 {
		t.Fatal("country redirect loop exceeded its redirect limit")
	}
	var baseChecks atomic.Int32
	s.availabilityHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		baseChecks.Add(1)
		return errors.New("base redirect rejected trace-private-sentinel 203.0.113.42")
	}
	country, detail := s.availabilityCheckCountry(context.Background())
	if country != "" || baseChecks.Load() != 1 {
		t.Fatal("country probe bypassed the availability client's redirect policy")
	}
	assertCountryDetailContainsNoTrace(t, detail)
}

func TestAvailabilityCountryAllowsOneSafeSameHostRedirect(t *testing.T) {
	var calls atomic.Int32
	s := &Scout{availabilityHTTPClient: &http.Client{Transport: countryCheckTransport(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			response := countryCheckResponse(302, "")
			response.Header.Set("Location", "/cdn-cgi/trace?next=1")
			return response, nil
		}
		return countryCheckResponse(200, "loc=GB\n"), nil
	})}}
	if country, _ := s.availabilityCheckCountry(context.Background()); country != "GB" || calls.Load() != 2 {
		t.Fatal("safe same-official-host redirect was not accepted")
	}
}
