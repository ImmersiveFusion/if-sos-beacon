<!-- Canonical source for the Docker Hub Overview. Pasted into the Hub page by hand
     (the description API rejects PATs). When you change this, re-paste it on Docker Hub. -->
# sos-beacon

**One container that watches public forums for the conversations you care about, has an AI sort each hit into your buckets, scores it, and posts a short pointer into a Discord channel.** A human sees the pointer, raises a hand, and replies in their own words. The AI finds the conversation; a person has it. **The tool never writes a reply.**

The topic is not baked in: it's config. One beacon watches observability pain, another watches people wrestling with what AI means for them, another could watch gas prices or potholes. One binary runs many at once, each just a config block. Pure open source, zero Immersive Fusion dependency: bring your own keys and run it.

## Quick start

sos-beacon reads secrets from the environment and a small YAML config. Grab [`config.example.yaml`](https://github.com/ImmersiveFusion/sos-beacon/blob/main/config.example.yaml), edit it, then:

```bash
docker run --rm \
  -e OAI_KEY -e OAI_BASE_URL -e SOS_APM_WEBHOOK \
  -v "$PWD/config.yaml:/config.yaml:ro" \
  -v "$PWD/sos-state.json:/sos-state.json" \
  immersivefusion/sos-beacon -config /config.yaml
```

- `OAI_KEY` / `OAI_BASE_URL`: any OpenAI-compatible endpoint (OpenAI, Azure OpenAI, Ollama, vLLM, OpenRouter, Groq).
- `SOS_APM_WEBHOOK` (and `SOS_AI_WEBHOOK`, ...): a Discord channel webhook URL per beacon.
- Sources are pluggable per beacon; Hacker News and Lobsters ship in the box.

**One pass, or always on.** The command above runs a single pass and exits, the zero-infra path for a cron or a GitHub Actions schedule. Add `-interval 10m` to run it as a **long-lived container** that polls each beacon on its own schedule and shuts down cleanly on `SIGTERM`.

**Persistence.** The dedupe/claim ledger must survive between runs. Either mount the JSON file store shown above (`sos-state.json`), or point it at **Azure SQL** (`store: { type: azuresql, ... }`, or `SOS_STORE_DSN`) for a long-running or multi-instance deployment, no volume needed. Miss this in one-shot mode and the beacon re-posts everything.

The image is multi-arch (`linux/amd64`, `linux/arm64`), distroless, and runs as non-root.

## The one thing it will never do

sos-beacon **surfaces pointers**: a place to look, a score, a bucket, a link. It does not draft replies, write outreach, or auto-post. That's not a setting; there is no code path from the AI's verdict to a posted message. See the [refused-features list](https://github.com/ImmersiveFusion/sos-beacon#what-it-refuses-to-do).

## See it run, live, in 3D

The container is OpenTelemetry-instrumented, and every LLM classification is a span with full OTel GenAI semantic conventions (model, token usage, latency, the verdict). Immersive Fusion runs the flagship beacons as live demo grids that stream into [DeepCube (TM)](https://deepcube.ai)'s 3D player, so you can walk the real pipeline as it moves: sources polling, the model judging each post, embeds firing. Unlike a synthetic demo, this is a real workload on public display. When a source fetcher dies, you watch the phantom form in the topology.

## Tags

- `latest` follows the newest release; each release also publishes a matching semantic-version tag. Pin by digest for reproducible cluster runs.

## Source, issues, full docs

[github.com/ImmersiveFusion/sos-beacon](https://github.com/ImmersiveFusion/sos-beacon)

Product docs: [docs.deepcube.ai](https://docs.deepcube.ai)

Apache-2.0. Built by Immersive Fusion.
