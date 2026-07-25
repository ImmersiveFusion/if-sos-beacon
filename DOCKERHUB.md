<!-- Canonical source for the Docker Hub Overview. Pasted into the Hub page by hand
     (the description API rejects PATs). When you change this, re-paste it on Docker Hub. -->
# sos-beacon

**One container that watches public forums for the conversations you care about, has an AI sort each hit into your buckets, scores it, and posts a short pointer into a Discord channel.** A human sees the pointer, raises a hand, and replies in their own words. The AI finds the conversation; a person has it. **The tool never writes a reply.**

The topic is not baked in: it's config. One beacon watches observability pain, another watches AI-hype discourse, another could watch gas prices. One binary runs many at once, each just a config block. Pure open source, zero Immersive Fusion dependency: bring your own keys and run it.

## Quick start

sos-beacon reads secrets from the environment and a small YAML config. Grab [`config.example.yaml`](https://github.com/ImmersiveFusion/if-sos-beacon/blob/main/config.example.yaml), edit it, then:

```bash
docker run --rm \
  -e OAI_KEY -e OAI_BASE_URL -e SOS_APM_WEBHOOK \
  -v "$PWD/config.yaml:/config.yaml:ro" \
  -v "$PWD/sos-state.json:/sos-state.json" \
  immersivefusion/sos-beacon -config /config.yaml
```

- `OAI_KEY` / `OAI_BASE_URL`: any OpenAI-compatible endpoint (OpenAI, Azure OpenAI, Ollama, vLLM, OpenRouter, Groq).
- `SOS_APM_WEBHOOK`: a Discord channel webhook URL.
- The mounted `sos-state.json` is the dedupe ledger: it **must** persist between runs, or the beacon re-posts everything.

The image is multi-arch (`linux/amd64`, `linux/arm64`), distroless, and runs as non-root.

## The one thing it will never do

sos-beacon **surfaces pointers**: a place to look, a score, a bucket, a link. It does not draft replies, write outreach, or auto-post. That's not a setting; there is no code path from the AI's verdict to a posted message. See the [refused-features list](https://github.com/ImmersiveFusion/if-sos-beacon#what-it-refuses-to-do).

## Tags

- `latest` follows the newest release; each release also publishes a matching semantic-version tag. Pin by digest for reproducible cluster runs.

## Source, issues, full docs

[github.com/ImmersiveFusion/if-sos-beacon](https://github.com/ImmersiveFusion/if-sos-beacon)

Apache-2.0. Built by Immersive Fusion.
