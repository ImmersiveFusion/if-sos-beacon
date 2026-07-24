# sos-beacon

A single-binary, topic-agnostic **signal harvester**. It watches public forums for the conversations you care about, has an AI sort each hit into buckets you define (`someone's asking for help` / `someone's furious at a vendor` / `just describing the pain`), scores it, and posts a short **pointer** into a Discord channel. A human sees the pointer, raises a hand to claim it, and replies **in their own words**.

The AI finds the conversation. A person has it. **The tool never writes a reply.**

No infrastructure, no Immersive Fusion dependency: clone it, bring your own keys, run it. One binary runs many beacons at once, each just a config block. `#sos-apm` watches observability pain; `#sos-ai` watches AI-hype discourse; you could point one at gas prices or potholes.

> **Status: Phase 0.** The skeleton runs end to end today: Hacker News source, any OpenAI-compatible model, Discord delivery, a JSON dedupe file, one-shot execution. Reddit + Lobsters sources, the boolean pre-filter, SQLite, digest batching, and the OTel demo grid land in later phases (see [Roadmap](#roadmap)). The architecture below is the whole platform; the checklist marks what's wired up now.

## Why this exists

The commercial "social listening" category races toward one thing: bots that talk to unaware humans at scale, monitoring fused with automated cold outreach. sos-beacon is the inverted, disclosed, human-replies-only design. It surfaces where a real person might genuinely help, and then gets out of the way. The contribution isn't the monitoring; plenty of tools monitor. It's intent-scored triage plus community dispatch plus a response ledger, with the anti-spam stance built into the type system instead of a policy page.

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
| **Sources** (`Fetcher`) | over-collect candidate posts from a platform | **hn**, **lobsters** ✅ · reddit, bluesky, mastodon 🔜 |
| **AI** (`Classifier`) | sort a post into the beacon's buckets, score its fit | **openai-compatible** ✅ (OpenAI, Azure OpenAI, Ollama, vLLM, OpenRouter, Groq) · anthropic 🔜 |
| **Delivery** (`Sink`) | post the pointer outward | **discord** ✅ · slack, email, webhook 🔜 |
| **Persistence** (`Store`) | dedupe + claim ledger + digest accumulator | **file (JSON)** ✅ · sqlite, azuresql 🔜 |

A **beacon** is one config block choosing a source set, a filter, a bucket rubric, and a destination. The engine is generic over all of it: buckets are data, not code, so the same binary serves `sos-apm`, `sos-ai`, or `sos-potholes`.

## Platform matrix

Which source platforms the beacon will and will not harvest, and why. Adding a source is [one file plus one registry line](docs/EXTENDING.md); the tiers below are about intent and ethics, not effort.

| Tier | Platforms | Status | Why |
|------|-----------|--------|-----|
| **T1: core** | Hacker News, Lobsters, Reddit | HN + Lobsters ship now; Reddit (OAuth) is next | Public, API-friendly, high signal. Reddit needs OAuth (unauthenticated access died in 2025), and being maintained through that is itself a feature. |
| **T2: community** | Bluesky, Mastodon, Stack Exchange, dev.to | Planned / community PRs | Free, mostly no-auth, good signal. Each is a real `Fetcher` plus one registry line. We do not ship inert stubs: a source is implemented or left out. |
| **T3: quarantined** | X / Twitter | Behind a BYO-paid-key flag, opt-in only | The only source with real per-call cost and terms-of-service friction. Never on by default; you bring your own key and turn it on deliberately. |
| **T4: refused** | LinkedIn; any server you do not administer; gray-market scraper APIs | Will not implement | These require pretending to be something you are not (fake accounts, scraping against terms, impersonation). The beacon does not do things that require pretending. This is a line, not a backlog. |

The same ethic runs through the [refused-features list](#what-it-refuses-to-do): the tool harvests where it can do so honestly and openly, and refuses where it cannot.

## Quick start

```bash
# install (or grab a release binary / the container image)
go install github.com/ImmersiveFusion/if-sos-beacon/cmd/sos-beacon@latest

# configure: copy the example, edit buckets/keywords/context
cp config.example.yaml config.yaml

# bring your own keys, nothing secret lives in the config file
export OAI_KEY=...           # your LLM API key
export OAI_BASE_URL=...      # e.g. https://api.openai.com/v1
export SOS_APM_WEBHOOK=...   # a Discord channel webhook URL

# one pass: fetch, classify, post pointers, exit
sos-beacon -config config.yaml
```

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

## Channel rules

Running a beacon channel is a responsibility. The humans who respond follow seven rules: one responder per thread, reply as yourself (never AI-drafted), never cross-vote, disclose affiliation, help without selling, skip vendor-hostile threads, and link the repo if accused of shilling. Read them before you run a channel: [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Watch the beacon run, live, in 3D

The flagship beacons are OpenTelemetry-instrumented and stream into [IAPM](https://immersivefusion.com)'s 3D player: a **real** production workload on public display, deployed the same way as its sibling [tracegen](https://github.com/ImmersiveFusion/if-opentelemetry-tracegen)'s demo grids (distroless, multi-arch, GitOps via Argo CD). When a source fetcher dies mid-run, you can watch the topology notice. *(Demo grid lands in Phase 2; see the roadmap.)*

**[Where does sos-beacon run?](WHERE-SOS-BEACON-RUNS.md)** is a community board of deployments. Add yours.

## Roadmap

- **Phase 0 (now):** core + four ports; HN source; openai-compatible AI; Discord delivery; file store; one-shot run. Real pointers land in a real channel.
- **Phase 1:** Reddit OAuth + Lobsters sources; boolean `AND/OR/NOT` pre-filter; SQLite store; digest batching + thresholds; `anthropic` adapter; the flagship `#sos-apm` end to end.
- **Phase 2:** OTel instrumentation (with a secret-scrub processor + span-attribute allowlist); the demo grid; Azure SQL store + container run-loop; GitHub Actions cron.
- **Phase 3:** Tier-2 sources as community PRs (Bluesky first); weekly-themes digest; watch-mode wording; the reaction-reading claim bot.

## Related tools

Part of Immersive Fusion's single-binary / zero-infra OSS family:

- **[tracegen](https://github.com/ImmersiveFusion/if-opentelemetry-tracegen)**: a topology-rich OpenTelemetry trace generator; the deploy-shape and release template this repo matches file-for-file.
- **[OpenTelemetry Chaos Simulator](https://github.com/ImmersiveFusion/if-opentelemetry-chaos-simulator-sample)**: interactive chaos engineering sandbox, [visualized in 3D](https://demo.iapm.app).

## Building from source

```bash
git clone https://github.com/ImmersiveFusion/if-sos-beacon.git
cd if-sos-beacon
go build -o sos-beacon ./cmd/sos-beacon
```

The binary is CGO-free (`CGO_ENABLED=0`) and ships on a distroless base. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for cross-compilation and the adapter-authoring guide.

## License

Apache License 2.0. See [LICENSE](LICENSE).

Copyright 2026 [ImmersiveFusion](https://immersivefusion.com)
