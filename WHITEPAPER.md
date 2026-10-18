# ITEMBA NET

## A Sandboxed Universal Search and Access Environment

White Paper, Version 2.0 | 18 October 2026

This document supersedes the ITEMBA NET Manifesto 1.1. It is written for a single operator building a personal research platform to test technologies and the limits of what one system can do. It is not a product plan. It makes no promises to users, partners or regulators, because there are none.

---

## Abstract

ITEMBA NET answers one question for any piece of media or any document: where on the global internet is this hosted, and how do I reach it cleanly?

A query names an object: a film, an album, a track, a game, a picture, a book, a paper, an archive. The system searches its own index and its own crawl of the web, confirms which locations actually hold that object, and returns at most one hundred candidate locations ranked by evidence. The operator selects one. The selection opens inside an isolated sandbox that streams the result back to the operator's screen with every advertisement, tracker, redirect and popup removed before it renders. The sandbox is the only path to the content. The operator's machine never touches a remote page directly.

Three ideas define the system.

**Search is object-centric.** It finds hosts of a thing, not pages about a thing.

**Discovery is independent and unrestricted.** The system crawls for itself. It does not filter another engine's results, does not keep a list of approved sources, and does not refuse to index a location because the page around it is hostile.

**Safety lives at the boundary, not in the index.** Because every result opens inside an isolated, ad-free sandbox, the crawler is free to index anything and the operator is free to open anything. Risk is contained where it is encountered, not predicted in advance.

---

## 1. The problem

General search engines rank pages by authority and relevance to text. They do not know whether a page hosts a specific film or merely discusses it, and they are indifferent to what happens to you after you click. Dedicated aggregators know where licensed content lives, but only within the catalogs they are paid to cover. Ad blockers protect a browser tab but not the machine, and they break constantly on the sites where they are needed most. Remote browser isolation exists in enterprise security, disconnected from any notion of finding things.

Reaching a piece of media on the open web therefore means three separate struggles: finding which of thousands of candidate pages actually hold it, surviving the page once you arrive, and keeping your own machine out of the blast radius. No system unifies finding, verifying and safely opening across every kind of media. ITEMBA NET is that system, built for one person who wants to see how far it can go.

---

## 2. What ITEMBA NET is

The operator types a title, a few keywords, a file name, a content hash, or a URL. Within a second, locations already known to the index appear. Over the following seconds, a live discovery burst fans out across the web, matches candidates against the requested object, verifies them, and fills the list. The list is capped at one hundred. Better candidates displace worse ones; the cap never grows.

Each location in the list carries:

- the host and exact URL;
- the object match confidence and what it was matched on;
- its verification level, from merely listed to confirmed clean playback;
- access terms where known: free, sign-in, paid, region-locked, or unknown;
- the time of the last check and the egress location it was checked from;
- the Shield profile for that host and its measured ad density.

Selecting a location opens it in Vault. Video plays, audio streams, images render, documents open in a reader, browser games run, emulated games run with controller input, and software archives can be inspected, all inside the sandbox and all with Shield active. Nothing is downloaded to the operator's machine unless the operator explicitly exports it.

ITEMBA NET is not a meta-search that filters other engines. It is not a curated catalog. It is not a browser extension. It is a search engine with its own crawler, an identity engine that knows what an object is, a verification fleet that proves where it is, and a sandbox that makes opening it safe and clean.

---

## 3. Principles

1. **Object-centric.** A query names a thing. Results are locations that hold that thing. Pages that merely mention it rank at the bottom or not at all.

2. **Independent discovery.** The crawler, index and ranking belong to the system. External search APIs are permitted only as bootstrap seeds when the index is thin on a topic, never as the source of results.

3. **Unrestricted index, guarded boundary.** The index does not judge sources. Scout does not classify hosts as legitimate or illegitimate and does not skip a page because it is ugly, aggressive or obscure. Hostility is Vault's and Shield's problem. The index's only question is whether a location hosts the object.

4. **The hundred.** A query returns at most one hundred locations. The cap is a deliberate constraint that forces ranking quality: one hundred is enough to represent the whole internet's offer for an object and small enough to be read by a person.

5. **Ad-free by construction.** Everything viewed through the system is free of advertising. This is a property of the sandbox, not a feature per site, and it applies to banners, overlays, popups, popunders, redirects, video ads inserted by players, and ads stitched into media streams.

