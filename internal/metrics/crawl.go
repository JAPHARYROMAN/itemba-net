package metrics

// ObserveCrawl counts one frontier entry processed by the crawler, by kind
// (search or page) and outcome.
func (m *Metrics) ObserveCrawl(kind, outcome string) {
	m.crawlFetches.WithLabelValues(kind, outcome).Inc()
}

// ObserveListings counts listings a driver search recorded in Atlas.
func (m *Metrics) ObserveListings(driver string, count int) {
	if count > 0 {
		m.crawlListings.WithLabelValues(driver).Add(float64(count))
	}
}
