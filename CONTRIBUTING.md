# Contributing to sos-beacon

Thanks for your interest in sos-beacon, a single-binary, topic-agnostic signal harvester. Bug reports, feature ideas, new adapters, code, and docs are all welcome.

## The one rule that isn't negotiable: surface, don't draft

sos-beacon **surfaces pointers**: a place to look, a score, a pick-list, a pattern-with-a-link. It never drafts prose for a human to post. This is enforced structurally: a `Verdict` flows into the delivery embed and stops; there is deliberately no code path from a `Verdict` to a written reply.

**The PR acceptance test:** a change PASSES only if the system's output is a *pointer*. It FAILS automatically if the output is *prose intended for a human to use downstream*: a drafted reply, an outreach sequence, a config written for someone, a summary-for-posting. No case-by-case debate. The refused-features list in the [README](README.md) is the concrete form of this rule; PRs that cross it will be closed.

## Ways to contribute

- **Report a bug** or **request a feature**: open an issue with one of the [issue templates](.github/ISSUE_TEMPLATE).
- **Add a source / AI / delivery / store adapter**: the four ports (`Fetcher`, `Classifier`, `Sink`, `Store`) live in [`internal/core`](internal/core/core.go). Implement the interface in the matching package; the core never imports your adapter.
- **Add your deployment**: running a beacon somewhere? Add it to [`WHERE-SOS-BEACON-RUNS.md`](WHERE-SOS-BEACON-RUNS.md) via a pull request, or use the "Add a deployment" issue template and we'll add it for you.
- **Send a pull request**: see below.

## Building from source

sos-beacon is a single Go module. The only runtime dependency is a YAML parser.

```bash
git clone https://github.com/ImmersiveFusion/if-sos-beacon.git
cd if-sos-beacon
go build -o sos-beacon ./cmd/sos-beacon

# copy the example config, bring your own keys, run one pass
cp config.example.yaml config.yaml
export OAI_KEY=... OAI_BASE_URL=... SOS_APM_WEBHOOK=...
./sos-beacon -config config.yaml
```

Cross-compile for another platform:

```bash
GOOS=linux   GOARCH=arm64 go build -o sos-beacon     ./cmd/sos-beacon
GOOS=darwin  GOARCH=arm64 go build -o sos-beacon     ./cmd/sos-beacon
GOOS=windows GOARCH=amd64 go build -o sos-beacon.exe ./cmd/sos-beacon
```

Keep the binary **CGO-free** (`CGO_ENABLED=0`): the release build is a static binary on a distroless base. Any future SQLite store must use the pure-Go `modernc.org/sqlite` driver, never the cgo `mattn/go-sqlite3`.

## Pull requests

1. Fork the repo and branch from `main`.
2. Keep changes focused: one logical change per PR.
3. Run `go build ./...` and `go vet ./...` (and `gofmt -l .` should print nothing) before pushing.
4. Use clear, conventional commit messages (`feat:`, `fix:`, `docs:`, …).
5. Open the PR against `main` and describe what changed and why.

## Releasing

Releases are cut by pushing a version tag. The CI workflow
([`.github/workflows/release.yml`](.github/workflows/release.yml)) builds the
cross-platform binaries, publishes a GitHub Release, and pushes the multi-arch
container image to Docker Hub (`immersivefusion/sos-beacon`).

**Tag format: use the `v`-prefixed form, e.g. `v0.1.0`.** This is the canonical
scheme (it matches the Go ecosystem and what GoReleaser expects).

```bash
git tag v0.1.0
git push origin v0.1.0
```

The trigger also accepts the older bare-number form (`0.1.0`) for backward
compatibility, but new releases should always be `v`-prefixed so the
Tags/Releases lists stay consistent and sort cleanly.

## Reporting issues

Use the issue templates. For bugs, include your platform/architecture, the beacon config block (redact secrets, env var names are fine), the exact command, and what you expected versus what happened. `-log-level debug` gives more detail.

## Code of conduct

Two documents, two scopes:

- [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) is the **channel operating rules**: how humans behave in a beacon's Discord channel (one responder per thread, reply as yourself, disclose affiliation, and so on). Read it before you run a dispatch channel.
- For contributing here, follow the spirit of the [Contributor Covenant](https://www.contributor-covenant.org/): no harassment, assume good faith, keep it about the work.

## License

By contributing, you agree your contributions are licensed under the repository's [Apache-2.0 license](LICENSE).