6. **Evidence over assertion.** A result is a claim backed by a probe. Every verification label names the check performed, when, and from where. Empty results are valid results.

7. **Zero residue.** Sessions are ephemeral. Profiles, caches, cookies and downloads die with the session. The host machine holds only the index and what the operator chose to keep.

8. **Personal scale, no compromise.** One operator, a few machines, no multi-tenant concerns, no release constraints. Every design decision is free to favour capability over caution.

---

## 4. Architecture

The system is eight cooperating subsystems. Four exist in the current codebase and are extended; four are new.

| Subsystem | Role | Status |
|---|---|---|
| **Console** | The operator's interface: search, results, sandbox viewport, telemetry, session control | Exists as the web dashboard |
| **Scout** | Autonomous crawler and live discovery fleet | Exists as federated provider search; becomes a true crawler |
| **Atlas** | Persistent index of objects, hosts, locations and evidence | New |
| **Lens** | Object identity: deciding that a location holds the requested thing | New, absorbing current matching logic |
| **Probe** | Verification workers that open candidates in disposable sandboxes and record evidence | Exists as the playback sampler; generalises to all media |
| **Vault** | The sandbox: isolated sessions streamed to the Console | Exists; upgraded to video streaming, microVM isolation and per-session egress |
| **Shield** | Ad, tracker, redirect and popup elimination across every layer | Exists; gains media-timeline and behavioural layers |
| **Relay** | Zero-trust media proxy for streams and files that bypass the page | Exists |

Data flows in two loops.

**The discovery loop.** Scout fetches and renders pages, Lens decides what objects they hold, Probe verifies the promising ones, and Atlas records everything with timestamps. This loop runs continuously in the background, prioritised by what the operator searches for and how stale existing records are.

**The access loop.** The Console sends a query, Atlas answers instantly, Scout runs a live burst to fill gaps, the ranked hundred stream to the Console, the operator selects, Vault opens a session with Shield enforced on every byte, and the session is streamed back as video, audio and input. Evidence gathered during the session feeds back into Atlas.

```
operator -> Console -> Atlas ------------------> ranked <=100 -> selection
               |         ^                                         |
               +-> Scout -> Lens -> Probe -+                       v
                                                    Vault session -> Shield -> Egress -> web
                                                         |
                                                         +-> video + audio + input stream -> Console
                          media URLs -> Relay (Shield-filtered) -> Vault player
```

---

## 5. The search pipeline

**Parse.** The query is interpreted into an object description: likely type, normalised title, aliases and transliterations, year, edition, language, format hints, and any explicit constraints such as a preferred egress country. A raw URL or hash short-circuits to identification.

**Atlas lookup.** Known locations for the object return immediately, ordered by their stored rank. This is the sub-second path and the reason the index exists.

**Discovery burst.** Scout fans out in parallel across: the crawl frontier near the object's neighbourhood, the search forms of hosts already known to carry this object type, sitemaps and feeds of those hosts, link neighbourhoods of previously verified locations, and bootstrap seeds from external search when Atlas is thin. Each host has a request budget; the burst as a whole has a time budget.

**Lens matching.** Every candidate page is reduced to the objects it hosts and compared with the request. Matching uses the identity methods of the object's type, described in section 6, and produces a confidence with a stated basis.

**Verification.** Candidates above a confidence threshold are opened by Probe workers in disposable sandboxes. The ladder below defines what each level means. Verification is bounded per candidate and in aggregate; unverified candidates remain in the list labelled as such.

**Rank and cap.** The hundred are chosen by relevance, verification level, freshness, access terms, host reputation learned from the system's own history, and measured protection quality. Diversity across hosts is enforced so that a single mirror farm cannot fill the list.

**Stream.** Results reach the Console as they are produced. The list is live: verification badges update, displaced candidates fall off, and the top of the list stabilises first.

### The verification ladder

| Level | Name | Meaning |
|---|---|---|
| 0 | Listed | A page references the object. Nothing has been checked. |
| 1 | Reachable | The location responds with content from the current egress. |
| 2 | Identified | Lens confirmed the object at this location from metadata, file inspection or fingerprint. |
| 3 | Rendered | Probe opened it in a sandbox and the object played, displayed, ran or opened for a bounded sample. |
| 4 | Clean | Level 3 was achieved with Shield fully active and zero measured ad exposure. |

