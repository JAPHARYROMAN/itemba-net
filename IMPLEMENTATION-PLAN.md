# ITEMBA NET Implementation Plan

Version 1.0 | 18 October 2026 | Companion to [WHITEPAPER.md](WHITEPAPER.md) version 2.0

---

## 1. Purpose and how to read this

This plan turns the white paper into a sequence of buildable phases. Each phase states its goal, its workstreams, what it delivers, how you know it is finished, and what it depends on. Exit criteria are observable: a thing you can do or a number you can read. Nothing in this plan is code; it is the order of work and the decisions that shape it.

The phases follow the white paper, with one addition at the front. Phase 0 exists because the vision needs version control, a Linux execution host, persistent storage and a single binary with roles before any of the interesting work is worth starting.

Effort is given in focused weeks for one engineer working full time. Treat the numbers as relative sizes rather than promises. Summed across Phases 0 to 6 they come to roughly 40 to 60 weeks, which is to say about a year of serious work for one person, with the research tracks open-ended beyond that.

---

## 2. Ground rules

Carried forward from the current code, because they are good:

- Every network action has a bounded time and byte budget.
- Every verification carries its method, timestamp and egress location.
- Unknown is an explicit state. Nothing silently falls back to a weaker mode.
- The ordinary test suite runs offline on fixtures. Live checks are opt-in by environment variable.

New for this plan:

- **Git from day one.** Every phase is a series of small commits on main, with a tag at each exit criterion.
- **One module, one binary, roles by flag.** The same executable runs as console, crawler, probe, or all three. This is what lets the system span one laptop or three machines without a second codebase.
- **Linux is the execution host** for Vault, Probe and the crawler's rendering tier. MicroVMs, per-session network namespaces and GPU access all require it. Windows remains a good place to develop the Console and the Go code.
- **PostgreSQL is the system of record from Phase 1.** SQLite is not used as a stepping stone, because the crawler, the probes and the console write concurrently from the start.
- **Every new subsystem ships with its metrics** from section 14 of the white paper, on the day it lands.
- **Feature flags gate incomplete domains and tiers**, so main is always runnable and demonstrable.

---

## 3. Target shape of the codebase

The current layout maps onto the white paper's subsystems as follows. Names in the left column exist today.

| Today | Target | Notes |
|---|---|---|
| `cmd/itemba-server` | `cmd/itemba` with `--role console, crawler, probe, all` | Handlers move out of main into the console package |
| handlers in `main.go` | `internal/console` | HTTP, WebSocket and result streaming |
| in-memory caches in `internal/scout` | `internal/atlas` | Schema, migrations, repositories, freshness, host reputation |
| matching logic scattered in `internal/scout` | `internal/lens` | Object model, query parse, normalisation, matching, fingerprints |
| provider search in `internal/scout` | `internal/scout` | Frontier, scheduler, fetch tiers, driver runtime, discovery burst |
| playback sampler in `internal/scout` | `internal/probe` | Verification workers per domain, evidence capture |
| `internal/vault` | `internal/vault` | Sessions, isolation tiers, media streaming, egress, snapshots |
| `internal/shield` | `internal/shield` | Layers L0 to L6, host profiles, exposure measurement |
| `internal/relay` | `internal/relay` | Unchanged role, more formats |
| `internal/cdp` | `internal/cdp` | Unchanged |
| none | `internal/domain/{video, audio, image, document, game, software, live}` | Per-domain identity, probe and presentation adapters |
| none | `drivers/` | Host driver definitions as data files, with small scripts where needed |
| `web/` | `web/` | The Console frontend |
| `deploy/` | `deploy/` | Compose files, images, microVM root filesystems, egress configurations |

---

## 4. Technology decisions

These are the choices the plan assumes. Each can be revisited, but changing one later costs more than deciding now.

