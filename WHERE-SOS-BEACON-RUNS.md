# Where sos-beacon Runs

**One binary. One small container. It runs anywhere (a cron, a Pi, a cluster), and this is the board of where it actually does.**

sos-beacon ships as a single static binary and a distroless, multi-arch image (`linux/amd64` + `linux/arm64`), no infrastructure required:

```bash
# one pass: fetch, classify, post pointers, exit
sos-beacon -config config.yaml
```

That portability is the point: the same build runs one-shot from a GitHub Actions cron, always-on in a container, or on a fanless box in someone's office. Below is where it runs for real. Running a beacon somewhere? Add yours. See [Add your deployment](#add-your-deployment).

---

## The deployments

### Immersive Fusion: the flagship beacons

- **Where it runs:** the **Immersive Fusion cloud**, alongside the [tracegen](https://github.com/ImmersiveFusion/opentelemetry-tracegen) demo grids, deployed declaratively via GitOps (Argo CD).
- **What for:** the flagship channels. `#sos-apm` watches observability pain (alert fatigue, dashboard sprawl, per-host pricing rage); each channel is a single config block watching public forums and posting pointers for a human to claim.
- **Flavor:** the distroless container (`immersivefusion/sos-beacon`, pinned by digest in GitOps), reading its AI port through the same public `openai-compatible` adapter any forker uses (IF just points it at Azure OpenAI; there is no IF-only backend).
- **The point:** IF runs its own OSS tool with zero private dependencies. If a source adapter dies mid-run, that failure is on public display, which is exactly the kind of honesty this project is built on.

---

## Add your deployment

Running a beacon somewhere (a homelab cron, a community Discord, a niche subreddit watch, a civic-issue tracker)? **List it too.** This board is earned, not bought: the only entry fee is that you actually run it.

Open a pull request at [github.com/ImmersiveFusion/sos-beacon](https://github.com/ImmersiveFusion/sos-beacon) adding a block under [The deployments](#the-deployments) using this template:

```markdown
### <Your name or org>: <one-line what>

- **Where it runs:** <platform + architecture, e.g. "a GitHub Actions cron" or "a Raspberry Pi 4">
- **What for:** <the beacon: what topic it watches and where it posts>
- **Flavor:** <container or binary; the tag you run, e.g. immersivefusion/sos-beacon:0.1.0; one-shot or always-on>
- **Link:** <optional but encouraged: a public channel, a blog post, or a repo; it's the best proof>
```

Keep it factual and specific: the specifics are the merit. No marketing, no logos-for-sale; just where the beacon runs and what it watches. A maintainer will review and merge; entries that name a platform, a topic, and a version (or a link) move fastest.

Prefer not to write the PR yourself? [Open an issue](https://github.com/ImmersiveFusion/sos-beacon/issues/new) with the same details and we'll add it.

---

Contributions are accepted under the repository's [Apache-2.0 license](LICENSE).
