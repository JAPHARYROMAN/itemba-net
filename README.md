# ITEMBA NET

The [ITEMBA NET White Paper](WHITEPAPER.md) defines the direction of this project: a sandboxed, object-centric search engine for every kind of media, with independent and unrestricted discovery, at most one hundred candidate locations per query, and ad-free access inside an isolated sandbox. The architecture, principles and phases in the white paper guide future development. The [Implementation Plan](IMPLEMENTATION-PLAN.md) sequences that work into phases with exit criteria.

**ITEMBA NET** combines three distinct systems paradigms into one cohesive platform:
1. **`itemba-scout` (Focused Web Crawling & Headless Stream Verification)**
2. **`itemba-vault` (Remote Browser Isolation & Pion WebRTC Streaming)**
3. **`itemba-shield` (Triple-Layer DNS, CDP Network, & DOM Ad Sanitization)**

---

## Repository Architecture

```text
ITEMBA-NET/
├── cmd/
│   └── itemba/
│       ├── main.go                 # Entry point: --role flag, local configuration, subsystem wiring
│       └── role.go                 # Roles: console and all run now; crawler and probe arrive with Phases 2 and 4
├── internal/
│   ├── atlas/                      # Persistent index (Phase 0: connection pool, embedded migrations, health)
│   │   ├── store.go                # PostgreSQL pool and health check; errors never echo the URL
│   │   ├── migrations.go           # Ordered NNNN_name.sql migrations applied under an advisory lock
│   │   └── migrations/             # 0001_baseline.sql enables the vector extension
│   ├── cdp/
│   │   └── client.go               # Concurrent Chrome DevTools Protocol client & hybrid Docker/Local launcher
│   ├── config/
│   │   └── config.go               # Typed Config from the optional .env.local file plus the environment
│   ├── console/                    # Operator interface: HTTP API, WebSocket multiplexer, metrics, dashboard
│   │   ├── console.go              # Route registration and handlers over scout, vault, shield, relay and atlas
│   │   └── testbench.go            # Built-in Clean Cinema, SketchyStreamz Ad-Trap, & Fake-Wall testbenches
│   ├── metrics/
│   │   └── metrics.go              # Prometheus registry: search, probe, shield, vault, atlas and HTTP measures
│   ├── shield/                     # Pillar 3: itemba-shield (Triple-Layer Ad & Threat Guillotine)
│   │   ├── blocklists.go           # Layer 1 DNS Sinkhole (OISD/HaGeZi/StevenBlack) & Layer 2 CDP Filter rules
│   │   ├── scripts.go              # Layer 3 Window-Spawn Lock, Z-Index Clickjacking Sweeper, & Theater Isolator
│   │   └── shield.go               # Real-time CDP Fetch.requestPaused interceptor & telemetry broadcaster
│   ├── scout/                      # Pillar 1: itemba-scout (Federated Seed Discovery & Stream Verifier)
│   │   ├── scout.go                # Concurrent seed discovery (Internet Archive API + Open Catalog + Testbenches)
│   │   └── verifier.go             # Headless Chromium worker sniffing <video>, <iframe>, & .m3u8/.mpd manifests
│   └── vault/                      # Pillar 2: itemba-vault (Remote Browser Isolation Sandbox & Streamer)
│       └── manager.go              # Ephemeral sandbox lifecycle, Pion WebRTC DataChannels, & input multiplexing
├── deploy/
│   ├── coredns/                    # Container Layer 1 DNS Sinkhole configuration
│   │   ├── Corefile
│   │   └── blocklist.hosts
│   ├── grafana/                    # Provisioned Prometheus datasource and the Phase 0 dashboard
│   └── prometheus/                 # Scrape configuration for the binary's /metrics
├── Dockerfile.vault                # Hardened Linux RBI container (Debian + Chromium + Xvfb + PulseAudio)
├── docker-compose.yml              # Full containerized deployment (CoreDNS Sinkhole + Vault Container)
└── web/                            # Milestone 4: React + TypeScript + Vite Unified Theater Dashboard
```

---

## Movie search

Search by movie title or keywords, optionally adding a release year. **Worldwide is the default.** Global web discovery has no country or preferred-provider bias. Results contain individual movie/player pages and rank successful playback samples, reachable media and matching pages ahead of unchecked listings. The check server's country is evidence about that observation; it does not determine the search region or establish worldwide access. Users can explicitly choose a country preference. Guides, reference pages, merchandise, theater tickets and obsolete Showmax links are excluded.