Every level records the method, the timestamp and the egress country. A Level 4 result from one country is not a claim about another. Verification decays: a record older than its type's freshness window drops one level until re-probed.

### Why one hundred

A cap turns ranking from a nicety into the core problem. With unlimited results, a system can hide weak ranking behind volume. With one hundred, every slot has to be earned, and the operator can actually read the whole offer. The number is also an engineering budget: one hundred candidates is what a few Probe workers can keep verified for the objects the operator cares about.

---

## 6. Media domains

The system is general-purpose. Each domain differs in how objects are discovered, how identity is established, and what Vault does with a selection.

| Domain | Discovery emphasis | Identity | In Vault |
|---|---|---|---|
| **Video** (films, series, clips, live) | Catalog pages, player pages, HLS and DASH manifests, embed frames | Title, year, runtime, episode, perceptual video fingerprint from a sample | Protected player, theater isolation, ad-cue stripping, subtitle discovery |
| **Audio** (tracks, albums, mixes, podcasts) | Track pages, playlists, feeds, direct MP3 and FLAC files | Tags, duration, acoustic fingerprint | Gapless queue player streamed through Relay, album and playlist views |
| **Images** (photos, art, scans) | Galleries, image hosts, CDN paths, reverse-image neighbourhoods | Perceptual hash, dimensions, EXIF, near-duplicate clusters | Zoomable viewer, galleries, side-by-side variants |
| **Documents** (PDF, EPUB, office, papers) | Repositories, library pages, direct files | Text hash, title and author extraction, page count, DOI and ISBN where present | Sandboxed reader with text search; the host never opens the file |
| **Games** | Browser game portals, emulator-ready archives, cloud-playable titles | Title, platform, build, archive hash | HTML5 games run directly; emulated titles run on in-sandbox cores with controller input over the stream; native titles run on a GPU Vault node and stream like any session |
| **Software and archives** | Mirrors, release pages, package indexes | File hash, version, signature | Inspected and extracted inside the sandbox; export only on explicit choice |
| **Live streams** | Schedules, channel pages, manifests | Channel identity, schedule match | Player with latency controls and ad-break suppression |

Two cross-cutting services apply to every domain. A **fingerprint library** lets Lens confirm identity from the content itself, so that a ten-second sample can prove a location holds film A rather than trusting a page title. A **format matrix** records what Vault can natively render, so that a result is never labelled playable for a format the sandbox cannot decode.

---

## 7. Scout: the crawler

Scout grows from a set of provider adapters into an autonomous, focused crawler.

**Frontier.** A prioritised queue of URLs scored by expected value: proximity to objects the operator has searched for, host productivity in past crawls, freshness decay of existing records, and novelty of the host. The frontier is persistent and survives restarts.

**Fetch tiers.** Plain HTTP fetching for the majority of pages, a pool of headless rendering workers for JavaScript-heavy hosts, and full Vault sessions for hosts that defend themselves. Scout escalates a URL through tiers only when the cheaper tier fails to yield.

**Host drivers.** For hosts that carry many objects, Scout learns or is given a driver: how to search the host, how to walk its catalog, how to extract object candidates from its pages. Drivers are declarative where possible and scripted where necessary, and they are the system's accumulated knowledge of where things live.

**Identity management.** Rotating user agents, header profiles, and egress identities so that one host's defences do not starve the crawl. This is an engineering measure for continuity, not a stance.

**Politeness as self-preservation.** Per-host concurrency and rate limits exist to keep the crawler welcome, or at least unnoticed, long enough to finish. They are configurable and are not a principle.

**Scale.** A personal deployment targets tens of millions of frontier URLs, a few million rendered pages, and continuous coverage of a few thousand productive hosts, running on one to three machines. Breadth comes from focus, not from attempting to crawl the whole web.

---

## 8. Atlas and Lens: the index and the identity engine

**Atlas** is the persistent memory of the system. Its core records are:

- **Object**: a canonical thing with type, title, aliases, year, creators, external identifiers and fingerprints.
- **Host**: a site with its drivers, Shield profile, reputation, and crawl budget.
- **Location**: a URL on a host that holds or claims to hold an object, with access terms.
- **Evidence**: one probe result for one location: level, method, timestamp, egress, and artefacts such as screenshots, manifests and request logs.
- **Session**: an operator session with its selections and what Shield observed, feeding host reputation and rule learning.

