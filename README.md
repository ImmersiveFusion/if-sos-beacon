![A beacon on a vast dark plain of scattered signals, three burning magenta where someone is asking for help](.img/banner.jpg)

# sos-beacon

A single-binary, topic-agnostic **signal harvester**. It watches public forums for the conversations you care about, has an AI sort each hit into buckets you define (`someone's asking for help` / `someone's furious at a vendor` / `just describing the pain`), scores it, and posts a short **pointer** into a Discord channel. A human sees the pointer, raises a hand to claim it, and replies **in their own words**.

The AI finds the conversation. A person has it. **The tool never writes a reply.**

No infrastructure, no Immersive Fusion dependency: clone it, bring your own keys, run it. One binary runs many beacons at once, each just a config block. `#sos-apm` watches observability pain; `#sos-ai` belongs to the academies and is pointed at the AI reckoning itself, [described in the academy's own words below](#sos-ai-the-academy-beacon); you could point one at gas prices or potholes.

> **Status: in use.** Running end to end today: **Hacker News and Lobsters** sources, any OpenAI-compatible model, Discord delivery, JSON or Azure SQL persistence, one-shot or container-loop execution, and OTel tracing to a live grid. Two beacons are deployed, `#sos-apm` and `#sos-ai`. **The Reddit adapter is complete but ships disabled and we do not run it**, which is a decision rather than a gap: see [The Reddit source ships disabled](#the-reddit-source-ships-disabled-and-we-do-not-run-it). The boolean pre-filter, SQLite, digest batching and the Tier-2 sources are still ahead (see [Roadmap](#roadmap)). The architecture below is the whole platform; the checklist marks what's wired up now.

## What SOS means

**SOS is the Spatial Observability Signal. It is also Save Our Souls.** Both meanings are load-bearing, and it acquired them in that order.

It began as the first. [Spatial observability](https://spatialobservability.org) argues that flattening a running system into dashboards has a cost: engineers with forty tabs open, alert fatigue, incidents where most of the time goes to working out which thing actually broke. A manifesto can assert that. A beacon can show it, live and in public, by catching it happening in real people's own words every hour. The first job was evidence.

Then the name doubled, and the second meaning turned out to be the larger one. The signal was never only a market signal; it was a distress call. Distress is not a niche, so the tool generalised. It is topic-agnostic by design, and observability is simply the first thing we pointed it at.

### A public good in a graveyard

The tooling landscape is full of dead things. Scrapers that broke when a platform changed its auth and were never fixed. Repos whose last commit was three years ago. Tools that solved a real problem for someone, once. A maintained, Apache-2.0, runs-without-us tool is itself the argument, and keeping it alive is most of the work. Being maintained is the statement.

### Running one is a responsibility, not a right

A tool that finds people in difficulty at scale is power, and that power carries a duty. Answer as a human, in your own words, disclosed as yourself, or do not run it. Anyone can run their own beacon and we would rather they did. Running it honestly is the price of admission, and nothing in the licence enforces it.

To be clear about the size of the promise: it is small. A person notices and shows up and means it. That is all it is, and it is worth doing.

## Why this exists

The commercial "social listening" category races toward one thing: bots that talk to unaware humans at scale, monitoring fused with automated cold outreach. sos-beacon is the inverted, disclosed, human-replies-only design. It surfaces where a real person might genuinely help, and then gets out of the way. The contribution isn't the monitoring; plenty of tools monitor. It's intent-scored triage plus community dispatch plus a response ledger, with the anti-spam stance built into the type system instead of a policy page.

That is one idea applied to outreach: **the machine's job is reach, the human's job is the answer.** AI is good at hearing a thousand threads at once. It is not good at showing up and meaning it, and that is not a gap that closes with a better model. So the AI finds the cry and a person owns the reply. **Surface is the only verb**, and it is enforced by the type system rather than promised in a policy page: there is no code path from a classification to written prose. Answering a Save Our Souls with a bot is the precise hollow thing this refuses to build.

## How it works

sos-beacon is **ports-and-adapters**. The core pipeline (fetch -> pre-filter -> classify -> decide -> deliver -> record) is pure and knows nothing about Hacker News, Discord, or "APM." Everything external is a swappable adapter behind one of four ports:

```text
                 +-------------------- core domain (pure) --------------------+
  Sources port   |  fetch -> pre-filter -> classify -> decide -> deliver ...  |  Delivery port
  (Fetcher)  --> |                                                           | --> (Sink)
                 |                     |                 |                     |
                 +---------------------|-----------------|---------------------+
                                  AI port            Persistence port
                                (Classifier)            (Store)
```

| Port | What it does | Adapters |
|---|---|---|
| **Sources** (`Fetcher`) | over-collect candidate posts from a platform | **hn**, **lobsters**, **reddit** ✅ · bluesky, mastodon, stackexchange 🔜 |
| **AI** (`Classifier`) | sort a post into the beacon's buckets, score its fit | **openai-compatible** ✅ (OpenAI, Azure OpenAI, Ollama, vLLM, OpenRouter, Groq) · anthropic 🔜 |
| **Delivery** (`Sink`) | post the pointer outward | **discord** ✅ · slack, email, webhook 🔜 |
| **Persistence** (`Store`) | dedupe + claim ledger + digest accumulator | **file (JSON)** ✅ · sqlite, azuresql 🔜 |

A **beacon** is one config block choosing a source set, a filter, a bucket rubric, and a destination. The engine is generic over all of it: buckets are data, not code, so the same binary serves `sos-apm`, `sos-ai`, or `sos-potholes`.

## Platform matrix

Which source platforms the beacon will and will not harvest, and why. Adding a source is [one file plus one registry line](docs/EXTENDING.md); the tiers below are about intent and ethics, not effort.

| Tier | Platforms | Status | Why |
|------|-----------|--------|-----|
| **T1: core** | Hacker News, Lobsters, Reddit | HN + Lobsters run by default; **Reddit ships OFF, see below** | Public, API-friendly, high signal. Reddit needs OAuth (unauthenticated access died in 2025), and being maintained through that is itself a feature. |
| **T2: community** | Bluesky, Mastodon, Stack Exchange, dev.to | Planned / community PRs | Free, mostly no-auth, good signal. Each is a real `Fetcher` plus one registry line. We do not ship inert stubs: a source is implemented or left out. |
| **T3: quarantined** | X / Twitter | Behind a BYO-paid-key flag, opt-in only | The only source with real per-call cost and terms-of-service friction. Never on by default; you bring your own key and turn it on deliberately. |
| **T4: refused** | LinkedIn; any server you do not administer; gray-market scraper APIs | Will not implement | These require pretending to be something you are not (fake accounts, scraping against terms, impersonation). The beacon does not do things that require pretending. This is a line, not a backlog. |

The same ethic runs through the [refused-features list](#what-it-refuses-to-do): the tool harvests where it can do so honestly and openly, and refuses where it cannot.

### The Reddit source ships disabled, and we do not run it

`internal/sources/reddit.go` is a real, complete adapter: app-only OAuth, no keys committed, inert unless a beacon lists `reddit` in its `sources` AND supplies credentials. **Immersive Fusion does not operate it**, and you should not either until you have checked that you may.

Reddit's Developer Terms S4.1 prohibits Data API access "by or on behalf of a business" and sends commercial users to a separate agreement with Reddit. The Data API Terms draw a narrower line (S3.1 / S3.2, keyed to commercial purpose and revenue rather than business status), and there is a real argument about which governs. That is a question for a lawyer, not a README. We are also not going to route around it with a personal account: Data API Terms S4.2 treats that as masking who is accessing and why, and it would gut the transparency our [code of conduct](CODE_OF_CONDUCT.md) is built on. So we left the source off and shipped the code anyway, because the one platform we cannot use should still be available to everyone who can.

If you enable it, these obligations are yours:

- **Register one `web app`, not a `script` app.** "Script" means personal use; using it from an organization misrepresents your OAuth identity (Data API Terms S2.8). One app covering all your beacons, not one per beacon (Developer Terms S4.2).
- **Attribution is mandatory, not a nicety.** Developer Terms S5.2: link back to the thread, cite the author's username, and make clear the content came from Reddit. The Discord sink does all three for Reddit-sourced findings.
- **Deletion on revocation.** Data API Terms S6 lets Reddit revoke access at any time without notice, and S3.2 / S6 then require deleting stored User Content and anything derived from it. Budget for purging your store, not just for switching the source off.
- **A privacy policy** is required in the app registration (Data API Terms S2.6, Developer Terms S7.2).

None of this touches the rest. Hacker News, Lobsters and the Tier-2 platforms carry no equivalent restriction.

## Quick start

```bash
# install (or grab a release binary / the container image)
go install github.com/ImmersiveFusion/sos-beacon/cmd/sos-beacon@latest

# configure: copy the example, edit buckets/keywords/context
cp config.example.yaml config.yaml

# bring your own keys, nothing secret lives in the config file
export OAI_KEY=...           # your LLM API key
export OAI_BASE_URL=...      # e.g. https://api.openai.com/v1
export SOS_APM_WEBHOOK=...   # a Discord channel webhook URL

# one pass: fetch, classify, post pointers, exit
sos-beacon -config config.yaml
```

> **Module path changed.** Up to and including `v0.2.0` this module was
> `github.com/ImmersiveFusion/if-sos-beacon`. It is now
> `github.com/ImmersiveFusion/sos-beacon`, matching the repository name. Existing builds pinned to
> `v0.2.0` or earlier keep resolving under the old path and are unaffected. To pick up any later
> release, update your import paths and your `go.mod` require line to the new path.

Or in a container (mount the config and the state file; the state file must persist between runs):

```bash
docker run --rm \
  -e OAI_KEY -e OAI_BASE_URL -e SOS_APM_WEBHOOK \
  -v "$PWD/config.yaml:/config.yaml:ro" \
  -v "$PWD/sos-state.json:/sos-state.json" \
  immersivefusion/sos-beacon -config /config.yaml
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `config.yaml` | path to the beacon config file |
| `-log-level` | `info` | `debug`, `info`, `warn`, `error` (or set `SOS_BEACON_LOG_LEVEL`) |
| `-interval` | `0` | container loop mode: poll every interval (e.g. `10m`); `0` is one-shot. A per-beacon `poll_interval` overrides it. |

### Run modes

Without `-interval` (and with no per-beacon `poll_interval`), the beacon makes **one pass over every beacon and exits**: the zero-infra path for a cron or a GitHub Actions schedule. Set `-interval 10m` (or a per-beacon `poll_interval`) to run as a **long-lived container**: each beacon polls on its own interval, sources are fetched sequentially per tick, and `SIGTERM` (or Ctrl-C) drains the in-flight poll and shuts down cleanly. The published image defaults to container use.

### Tracing

The beacon is OpenTelemetry-instrumented (`beacon.poll` -> `source.fetch` -> `signal.classify` -> `finding.deliver`), and the classify span carries OTel GenAI attributes including real token usage. Tracing is a no-op unless you point it at a collector via the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (and related `OTEL_EXPORTER_OTLP_*`) env vars. An allowlist filter enforces span hygiene: only structure, metadata, and the one-line reasoning summary are exported. Full prompt/completion content and every secret (webhook URLs, API keys) are scrubbed before anything leaves the process.

### One-shot state durability

In one-shot mode the dedupe state (`sos-state.json`) **must survive between runs** (committed, cached as a GitHub Actions artifact, or on a mounted volume) or the beacon re-posts everything and the channel dies. Container/always-on modes get this free from their volume. This is the sharpest operational gotcha; the state file is small and single-writer, so committing it is a fine tradeoff.

## Configuration

A beacon configures six dimensions; only the content changes per topic. See [`config.example.yaml`](config.example.yaml) for the full two-beacon example. The shape:

```yaml
ai:    { provider: openai-compatible, base_url_env: OAI_BASE_URL, model: gpt-5, key_env: OAI_KEY }
store: { type: file, path: ./sos-state.json }

beacons:
  - name: sos-apm
    sources: [hn]
    keywords: [datadog, "new relic", observability, APM]
    context: |
      Incumbents: Datadog, New Relic, Grafana. Pain: alert fatigue,
      dashboard sprawl, MTTR, per-host pricing.
    buckets:
      - { name: seeker,           emoji: "🙋", definition: "actively asking for a tool recommendation" }
      - { name: incumbent-rage,   emoji: "🔥", definition: "furious at a vendor's bill or behavior" }
      - { name: problem-adjacent, emoji: "🌫️", definition: "describes the pain without asking for tools" }
    destination: { type: discord, webhook_env: SOS_APM_WEBHOOK }
    thresholds: { realtime: 0.75, digest: 0.50 }
    max_age: 12h
```

Nothing in the engine knows what "APM" is. Swap the keywords, context, and buckets and you have a civic-issue tracker.

## What it refuses to do

sos-beacon **surfaces pointers**: a place to look, a score, a pick-list, a pattern-with-a-link. It does **not**, and by design *cannot*, produce prose for a human to post downstream. There is no code path from a classification to a written reply. The following are refused, on purpose, forever:

- **Reply drafting** or AI-generated/AI-drafted responses of any kind
- **Voice profiles** or tone-matching for replies
- **Email-finding** and contact enrichment
- **Outreach sequencing** or campaign automation
- **Auto-posting** to any platform (even paid incumbents concede this gets accounts banned)
- **LinkedIn**, and any platform you'd have to pretend to be a human on
- **Gray-market scraper APIs**

If a feature's output is a message a human is meant to send, it fails the acceptance test in [`CONTRIBUTING.md`](CONTRIBUTING.md) automatically. This is the whole point of the project, not a limitation of it.

## `#sos-ai` (the academy beacon)

One beacon in this family is not pointed at observability pain at all.

> #sos-ai is the beacon pointed at the AI reckoning itself, and it belongs to the academies, not the
> product.
>
> When someone is frightened of AI, afraid of losing their work to it, or just trying in good faith to
> make sense of what is happening, the beacon surfaces that moment and a person from the academy shows
> up. A person, in their own words, disclosed as themselves. It never pitches, never sells, and never
> writes the reply. Surface is the only verb.
>
> This is "We Will Be OK" running as a service: the same promise, kept one conversation at a time. It
> carries the same firewall as everything else the academies make. No one who reaches it is a lead.
> Their words are not harvested, not marketed, and not kept to sell anything. The help is
> unconditional, and it is free.
>
> It is not a crisis line and not a substitute for professional help, and it does not pretend to be. It
> is one human offering another the thing no tool can, to be heard by someone who means it. When
> someone needs more than that, the honest move is to point them to real help, not to hold them.

The words above are the canonical promise, maintained by the Immersive Fusion academies as their
source of record and reproduced here verbatim; they are not this project's to edit.

## Channel rules

Running a beacon channel is a responsibility. The humans who respond follow seven rules: one responder per thread, reply as yourself (never AI-drafted), never cross-vote, disclose affiliation, help without selling, skip vendor-hostile threads, and link the repo if accused of shilling. Read them before you run a channel: [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Organic telemetry, and watching it run

Being OpenTelemetry-instrumented has a second purpose, and it is genuinely the secondary one: the beacon is a real workload doing a real job, so it emits **real** telemetry as a byproduct.

That makes it the **organic** counterpart to its sibling [Snowglobe](https://github.com/ImmersiveFusion/snowglobe), which generates **synthetic** OTel out of nothing. Where Snowglobe invents a system to observe, sos-beacon is one. Both speak plain OTLP, so both feed anything: Jaeger, Tempo, Grafana, an OpenTelemetry Collector, or spatial tools such as DeepCube.

Its most instructive failure is its own. When a source fetcher dies mid-run, the topology notices before any alert does: the thing that was calling it is still calling, and nothing answers. A hole where a service used to be. The rescuer's own SOS, made visible.

The flagship beacons stream into [DeepCube](https://deepcube.ai)'s 3D player, deployed the same way as Snowglobe's demo grids (distroless, multi-arch, GitOps via Argo CD). The grids run live on Twitch at [twitch.tv/deepcubelive](https://www.twitch.tv/deepcubelive), no account and nothing to install. *(The beacon's own demo grid lands in Phase 2; see the roadmap.)*

**[Where does sos-beacon run?](WHERE-SOS-BEACON-RUNS.md)** is a community board of deployments. Add yours.

## Roadmap

**Shipped.** Core plus the four ports. Hacker News and Lobsters sources. Any OpenAI-compatible
model. Discord delivery. JSON file and Azure SQL stores. One-shot runs and the long-running
container loop (`-interval`). OpenTelemetry instrumentation with a secret-scrub processor and a
span-attribute allowlist. **Two beacons are deployed and streaming to live grids, `#sos-apm` and
`#sos-ai`.**

**Not planned: the Reddit source.** The adapter is complete and it ships **disabled**, and we do not
run it. That is a decision, not a backlog item, and the reasoning is in
[The Reddit source ships disabled](#the-reddit-source-ships-disabled-and-we-do-not-run-it). If you
are entitled to run it, that section says what you would owe.

**Next.** The boolean `AND/OR/NOT` pre-filter (#12). A SQLite store, for people who want persistence
without Azure. An `anthropic` adapter. A GitHub Actions cron, for anyone who would rather not run a
container.

**Later.** Tier-2 sources as community pull requests, Bluesky first. Weekly-themes digest.
Watch-mode wording. The reaction-reading claim bot.

## Related tools

A small family of single-binary, zero-infra OpenTelemetry tools, all Apache-2.0 and all usable without an Immersive Fusion account:

- **[Snowglobe](https://github.com/ImmersiveFusion/snowglobe)**: topology-rich **synthetic** OpenTelemetry from a single binary, the counterpart to this tool's organic output. Also the deploy-shape and release template this repo matches file-for-file.
- **[Shoebox](https://github.com/ImmersiveFusion/shoebox)**: paste a diagram of a system, break something in it, and fire one request through. A snowglobe is sealed; in a shoebox you can open it up. [Visualized in 3D](https://shoebox.deepcube.ai).

## Building from source

```bash
git clone https://github.com/ImmersiveFusion/sos-beacon.git
cd sos-beacon
go build -o sos-beacon ./cmd/sos-beacon
```

The binary is CGO-free (`CGO_ENABLED=0`) and ships on a distroless base. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for cross-compilation and the adapter-authoring guide.

## License

Apache License 2.0. See [LICENSE](LICENSE).

Copyright 2026 [ImmersiveFusion](https://immersivefusion.com)