**Full-length movies** is enabled by default and prefers movies of at least 40 minutes. Known shorts, trailers, previews and menu files are excluded; unknown runtimes remain explicitly unconfirmed. A sampled native runtime overrides a conflicting catalog runtime. Turn the preference off to include shorts. The search API accepts `fullLengthOnly` (default `true`); full-length and all-length cached reports are separate.

The app opens with live **Popular** chart suggestions and a **Recent store releases** view, replacing fixed classic-film examples. Selecting a title runs the normal global search for free and paid watch options; the app does not automatically search or play a title. Suggestions use Apple's public movie charts across the same eight sampled markets, with market and check-time provenance. Recent suggestions use reported store catalog dates within the past 24 months and exclude future dates. Store dates can differ from original film releases and include reissues, so they never become inferred film years or search constraints. Charts indicate interest in those storefronts, not worldwide popularity or a confirmed streaming offer.

`GET /api/scout/discover?mode=popular&region=ALL` supplies the suggestion shelf; `mode=recent` selects recent store releases. It requires no API key and consumes no web-search credits. Eight markets share an eight-second deadline with four concurrent requests. Popular and Recent reuse a 15-minute cache per region (three minutes for partial failures). Manual title search remains available when a chart fails.

Title searches also consult the full current chart cache for exact movie matches. This preserves the original Apple movie-page link when its keyword-search API misses a recent chart release. The linked page retains its actual storefront market, is labeled paid with its current offer unconfirmed, and does not acquire an invented price, original release year or playback claim.

**Worldwide** searches eight Apple storefronts (United States, United Kingdom, India, South Africa, Germany, France, Japan and Australia), alongside Internet Archive, Blender's public movie catalog, federated PeerTube search, Wikimedia Commons and configured global web discovery. Blender's public PeerTube search supplies movie candidates without an API key, independently of the wider PeerTube index; its metadata resolver must confirm public media before sampling. Storefront requests share a ten-second budget with at most four active requests. Choose a country preference to search its storefront instead. Each offer retains its market; a listing does not establish availability elsewhere. No URLs are rewritten into a local storefront, and no guessed movie IDs are generated.

Results show free, subscription, rental, purchase, paid or unconfirmed access. Catalog prices include their currency and market. A known paid service model (for example Netflix subscriptions) is labeled separately from a confirmed offer for this movie; YouTube videos and unfamiliar sites retain unconfirmed access. Evidence links open through the protected browser. Regional badges distinguish a catalog listing in the selected country, a supported service with the movie unconfirmed, an unknown region and a foreign-market offer. Check time and verification status distinguish listings, reachable pages and reachable media; a successful media probe is not a full playback test. Failed static reference streams are excluded. The server keeps no in-memory copy of search reports; repeat searches are answered from Atlas when a database is configured, as described under Phase 1 below.

Every **Open with ad blocking** action opens the source inside Vault. Required network interception and page protection must initialize successfully before navigation; failures appear in the interface. Sign-in, payment and regional requirements remain with the provider. Paid listings are never converted into preview streams or offered through the free-media relay. Ad blocking reduces advertising and tracking; effectiveness and playback compatibility vary by site.

Direct movie-page recognition includes current Disney+, Hulu, Peacock, HBO Max, Paramount+, Fandango at Home, Rakuten TV, Pluto TV and Filmzie routes. Subscription services are labeled paid; free service models are distinguished from evidence of an offer for this specific title. Mixed free/rental/purchase services retain unconfirmed access until primary movie evidence exists. A Plex guide page alone is excluded: up to three matching Plex movie pages receive a bounded primary-action check (3.5 seconds overall, 1 MiB per response). A visible movie-specific **Watch for Free** action can establish a page offer, while trailers, FAQs, recommendations, hidden content, external providers and guide-only pages cannot. It does not authorize a native playback sample without supported public media.

Free-media selections start with the protected relay enabled. A fatal HLS or native-player error remains visible and offers a retry with the current protection setting; errors never silently switch to a direct CDN connection. Each new search-result selection restores its requested protection setting.