Storage is a relational database with full-text search for titles and aliases, and a vector index for fuzzy and cross-language matching. Records carry freshness windows per object type. Host reputation is learned entirely from the system's own history: break rate, ad density, redirect behaviour, malware encounters, and how often its locations reach Level 4.

**Lens** decides whether a candidate holds the requested object. Its methods, in increasing cost: metadata comparison with normalisation and alias expansion, structural inspection of files and manifests for duration and dimensions and codec, and content fingerprinting from a sampled portion. Lens always states which basis produced a match, and a stronger basis overrides a weaker one when they disagree.

---

## 9. Vault: the sandbox

Vault is the heart of the system's safety model and the reason discovery can be unrestricted.

**Isolation tiers.** Containers for ordinary sessions, microVMs for hosts with poor reputation or active exploitation history, and GPU-backed nodes for games and high-resolution playback. The tier is chosen per session from host reputation and the operator's override.

**Streaming.** Sessions stream to the Console as an encoded video track with synchronised audio over WebRTC, with mouse, keyboard, touch and controller input returned over a data channel. Bitrate adapts to the link. Theater mode isolates the player element and streams it alone at full quality.

**Egress.** Each session chooses its exit: direct, a VPN endpoint, a proxy pool, or an anonymity network, with country selection. The egress used is recorded on every piece of evidence. This turns the question of regional availability from guesswork into an experiment the operator can run on demand.

**Zero residue.** Profiles are created at session start and destroyed at session end. Clipboard, file system and downloads are sealed. Anything the operator wants to keep is exported through an explicit action that moves it to a quarantined archive, never to the live host system.

**Multi-session.** Several sandboxes can run at once, presented as tabs. A session can be handed between the operator's devices without restarting, and a session can be snapshotted and resumed.

**Evidence capture.** Every session can record screenshots, request logs, media manifests and Shield decisions. Probe is Vault running unattended with capture always on.

---

## 10. Shield: ad elimination

Shield's goal is zero advertising exposure on anything viewed through the system, without breaking the content.

| Layer | Scope | Mechanism |
|---|---|---|
| L0 Egress policy | Where traffic may go | Per-session egress and per-host network allowances |
| L1 DNS | Domain resolution | Sinkhole of aggregated blocklists, refreshed continuously |
| L2 Network | Every request | Interception at the browser protocol level with rule matching on URL, type, initiator and headers |
| L3 DOM | The rendered page | Cosmetic rules, element hiding, script neutralisation, window-spawn lock, overlay and clickjacking sweeper |
| L4 Media timeline | Streams and players | Stripping of server-inserted ad segments, VAST and IMA suppression, ad-cue detection in manifests |
| L5 Behavioural | Page behaviour over time | Redirect chain breaking, popunder detection, forced-navigation suppression, fake-button recognition |
| L6 Visual | What is on screen | Model-based detection of advertising regions from the rendered frame, as a research track |

**Per-host profiles.** Shield keeps a profile per host recording which layers are needed, which rules break the content, and what exceptions are in force. Profiles are learned from sessions and from Probe runs, and can be edited by the operator.

**Breakage handling.** When a page or player fails under protection, the Console offers a one-step relaxation that is specific, visible and reversible. Shield never silently disables itself, and a relaxed session cannot produce Level 4 evidence.

**Measurement.** Ad exposure is measured, not assumed. A session reports blocked and allowed requests by layer, visible ad regions detected, and media timeline modifications. A host earns a clean rating from these measurements over repeated sessions.

---

## 11. Relay

Relay carries media that does not need its page: HLS and DASH manifests, MP4 and WebM files, MP3 and FLAC streams, images and documents. It fetches on behalf of the sandbox through the session's egress, applies Shield rules to every manifest, key, segment and file, supports range requests, caches within bounded storage, and never exposes upstream credentials or signed URLs to the Console. For audio and image browsing, Relay plus a native player is often the whole session, with no page rendered at all.

---

## 12. Research tracks

These are the problems that make the project worth building. None is required for the system to work; each pushes a boundary.