| Concern | Decision | Why |
|---|---|---|
| Language | Go throughout, as today | One toolchain, static binaries, the existing code |
| System of record | PostgreSQL with full-text search and the pgvector extension | Concurrency, durability, text and vector search in one place |
| Job queues | Postgres tables with row locking, no separate broker | Fewer moving parts; revisit only if cross-machine push is needed |
| Browser automation | Chromium over the DevTools Protocol, as today | The existing client already does interception and screencast |
| Isolation tiers | Containers, then a sandboxed container runtime such as gVisor, then Firecracker microVMs | Three cost levels matched to host reputation |
| Session streaming | Pion WebRTC, as today, upgraded to encoded video and audio tracks | Lower bandwidth and smoother than JPEG frames over a data channel |
| Video encoding | Hardware encoding where present, software H.264 otherwise, with a 720p fallback | Encoding cost is the main scaling limit of Vault |
| Egress | A network namespace per session, WireGuard tunnels to chosen exits, a proxy pool, Tor as an option | Country selection and recorded egress on every piece of evidence |
| DNS inside sessions | The existing CoreDNS sinkhole as the only resolver | Layer 1 of Shield applies to every session and every probe |
| Fingerprints | Acoustic fingerprints for audio, perceptual hashes for images, frame-sequence hashes for video, text similarity hashes for documents, file hashes for software | Identity from content rather than page titles |
| Emulation | Browser-hosted emulator cores inside the sandbox; native titles from a GPU node captured into the same WebRTC path | Games without a second streaming stack |
| Documents | PDF and EPUB rendered by browser libraries inside the sandbox; office formats converted to PDF by a headless office container | The host never opens the file |
| Driver scripting | A declarative data format first, with an embedded scripting language for the minority of hosts that need logic | Drivers as data, testable against recorded fixtures |
| Frontend | React, TypeScript and Vite, as today | No reason to change |
| Observability | A Prometheus metrics endpoint and Grafana dashboards, structured logs | The white paper's measures become graphs |

---

## 5. Phases

### Phase 0: Foundations

**Goal.** A repository, a Linux host, a database, one binary with roles, metrics, and every current feature still working.

**Effort.** 2 to 3 weeks.

**Workstreams.**

1. Version control and continuous integration. Initialise git, commit the current tree, add a pipeline that runs vet, the Go tests, the TypeScript check and the frontend build on every push.
2. Linux execution host. Choose between a dedicated Linux machine, a VPS with nested virtualisation, or WSL2 with virtualisation enabled. Install Docker and confirm KVM is available. Document the two-host development loop: edit on Windows, run Vault and Probe on Linux.
3. Binary roles. Move the handlers out of the current main package into the console package. Introduce the role flag. Each role starts only the subsystems it needs.
4. PostgreSQL. Add it to the compose file, choose a migration tool, add a health check and a connection pool.
5. Observability. Expose metrics. Build the first dashboard: search duration, probe outcomes by status, Shield counters by layer.
6. Configuration. Replace the ad hoc local file parsing with one configuration file plus environment overrides. Secrets stay in the environment.

**Exit criteria.** The binary in the all role on the Linux host serves the current dashboard, completes a movie search, and opens a Vault session. The pipeline is green. The dashboard shows the search.

**Depends on.** Nothing.

---

### Phase 1: Memory and generality

**Goal.** Every search reads Atlas first. Every probe writes back. The request is a typed object, not a movie.

**Effort.** 5 to 7 weeks.

**Workstreams.**

1. Atlas schema. Tables for objects, aliases, hosts, locations, evidence, sessions, rules and host reputation. Freshness windows per object type. Full-text and vector columns on objects and aliases.
2. Lens version one. A query parser that infers type, normalises titles, extracts year, edition and language, and expands aliases including transliterations. The matching logic now spread across the scout package moves here. Every match records the basis it was made on.
3. Read-through search. The console search handler consults Atlas and streams known results immediately, then launches the discovery burst using the existing providers and streams additions. The hundred cap, displacement of weaker results and host diversity live here.
4. Write-back. Every provider result, availability check and playback sample becomes a location row and an evidence row. The in-memory search, discovery and shelf caches are deleted.
5. The verification ladder. Map today's statuses to Levels 0 to 4. Add the decay job that drops stale evidence one level until re-probed.
6. Console. Typed results, live list updates, an evidence panel per card, level badges with method, time and egress.
7. Fixtures. The proof files in the runtime folder become golden fixtures for write-back and ranking tests.

**Exit criteria.** A repeated search returns from Atlas in under one second at the 95th percentile with at least the verified results it had before. The list never exceeds one hundred entries. An evidence row exists for every badge shown. The old cache code is gone.

**Depends on.** Phase 0.

---

### Phase 2: The crawler

**Goal.** Discovery runs continuously and independently. Providers become drivers.

**Effort.** 6 to 9 weeks.

**Workstreams.**