For broader discovery of websites beyond these catalogs, configure `TAVILY_API_KEY` or `BRAVE_SEARCH_API_KEY` as described below. Tavily is an alternative when Brave is unavailable. Without either key, the app shows that web discovery is unavailable while catalog search continues. Search snippets alone do not establish a free offer or local playback. Apple movie listings use its [Search API](https://performance-partners.apple.com/search-api) and require no API key. Provider failures and empty responses are visible under **Search coverage**.

Provider guidance was checked on 18 October 2026: [Netflix country coverage](https://help.netflix.com/en/node/14164), [Apple country features](https://support.apple.com/en-us/118205), [Prime Video country guidance](https://www.primevideo.com/help?nodeId=GD5REBNJD74BURF6), [Tubi markets](https://tubitv.com/help-center/About-Tubi/articles/4409969055259), and [Showmax's Tanzania closure](https://www.dstv.com/en-tz/watch/showmax/). These describe service policies rather than per-movie licensing, and should be refreshed as providers change.

### Automatic availability checks

Each movie search observes the server's outgoing country and checks up to six matching movie pages, alongside existing bounded free-media probes. In Worldwide mode, successful free-video playback samples rank first regardless of the check location, followed by media responses and matching movie-page responses. Explicit country preferences apply local evidence only when its check country matches. Storefront markets remain visible. Missing links, explicit country restrictions and failed or blocked checks rank lower. Cards show the checked URL, observation time and actual check country. One network observation never confirms every country.

Page checks distinguish a matching reachable movie page, a regional/legal restriction, a missing link, sign-in, a restricted automated check and an inconclusive response. HTTP 403 alone is not a country block, and HTTP 200 is not confirmed playback. The parser ignores scripts, hidden content, navigation, recommendations, trailers and FAQs when looking for movie access messages. It checks movie identity and release year, and does not treat a sign-in redirect or a soft 404 as a working movie page. A reachable media response means its sampled format/signature was recognized; it does not establish complete playback, licensing or a paid user's entitlement.

Checks use the same direct server connection for the country observation and movie/media requests. The [Cloudflare diagnostic endpoint](https://developers.cloudflare.com/fundamentals/reference/cdn-cgi-endpoint/) is reduced to its country code; IP addresses and raw trace data are not kept or returned. Positive country observations are reused for up to 60 seconds. Page responses are limited to 512 KiB and five seconds, with at most five public-web redirects. Private/local destinations and nonstandard ports are rejected, including DNS and redirect targets. Page checks do not execute provider scripts or submit credentials. The complete search has a 44-second deadline; links outside its check budget remain explicitly unchecked. Catalog-only cached reports retain their original check timestamps.

### Free playback samples

Up to two supported, reachable free videos are sampled automatically in parallel, preferring different sites and avoiding repeated tests of an identical media URL. Free-media discovery resolves relevant Internet Archive item pages, public PeerTube video metadata and supported public-domain movie pages into their explicit media sources. Metadata must identify the requested movie, and failed resolutions remain listings. This enrichment checks at most six targets with three workers, four seconds per target and a six-second overall budget. Search snippets do not authorize playback of paid or unknown sources.

For equally relevant and equally reachable candidates, explicit browser-compatible format metadata and lower known average bitrates help choose which copies fit the short sample budget. These scheduling hints never confirm playback or affect country availability; successful native decoding still determines the playback badge.

Supported discovery routes include Internet Archive `/details/` and `/embed/` item pages, public PeerTube `/w/`, `/videos/watch/` and `/videos/embed/` links, and individual [Public Domain Movies](https://publicdomainmovie.net/) pages. Public Domain Movies links are checked against the exact Archive item in the page's download section; this adds discovery pages while retaining the original media provider. PeerTube supports public files and HLS playlists, including a public CDN explicitly identified by the hosting instance's video API. Other websites retain their page-access checks and protected browsing while playback support remains unconfirmed.

Feature discovery also searches [PublicDomainMovie.org](https://publicdomainmovie.org/) and [ArcoirisTV](https://www.arcoiris.tv/) through their public search forms. The former must embed one matching Archive feature in its primary movie content; the latter must publish matching first-party video metadata and a feature-length runtime. These searches require no account or API key. Public Domain Movies' feature browse adds exact title/year matches from its advertised catalog pages. Archive search considers up to 30 listings, resolves at most six relevant items within eight seconds, rejects mismatched filename years and ancillary videos, and prefers known streaming derivatives and explicit H.264/WebM metadata before other encodings. Archive documents its [optimized 512kb MP4 derivative](https://blog.archive.org/2010/04/26/better-mp4-h-264-derivatives-at-archive-org/); these format hints help choose a candidate, while decoded frame progress is still required to confirm a start.

Each sample uses a fresh muted Chromium profile and an ITEMBA-owned local video player; provider pages, ads, logins and payment flows are not executed. Media is relayed through the same guarded server connection as the country observation. Chromium can request only that sample's local player and media resources. MP4/WebM and supported HLS streams use at most 6 MiB of upstream data per sample and a 12-second browser deadline inside a 14-second sample budget. HLS uses the locally built `playback-hls.min.js` asset and selects the lowest advertised bandwidth.

Pluto TV and Filmzie movie links also receive bounded **provider access checks**, both when discovered by title and when pasted directly into search. Up to four links share a six-second budget with two workers, covering both sites before additional copies. Pluto's explicit location-unavailable page and Filmzie's public main-video country response are recorded as restrictions, with the actual check location shown. These checks do not count as decoded playback samples. An accessible Pluto page remains unconfirmed until its main movie content can be attributed safely; no private or guessed stream API is used.

Filmzie metadata must identify a matching feature title, year and full movie asset, followed by a public guest playback response. Trailers, DRM, sign-in requirements and explicit country restrictions stop the check. An approved main movie file may enter the existing protected native sampler; provider stream URLs stay private and are not exposed as generic client relay links. Filmzie's server-inserted-ad HLS remains unconfirmed because a preroll cannot establish that the movie started. The live checks on 18 October 2026 found both providers restricted from the server connection in Tanzania; Worldwide remains the search default.

The sampler applies Shield rules to its initial media, redirects and every HLS playlist, key, map and segment. Explicit ad cues and DRM playlists stop verification without rewriting the movie timeline or retrying without protection. Blocked domains and explicit custom URL rules apply even when an address ends in a movie-file extension. A protection or content-attribution failure cannot receive a successful playback badge or ranking boost.

Additional supported free providers are **Fawesome**, **NASA+**, **NFB / ONF**, and the **Catsuka player explicitly authorized by filmmaker Nina Paley**. Their public movie metadata must match the title, year where supplied, feature runtime and exact main asset. Guest restrictions, paid offers, trailers, DRM and unattributed advertising remain unverified. Signed provider media stays private; movie pages open through Vault with ad filtering enabled. Fawesome follows its normal player's preference for an explicitly supplied, movie-node-bound main HLS source, with the public main MP4 contract supported when no HLS source is declared. Both stay behind the same protected native checker; no stream path is guessed, no oversized MP4 is downloaded and no unprotected retry is made. The creator adapter follows the filmmaker's explicit publisher link. NFB's exact public embed supplies its unchanged signed movie stream; NASA+ publishes its main video separately from preview media.

Protected beginning samples confirmed these films from the current connection on 18 October 2026: *The Secret Wife* (2024, Fawesome, about 77 minutes), *Planetary Defenders* (2025, NASA+, about 75 minutes), *We Will Stand Up* (2019, NFB, about 99 minutes), and *Seder-Masochism* (2018, creator-authorized Catsuka, about 78 minutes). Dynamic Fawesome discovery also found *Omniscient Reader: The Prophecy*: its declared main HLS source passed an actual 1920×1080 sample with 1.1 seconds of natural progress and a roughly 117-minute movie runtime. The global query showed that free result first alongside a paid storefront offer. These are measured starts, not confirmation of uninterrupted complete-film playback or availability from other networks. Large MP4 headers can consume the bounded sample budget; an explicitly supplied main HLS source can use the provider's ordinary segmented playback path without increasing the sample limits.

NASA+ and NFB add public catalog searches without API keys or extra web-search credits. Fawesome discovery now follows its public website's normal guest search, resolving the current public application configuration and requesting matching movie listings. It does not rely on a fixed set of example films. Exact movie routes and node IDs must agree; series, trailers, shorts, paid offers and malformed destinations cannot become full-film playback candidates. Fawesome requests use the guarded direct connection without sign-in cookies, accounts or a country override. The global web query remains unrestricted and still uses one configured web-search request. Provider search is limited to its first response page; a title selected from a recently fetched Fawesome catalog also retains its exact observed movie page before fresh availability and playback checks.

**Free on Fawesome** adds live **Popular on Fawesome** and **New on Fawesome** suggestions to the existing global chart shelf. These are provider catalog listings, not a worldwide popularity ranking. Selecting a title runs the ordinary global search for free and paid watch options. Fawesome's page/feed dates can describe catalog additions or reissues: they are labeled catalog dates and never inferred as original movie years or appended to search queries. Popular and new feeds share an eight-second deadline, with at most four or five public requests on a cold start, respectively. Public application configuration and catalog suggestions are briefly cached; guest session tokens stay request-scoped.

Opening either Fawesome shelf automatically starts a finite round of protected checks for up to 24 exact movie pages from that server-fetched catalog. Cards update progressively and successful native starts move first; other cards retain provider order and remain searchable. One background worker shares the app's two native-playback slots, preserving capacity for global searches. Each attempt keeps the existing six-second metadata check, fourteen-second native sample budget and 6 MiB media limit. Shelf checks call no web-search API and add no Tavily search credits. Changing shelves releases the old round; a thirty-second view lease and fixed eight-minute deadline stop abandoned work. Progress polling never creates or retries a movie check.

Safe playback observations are cached in memory for five minutes and shared across both shelves by exact movie page, observed connection country and Shield rule revision. Only actual decoded video progress with a feature-length native runtime can receive a green badge or priority; HTTP success, catalog entries, ads, trailers and provider metadata cannot. Expired or invalidated proof loses its confirmation, and a newly observed change of connection country or protection rules prevents reuse. Cards show the check time and location and distinguish queued, checking, unconfirmed, unchecked and expired states. Signed/main media URLs are not retained in the shelf cache or returned to the browser. Worldwide remains the search default, and the observation location does not restrict the catalog or guarantee complete-film playback. **Playback started only** continues to use the same native evidence in search results.

The live automatic-shelf check on 18 October 2026 confirmed protected starts for the first fourteen Popular entries before switching shelves. The New round completed all seventeen checks with sixteen decoded starts and one unconfirmed movie. Shared entries retained the same observation across both shelves. A worldwide title search completed alongside this background round with free and paid results; the shelf's expanded twelve-card view stayed open through progress updates.

HLS sampling prefers declared video renditions before choosing the lowest bandwidth, so a cheaper audio-only rendition cannot displace the movie. The built local HLS decoder is preferred when supported. Full signed playlists may use up to 1 MiB, while the existing 6 MiB total sample budget, 12-second browser deadline and four usable media-segment limit remain unchanged. Playlist duration and all ad/DRM cues remain intact.

Success requires decoded video-frame progress, positive video dimensions, and at least one second of natural video-clock progress near the beginning at normal speed. A Play event, audio alone, a seek, or recognizable media bytes cannot confirm a start. Where the catalog provides a runtime, a materially shorter clip or different-duration video is rejected. Playback badges name the actual observed check country, or state that location is unknown. They describe the short beginning sample; access elsewhere, complete-film playback, ownership and future availability remain unconfirmed. Browser/codec failures, stalled video, unsupported sources and unsampled links retain explicit neutral states. Reachable free media can still be tried manually after an inconclusive sample. Paid sources are never sampled or relayed.

Recent commercial titles can be discovered and opened in the protected browser. Automatic native playback checks continue to cover supported public free media; a free service label on Pluto TV, Filmzie or another provider does not imply that ITEMBA has tested its player. Provider-specific sign-in, DRM, country restrictions and unsupported media remain explicit limitations, and ad blocking may affect compatibility.

This is federated discovery writing into a persistent index. Continuous global crawling is Phase 2 work.

### Enable Tavily web discovery

1. Open [Tavily](https://app.tavily.com), create an account or sign in, and obtain an API key from the dashboard. Its [free plan](https://www.tavily.com/pricing) currently includes 1,000 credits per month without a credit card.
2. Save the key as `TAVILY_API_KEY=your_key_here` in the project-root `.env.local`. Keep keys out of chat, source code and browser configuration.
3. Restart `itemba.exe` from the project root. Existing process environment variables take precedence over the local file. The file accepts only `PORT`, `TAVILY_API_KEY`, `BRAVE_SEARCH_API_KEY` and `ITEMBA_DATABASE_URL`.
4. Search for a movie and open **Search coverage**. A working connection shows **Tavily web search** with the number of destination links returned. Authentication and quota failures appear without exposing the key.

The app requests [basic search](https://docs.tavily.com/documentation/api-reference/endpoint/search), with automatic search-depth selection, generated answers and raw-page extraction disabled. Basic requests cost one credit. Worldwide searches omit country and preferred-domain fields and search for free, rental and purchase options across the web. Guide/reference domains are excluded upstream and all returned destinations are filtered again locally. Explicit country preferences may add country and service hints; they never guarantee local title availability. Tavily has a bounded ten-second request budget inside the 44-second search deadline, which also covers free-source discovery, availability and playback checks.

When both keys are configured, Tavily takes precedence. The app sends one web-provider request per search; it does not automatically retry a failed Tavily request through Brave. This avoids unexpected requests to a second provider. Configure only the provider you intend to use.

### Optional Brave web discovery

1. Open the [Brave Search API dashboard](https://api-dashboard.search.brave.com/app/keys), sign in or register, activate a **Search** plan, and create a key. Review billing and usage limits in your account. [Brave's setup guide](https://api-dashboard.search.brave.com/documentation/quickstart) describes the account and plan steps.
2. Open `.env.local` in the project root and set `BRAVE_SEARCH_API_KEY=your_key_here`. A blank local file is supplied for this workspace; `.env.example` is the reusable template. Keep the actual key in `.env.local`, which is excluded from version control. The key belongs to the Go server and must never use a `VITE_` prefix or appear in browser code.
3. Restart `itemba.exe` from the project root. Environment variables already set for the process take precedence over `.env.local`, including an explicitly blank variable. The optional file supports `BRAVE_SEARCH_API_KEY`, `TAVILY_API_KEY`, `PORT` and `ITEMBA_DATABASE_URL`; quoted literal values and comments are accepted, without shell expansion.
4. Search for a movie and expand **Search coverage**. A working connection shows **Brave web search** with the number of destination links returned. Invalid keys or plans show an authentication error; rate limits show a retry or quota message. An authenticated search can legitimately return no matches.

Brave searches the worldwide index (`country=ALL`); a selected country supplies a location hint. Recognized movie-site page titles are normalized for matching, while the original title is preserved in the description. Web listings remain unverified with unknown access until there is evidence of price or availability. Opening a result uses the same protected browser as catalog results.

Reports are not kept in memory between requests. When a database is configured, web-discovery listings are recorded in Atlas like any other location, so the retention terms of a configured web-search provider are the operator's to observe. See [Brave's retention guidance](https://api-dashboard.search.brave.com/documentation/resources/help-feedback) for its current standard terms. Setup and connection errors never include key values.

### Build and verify

```powershell
go test ./...
go build -o itemba.exe ./cmd/itemba
cd web
npx tsc --noEmit
npm run build
```

Rebuild and restart the server after backend changes. The executable serves the latest `web/dist` from the repository working directory.

The ordinary test suite uses offline fixtures for playback evidence, global ranking, source resolution, paid-access boundaries, range handling, time/byte limits and cancellation. The optional `TestLiveFreeMoviePlaybackSintel` runs a bounded public Internet Archive sample in a disposable Chromium profile when `ITEMBA_LIVE_PLAYBACK_TEST=1`. `ITEMBA_LIVE_FREE_SOURCE_TEST=1` enables the added-host Blender PeerTube resolution/playback smoke test. `ITEMBA_LIVE_PUBLIC_DOMAIN_TEST=1` separately tests Public Domain Movies; its large original files may remain unconfirmed within the sample budget. These checks need a working browser and Internet connection and do not load API keys or sign-in cookies. Network failures can make opt-in smoke tests fail without affecting the offline suite.

`ITEMBA_LIVE_FAWESOME_DISCOVERY=1` enables one bounded live guest title-search check; optionally set `ITEMBA_LIVE_FAWESOME_QUERY` to an actual film title. `ITEMBA_LIVE_FAWESOME_CATALOG=popular` or `recent` tests one live provider shelf. These checks exercise public configuration, guest initialization and listing identity without playing media or using web-search credits. The offline integration test separately enforces one Tavily request, global search, exclusion of paid playback, and the need for native decoded-frame proof.

`ITEMBA_LIVE_FEATURE_DISCOVERY=1` enables the full-feature Archive/PDM catalog check; `ITEMBA_LIVE_FEATURE_PLAYBACK=1` enables the *His Girl Friday* native sample. `ITEMBA_LIVE_FEATURE_TEST=1` enables the new ArcoirisTV/PublicDomainMovie.org sample tests (select one with `go test -run`). A live PublicDomainMovie.org *Night of the Living Dead* check observed a 96-minute track and decoded frames advancing normally. This confirms the beginning sample, not uninterrupted playback of the complete film. ArcoirisTV can expose the full runtime yet remain unconfirmed when its large media metadata exceeds the bounded sample's practical budget.

---

## Quick Start

### 1. Run Unified Platform (Local Hybrid Mode — Zero Docker Required)

The built frontend (`web/dist`) is served directly by `itemba` on `http://127.0.0.1:8080`:

```powershell
# Start the ITEMBA NET Control Plane + Dashboard on http://127.0.0.1:8080
./itemba.exe
```

The binary takes a `--role` flag: `console`, `crawler`, `probe` or `all` (the default). Only `console` and `all` run in this phase; `crawler` and `probe` refuse to start until Phases 2 and 4 of the [Implementation Plan](IMPLEMENTATION-PLAN.md) deliver their workers. `--config` names the optional local configuration file (default `.env.local`), `--static` the built dashboard directory (default `web/dist`) and `--database` the Atlas PostgreSQL URL (default from `ITEMBA_DATABASE_URL`).

### Phase 0 foundations

**Atlas database.** The binary runs without a database and says so at startup; nothing is persisted. To persist, start PostgreSQL with the pgvector extension and point the binary at it:

```powershell
docker compose up -d itemba-atlas
```

Then set `ITEMBA_DATABASE_URL=postgres://itemba:itemba-dev@127.0.0.1:5432/itemba` in `.env.local` or the environment, or pass `--database`. At startup the binary connects, applies the embedded migrations in `internal/atlas/migrations` in order under an advisory lock, and refuses to run if a configured database is unreachable or a migration fails. `GET /api/status` reports the connection, server version and the applied and pending migration counts under `atlas`. The migration test runs against a disposable database when `ITEMBA_TEST_DATABASE_URL` is set; CI provides one, and locally the compose service works with the URL above. The test drops the `atlas` schema, so never point it at real data.

**Metrics.** `GET /metrics` serves Prometheus metrics: search duration and source counts, provider results, probe outcomes, Shield blocked and allowed counts by layer, Vault session state, Atlas health and console request counts, plus Go runtime and process metrics. These are the measures from section 14 of the white paper that Phase 0 and Phase 1 own.

**Dashboard.** Prometheus and Grafana run from compose with the Phase 0 dashboard provisioned:

```powershell
docker compose up -d itemba-prometheus itemba-grafana
```

Prometheus scrapes the binary on the host at `host.docker.internal:8080` every 15 seconds (`deploy/prometheus/prometheus.yml`). Grafana listens on `http://127.0.0.1:3000`; sign in as `admin` with `ITEMBA_GRAFANA_PASSWORD` (default `itemba-dev`) and open **ITEMBA NET Phase 0** in the ITEMBA NET folder. The dashboard is `deploy/grafana/dashboards/itemba-phase0.json` and can be edited in place.

**Linux execution host.** Vault's isolation tiers, per-session egress and GPU access in later phases need Linux with virtualisation. Phase 0 uses Debian 13 inside WSL2 on the development machine; a dedicated Linux machine or virtual server can replace it later without a code change. Setup, once:

```powershell
wsl --install -d Debian --no-launch
```

Then create `%USERPROFILE%\.wslconfig` containing `[wsl2]`, `nestedVirtualization=true` and a `memory` cap, run `wsl --shutdown`, and provision the distro as root:

```powershell
wsl -d Debian -u root -- bash -c "apt-get update && apt-get install -y chromium ca-certificates curl fonts-liberation && useradd -m -s /bin/bash itemba && usermod -aG kvm itemba && printf '[user]\ndefault=itemba\n' > /etc/wsl.conf"
```

Chromium must run as the non-root `itemba` user because Vault launches it with its sandbox on. With nested virtualisation, `/dev/kvm` exists inside the distro, which the microVM tier in Phase 4 will need.

The development loop: edit and test on Windows, cross-compile, and run inside the distro from the same checkout, which WSL mounts under `/mnt/c`:

```powershell
$env:GOOS = "linux"; $env:GOARCH = "amd64"; go build -o itemba ./cmd/itemba
wsl -d Debian -- bash -c "cd '/mnt/c/projects/Actual Projects/ITEMBA-NET' && ./itemba --role all"
```

Windows reaches the server at `http://127.0.0.1:8080` through WSL's localhost forwarding. Verified on 18 October 2026: the binary served the dashboard from Debian, a worldwide search for *Sintel* returned Internet Archive sources with a decoded playback start from the Linux Chromium, and a Vault session opened the clean-cinema testbench in local isolated mode. CI builds and tests every commit on Ubuntu as well.

### Phase 1: memory and generality

Phase 1 is under way. Atlas holds the core schema from the white paper, objects, aliases, hosts, locations and evidence, as migration `0002_atlas_core.sql`, applied automatically at startup. Lens (`internal/lens`) parses a search into a typed object query (type, title and year, or a URL or hash), normalises titles so the same film matches itself across spellings, places scout's checks on the verification ladder, and ranks candidates under the hundred cap with host diversity.

Every search writes back to Atlas when a database is configured. Each source becomes a location on its host with evidence at the level its checks earn: listed, reachable, identified or rendered, with method, status, egress country and observation time. A source whose title matches the query joins the query's object; any other title gets its own object, because a location belongs to what it says it holds. Pruned sources are not locations and are skipped.

`GET /api/scout/search/stream?query=Sintel&region=ALL&fullLengthOnly=true` is the read-through search, as server-sent events:

- `known`: what Atlas already holds for the object, ranked, with effective levels after freshness decay. It arrives immediately.
- `report`: the fresh discovery burst, in the same shape as `POST /api/scout/search`, plus `atlas` with what was written.
- `merged`: the ranked hundred across known and fresh locations, each with its level and name, observation time, egress and origin (`index`, `fresh` or `both`).
- `done`, with the total duration.

`POST /api/scout/search` keeps its shape and gains an `atlas` summary when a database is configured. Freshness decay: rendered evidence keeps its level for 24 hours, identified for three days and reachable for seven, then drops one step until the location is probed again. Level 4, clean, is not yet awarded, because ad exposure is measured in Phase 5.

**A local database without Docker.** PostgreSQL 17 with pgvector runs inside the Debian WSL2 distro, which is the execution host anyway:

```powershell
wsl -d Debian -u root -- bash -c "apt-get install -y postgresql postgresql-17-pgvector"
```

Then set `listen_addresses = '*'` in `/etc/postgresql/17/main/postgresql.conf`, allow the role in `pg_hba.conf` with `host all itemba 0.0.0.0/0 scram-sha-256`, restart the service, and as the `postgres` user create `ROLE itemba LOGIN SUPERUSER PASSWORD 'itemba-dev'`, the `itemba` database owned by it, and `EXTENSION vector`. Inside the distro the binary connects to `postgres://itemba:itemba-dev@127.0.0.1:5432/itemba`. From Windows, use the distro's address from `wsl -d Debian -- hostname -I` instead of `127.0.0.1`, because this machine also runs a native Windows PostgreSQL service on port 5432. The same URL works for `ITEMBA_TEST_DATABASE_URL` when running the live Atlas tests from Windows.

Verified on 18 October 2026 on the WSL2 host with that database: a cold search for *Sintel* wrote three objects, two locations and three evidence rows; the repeat search received its known locations from Atlas in the first event within ten milliseconds, with the rendered Internet Archive location first and the egress country shown.

**Title identity in one place.** Scout's title helpers now delegate to Lens: canonical queries, release labels versus years in a title, derivative detection (trailers, reviews, clips and so on), file-name year conflicts and same-feature comparison all live in `internal/lens/title.go` with their own tests, so Atlas, the console and the providers agree on what a title is.

**Golden fixtures.** Four search reports captured from live runs on 18 October 2026 live in `testdata/reports`: a worldwide *Sintel* search with two decoded starts, a Tanzanian *Sintel* search where the sampler could not play the media, a worldwide *Interstellar* search of storefront and web listings, and a Tanzanian *Night of the Living Dead* search. The console test checks that each merges onto the ladder exactly as recorded, and the live Atlas test writes each into a disposable database and reads it back.

**No search cache.** The five-minute in-memory report cache is gone. The server keeps nothing between requests; Atlas is the memory, and the streamed search is the path that benefits. The discovery and shelf feeds keep their short provider throttles until Phase 2 turns providers into drivers.

**The Console reads the stream.** Every search in the dashboard now uses `GET /api/scout/search/stream` through a fetch-based event reader (`web/src/searchStream.ts`), so a newer search cancels an older one the same way as before. A **Ranked locations** section (`web/src/components/RankedLocations.tsx`) shows the index's known locations as soon as the first event arrives, each with its ladder badge (listed, reachable, identified, rendered, clean), host and access, the check age and egress country, and an origin marker; when the merged event arrives the section becomes the ranked hundred across index and fresh results, with origins `from index`, `fresh discovery` or `index + fresh`. Each card opens an evidence panel with level, method, status, egress and observation time, and an **Open with ad blocking** action. Without a database the section says so in one line and shows fresh results only. The detailed report cards below it are unchanged.

Verified in the browser on 18 October 2026 against the WSL2 database: a first *Sintel* search showed the index empty within two seconds and the ranked list after the burst; the repeat search showed its two known locations from the index within a second, marked *from index* with their check age, and the merged list then marked them *index + fresh*.

With that, every Phase 1 workstream in the implementation plan is in place: schema, Lens, read-through search, write-back with the cache retired, the ladder with decay, the Console and the golden fixtures.

For hot-reloading frontend development on `http://127.0.0.1:5173`:

```powershell
cd web
npm run dev
```

### 2. Optional: Build & Run Dockerized Linux RBI Sandbox (`itemba-vault`)

```powershell
docker compose up -d --build
```
Once built, toggle the **Runtime** badge in the top navigation bar to **Docker Container** to spawn hardened Linux containers (`--cap-drop=ALL`, `--security-opt=no-new-privileges`, CoreDNS `0.0.0.0` sinkhole).