- **Driver synthesis.** Learn how to extract objects from an unknown host by observing a Vault session: which requests carried the media, which page elements led to it, and what the URL structure implies. Turn one manual visit into a reusable driver.
- **Self-healing drivers.** Detect when a host changes and re-derive its driver automatically from a fresh session rather than waiting for a manual fix.
- **Content fingerprinting at scale.** Confirm identity from a short sample of video, audio, image or text, so that no result depends on trusting a page title.
- **Visual ad detection.** Recognise advertising from pixels, independent of any blocklist, and feed the result back into L2 and L3 rules.
- **Speculative verification.** Predict which candidates the operator will select and verify them before the click, so that selection is instant.
- **Egress experiments.** Run the same probe from several exit countries in parallel and present availability as a map rather than a single observation.
- **Game execution.** Run emulated systems inside the sandbox with low-latency input, and stream native titles from a GPU node, making games a first-class result type.
- **Natural-language querying.** Describe an object loosely and let the system resolve it against Atlas and the fingerprint library.
- **Personal archive.** Export verified objects into a quarantined, deduplicated, fingerprint-indexed store that the system can also search.
- **Session continuity.** Hand a live sandbox between devices and resume snapshots days later with state intact.

---

## 13. Non-goals and the operator's stance

The system is built for one operator. It is not multi-tenant, not for release, not monetised, and not designed around provider partnerships or legal defensibility. Those constraints shaped the previous manifesto and are removed here.

Discovery does not pre-judge sources, and the sandbox is what makes that safe. What the operator chooses to search for and open is the operator's own responsibility under the rules that apply to them. The system's job is to make the choice informed, the access clean, and the machine untouched.

---

## 14. Measures

Progress is judged by numbers the system itself produces.

| Measure | Definition |
|---|---|
| Time to first result | Query to first Atlas result on the Console |
| Time to first verified result | Query to first Level 3 or Level 4 result |
| Fill | How many of the hundred slots are occupied for a typical query, by object type |
| Verification rate | Share of the hundred at Level 2 or above when the burst ends |
| Identity precision | Share of Level 2 results that a later fingerprint confirms |
| Ad exposure | Visible ad regions and unblocked ad requests per session; the target is zero |
| Breakage | Share of Level 3 attempts that fail under full protection and succeed when relaxed |
| Freshness | Median age of evidence on results shown |
| Coverage | Productive hosts and objects in Atlas, per domain |

Each number is reported with its workload, egress and limitations.

---

## 15. Phases

The current codebase is a strong engine for one domain with no memory. The phases below move it to the system described above, in an order where each phase is useful on its own.

**Phase 1: Memory and generality.** Introduce Atlas as persistent storage. Generalise the search request from films to typed objects. Move existing matching into Lens. Make every search read from Atlas first and write every probe result back.

**Phase 2: The crawler.** Replace provider-by-provider search with a frontier, fetch tiers and host drivers. Convert existing provider code into the first drivers. Run discovery continuously, not only on demand.

**Phase 3: Domains.** Add audio, images and documents, each with its identity methods, Vault presentation and Probe verification. Add the fingerprint library.

**Phase 4: Vault upgrade.** Encoded video and audio streaming over WebRTC, microVM isolation tier, per-session egress with country selection, multi-session tabs and evidence capture.

**Phase 5: Shield depth.** Media-timeline and behavioural layers, per-host learned profiles, exposure measurement.

**Phase 6: Games.** In-sandbox emulation with controller input, then GPU-node streaming.

**Phase 7: Research tracks.** Driver synthesis, visual ad detection, speculative verification and the rest, in whatever order curiosity dictates.

---

## Appendix: mapping from the current codebase

| Current | Becomes |
|---|---|
| Scout package: provider search, availability checks, movie discovery | Scout drivers for those hosts, Lens matching rules, Probe verification methods |
| In-memory five-minute search cache | Atlas |
| Playback sampler with decoded-frame proof | Probe for the video domain; the template for every other domain's Level 3 check |
| Vault manager with frame streaming over data channels | Vault with encoded media tracks, isolation tiers and egress selection |
| Shield three layers | Shield L1 to L3, extended to L0 and L4 to L6 |
| Relay | Relay, extended to audio, images and documents |
| React dashboard | Console, extended to typed results, multi-session and evidence views |
| Tavily and Brave search integrations | Bootstrap seeds for the crawl frontier only |

The engineering culture of the existing code carries forward unchanged: bounded budgets on every check, evidence with method and timestamp, explicit unknown states, and no silent fallbacks.