1. Frontier. A persistent URL queue with priority, host, next-fetch time and tier. A scheduler with per-host token buckets.
2. Fetch tiers. Tier one is the HTTP client with identity profiles. Tier two is the pooled headless renderer, reusing the existing browser pool. Tier three is a full Vault session. Escalation happens only when a cheaper tier yields nothing.
3. Driver format and runtime. A declarative file describing search URL templates, catalog walking, candidate extraction, object field mapping, pagination and rate limits, with an optional embedded script for logic. A runtime that executes drivers and a harness that tests them against recorded fixtures.
4. Conversion of existing providers. Apple, Internet Archive, PeerTube, Wikimedia Commons, the public-domain movie sites, Fawesome, NFB, NASA+, Pluto TV and Filmzie each become a driver. The bespoke code for each is deleted once its driver passes a parity test.
5. Discovery burst. Orchestration across the frontier neighbourhood, host search forms, sitemaps and feeds, link neighbourhoods of verified locations, and external seeds, under per-host and overall budgets.
6. Continuous crawl. The crawler role runs unattended: freshness-driven re-crawl, host productivity learning, novelty exploration within a storage budget.
7. Identity management. User agent and header profiles, ban detection with backoff, and hooks for egress rotation that Phase 4 fills in.
8. External search as seed only. Tavily and Brave results enter the frontier. They never enter the results list directly.

**Exit criteria.** The crawler runs for 72 hours unattended and Atlas grows throughout. A search for a title the system has never seen returns Level 2 or better results from drivers without an external search call. All legacy provider code is removed and every driver parity test passes.

**Depends on.** Phase 1.

---

### Phase 3: Domains

**Goal.** Audio, images and documents are first-class. The fingerprint library exists.

**Effort.** 8 to 12 weeks, roughly three per domain plus the shared services.

**Workstreams.** Each domain gets the same four pieces: identity methods in Lens, a probe method, a Vault presentation, and a first set of drivers.

1. Audio. Identity from tags, duration and acoustic fingerprint. Relay streaming with range support. A queue player in the Console with gapless playback. The probe confirms decoded audio progress.
2. Images. Identity from perceptual hash with near-duplicate clustering. A zoomable viewer with galleries and side-by-side variants. The probe confirms decoded dimensions. Scout gains reverse-image neighbourhood expansion.
3. Documents. Identity from extracted text and similarity hash, with title, author, page count and identifiers where present. PDF and EPUB render inside the sandbox; office formats convert to PDF in a container first. The probe renders the first pages and confirms page count.
4. Fingerprint library. A service and storage for every fingerprint type. The video probe gains a sampled frame-sequence fingerprint.
5. Format matrix. A table of what Vault can decode per tier. No result may be labelled playable outside it.
6. Lens version two. Cross-type parsing and vector similarity for fuzzy and cross-language matching.

**Exit criteria.** A query of each new type returns a filled, verified list. On a fixture set, fingerprints confirm identity on at least nine in ten Level 3 results. No result is labelled playable outside the format matrix.

**Depends on.** Phase 1. Benefits from Phase 2 drivers but can start before Phase 2 finishes.

---

### Phase 4: Vault upgrade

**Goal.** Encoded streaming, three isolation tiers, per-session egress, multiple sessions, snapshots and evidence capture.

**Effort.** 8 to 12 weeks.

**Workstreams.**

1. Media tracks. A capture pipeline from the session display and audio sink into encoded video and audio tracks over Pion. Adaptive bitrate. Theater mode becomes a crop of the capture. The JPEG frame path is removed once parity is reached.
2. Isolation tiers. The container tier exists. Add a sandboxed runtime for the medium tier and Firecracker microVMs with a prepared root filesystem for the hostile tier. Tier selection from host reputation with an operator override. Warm pools per tier.
3. Egress. A network namespace per session. WireGuard exits with a country table, a proxy pool and Tor. CoreDNS is the only resolver inside the namespace. The egress used is written to every evidence row. Scout and Probe use the same mechanism.
4. Multi-session. A session registry, tabs in the Console, one WebRTC peer per session.
5. Snapshot and handoff. Checkpoint and restore for containers, snapshots for microVMs, and handoff between devices by renegotiating the peer.
6. Zero residue and export. Sealed clipboard and downloads. An explicit export action moves a file to a quarantined archive that is hashed and fingerprinted on arrival.
7. Evidence capture. Screenshots, request logs, media manifests and Shield decisions recorded per session. Probe becomes Vault running unattended with capture always on.

**Exit criteria.** A 1080p session streams at 30 frames per second with audio. A hostile-tier session runs inside a microVM. The same probe from two exit countries produces two distinct evidence rows. Two sessions run at once. A snapshot resumes.

**Depends on.** Phase 0 for the Linux host and Phase 1 for evidence rows. Independent of Phases 2 and 3.

---

### Phase 5: Shield depth

**Goal.** Zero measured ad exposure, learned per-host profiles, and honest breakage handling.

**Effort.** 5 to 8 weeks.

**Workstreams.**

1. Layer 0, egress policy. Per-session egress and per-host network allowances enforced in the namespace.
2. Layer 4, media timeline. Ad-cue detection in HLS and DASH manifests, discontinuity analysis of segments, suppression of client-side ad frameworks, extending the playlist filtering that already exists.
3. Layer 5, behavioural. A navigation-chain monitor, popunder detection, forced-navigation suppression and fake-button recognition.
4. Per-host profiles. Which layers a host needs, which rules break it, and what exceptions are active, learned from sessions and probes and editable by the operator.
5. Exposure measurement. Blocked and allowed requests by layer, timeline modifications, and visible ad regions once Layer 6 exists. A clean rating per host computed over repeated sessions.
6. Breakage handling. A one-step, specific, visible and reversible relaxation in the Console. A relaxed session can never produce Level 4 evidence.
7. Layer 6, visual. A stub that accepts frame samples and returns regions, ready for the research track.

**Exit criteria.** Every session reports an exposure number. Every host with enough sessions has a clean rating. An automated run over a fixture set of hostile pages shows zero visible ads. Breakage rate is measured and graphed.

**Depends on.** Phase 4 for evidence capture and egress. Layers 4 and 5 can start earlier against the current Vault.

---

### Phase 6: Games

**Goal.** Games are a result type you can play.

**Effort.** 6 to 10 weeks.

**Workstreams.**

1. Browser games. Detection of playable HTML5 titles, presentation inside the sandbox under Shield, keyboard and mouse input as today.
2. Emulated games. Emulator cores hosted in the sandbox browser, controller input mapped over the data channel, a latency budget, and save states folded into session snapshots.
3. Native games. Provisioning a GPU node, capturing its display and audio into the same WebRTC path, forwarding controller input.
4. Identity and drivers. Title, platform, build and archive hash in Lens; drivers for game portals and archives.

**Exit criteria.** A browser game and an emulated title play end to end with a controller under full protection. A native title streams from the GPU node.

**Depends on.** Phase 4 streaming and tiers.

---

### Phase 7: Research tracks

These run after, or alongside, the phases above and never block them. Each is framed as a hypothesis with a measure so that it can be judged.

| Track | Hypothesis | First measure | Needs |
|---|---|---|---|
| Driver synthesis | One observed session on an unknown host is enough to generate a working driver | Share of unknown hosts that yield a passing driver from one session | Phase 2 runtime, Phase 4 capture |
| Self-healing drivers | A host change can be detected and the driver re-derived without manual work | Mean time from host change to restored parity | Driver synthesis |
| Fingerprinting at scale | A ten-second sample identifies the object as reliably as full metadata | Identity precision on a held-out fixture set | Phase 3 library |
| Visual ad detection | Advertising regions can be recognised from pixels without blocklists | Precision and recall on labelled frames | Phase 5 Layer 6 stub |
| Speculative verification | The operator's selection can be predicted well enough to pre-verify it | Share of selections that were already at Level 3 when clicked | Phase 1 sessions |
| Egress experiments | Parallel probes from several exits produce a useful availability map | Agreement rate between exits per host | Phase 4 egress |
| Game execution | Emulated input latency can stay under a playable threshold over the stream | Measured input-to-frame latency | Phase 6 |
| Natural-language querying | Loose descriptions resolve to the right object against Atlas | Top-one resolution rate on a query set | Phase 3 Lens version two |
| Personal archive | Exported objects can be deduplicated and searched like any other location | Duplicate rate and search recall over the archive | Phase 4 export |
| Session continuity | A sandbox can move between devices and resume days later with state intact | Resume success rate and time | Phase 4 snapshots |

---

## 6. Cross-cutting workstreams

**Console.** Every phase lands its interface in the same release: typed results and evidence panels in Phase 1, driver and crawl status in Phase 2, players and viewers in Phase 3, tabs and egress choice in Phase 4, exposure and breakage controls in Phase 5, controllers in Phase 6.

**Testing.** Unit tests and fixture tests remain the default. Each driver carries recorded fixtures and a parity test. The runtime proof files become golden fixtures. Live checks stay opt-in. A nightly crawl smoke test runs on the Linux host.

**Observability.** The measures in section 14 of the white paper are the metrics catalogue. Each is owned by the phase that makes it measurable, and the dashboard grows phase by phase.

**Host security.** The sandbox protects the operator only if the host is hardened. Services run unprivileged, images are read-only, system call filtering is on, no volumes are shared with sessions, and session networks are segmented from the host network. A checklist per tier is maintained from Phase 4.

**Data management.** Evidence artefacts have storage budgets and retention windows. Atlas is backed up. Frontier growth is capped per host and overall.

**Infrastructure.** A Windows workstation for development and the Console. A Linux node with virtualisation for the crawler, probes and Vault. An optional GPU node for games and high-resolution sessions. Two or three inexpensive virtual servers as egress exits in different countries.

---

## 7. Sequencing

The dependency structure allows two tracks to run side by side once Phase 1 is done.

| Stretch | Primary track | Parallel track |
|---|---|---|
| 1 | Phase 0: Foundations | none |
| 2 | Phase 1: Memory and generality | none |
| 3 | Phase 2: The crawler | Phase 4: Vault upgrade, streaming and tiers |
| 4 | Phase 3: Domains, audio first | Phase 4: Vault upgrade, egress and snapshots |
| 5 | Phase 3: Domains, images and documents | Phase 5: Shield depth |
| 6 | Phase 6: Games | Research tracks begin |

If only one track can be worked at a time, the order is 0, 1, 2, 4, 3, 5, 6. Vault comes before the new domains because egress and capture improve every probe that follows.

---

## 8. Milestone demonstrations

Each milestone is something you can show in under five minutes.

- **M0.** On the Linux host, one binary serves the dashboard, runs a movie search and opens a sandbox. A dashboard graph moves.
- **M1.** Search the same film twice. The second search answers instantly from Atlas with evidence badges, and the list is capped at one hundred with live updates.
- **M2.** Search a film the system has never seen while the crawler has been running for days. Results come from drivers alone. Open the crawl status page and watch the frontier move.
- **M3.** Search an album, an image and a book. Each returns a verified list and opens in its own viewer or player inside the sandbox.
- **M4.** Open a hostile site in a microVM session with video and audio streaming, switch the exit country, and see two different evidence rows for the same page. Open a second session beside it.
- **M5.** Open a page known for aggressive advertising. The exposure counter reads zero. Deliberately relax protection and watch the evidence refuse to reach Level 4.
- **M6.** Pick a game from results and play it with a controller.

---

## 9. Risks

| Risk | Effect | Mitigation |
|---|---|---|
| Development stays Windows-only | Isolation tiers, egress and GPU work cannot start | Phase 0 makes the Linux host a hard exit criterion |
| Video encoding cost | Vault sessions saturate the CPU | Hardware encoding first, 720p fallback, theater crop reduces the encoded area |
| Crawler bans | Productive hosts go dark | Identity profiles, egress rotation, backoff, driver fixtures to detect breakage quickly |
| Fingerprinting cost | Probes slow down | Sample small portions, cache by location, compute only in Probe |
| Driver maintenance burden | Drivers rot faster than they are fixed | Fixtures and parity tests, productive hosts first, the self-healing research track |
| MicroVM complexity | Phase 4 stalls | Ship the sandboxed container runtime as the medium tier first and add microVMs behind a flag |
| Data growth | Evidence artefacts fill disks | Budgets and retention from Phase 1, artefact sampling for low-value hosts |
| Scope creep | Phases never close | Exit criteria define done; research tracks never block a phase; feature flags keep main runnable |

---

## 10. The first thirty days

1. Initialise git and commit the current tree. Add the pipeline.
2. Stand up the Linux host with Docker and virtualisation confirmed.
3. Move the handlers into the console package and add the role flag.
4. Add PostgreSQL with migrations and the first Atlas tables: object, host, location, evidence.
5. Expose metrics and build the first dashboard.
6. Make the movie search write locations and evidence to Atlas while still answering from the existing code paths.
7. Flip the search to read Atlas first, and watch the second search of any title return in under a second.

By day thirty, Phase 0 is complete and Phase 1 is visibly under way. Everything that follows builds on a system that remembers.

---

## Appendix A: Legacy code retired by phase

| Phase | Removed |
|---|---|
| 0 | Handler code in the server main; the ad hoc local environment file parser |
| 1 | In-memory search, discovery and shelf caches; movie-only request types |
| 2 | Every bespoke provider implementation in the scout package, once its driver passes parity |
| 4 | The JPEG frame streaming path |
| 5 | Hand-maintained per-site exceptions that per-host profiles now learn |

## Appendix B: Metrics by owning phase

| Measure | Phase |
|---|---|
| Time to first result | 1 |
| Time to first verified result | 1 |
| Fill | 1 |
| Verification rate | 1 |
| Identity precision | 3 |
| Ad exposure | 5 |
| Breakage | 5 |
| Freshness | 1 |
| Coverage | 2 |
