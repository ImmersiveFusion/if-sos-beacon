# Extending sos-beacon

sos-beacon is ports-and-adapters. There are exactly four ports, defined as
interfaces in [`internal/core/core.go`](../internal/core/core.go):

| Port | Interface | Package | Registry |
|------|-----------|---------|----------|
| Sources | `Fetcher` | `internal/sources` | `sources.Build` |
| AI | `Classifier` | `internal/ai` | `ai.Build` |
| Delivery | `Sink` | `internal/delivery` | `delivery.Build` |
| Persistence | `Store` | `internal/store` | `store.Build` |

Adding an adapter is always the same two steps: **write one file that implements
the interface, then add one line to that package's `registry.go`.** No `init()`,
no central switch, no change to `main.go`. The registry map is the whole plugin
seam and it is greppable in one place.

Three rules hold for every adapter:

1. **Accept interfaces, return concrete structs.** Your constructor returns
   `*MyThing`; the registry entry adapts it to the `core` interface.
2. **Surface, never draft.** The anti-slop invariant is structural: a `Verdict`
   flows to a `Sink` and stops. Do not add a path that turns model output into a
   message a human posts. Such a PR fails the acceptance test in
   [`CONTRIBUTING.md`](../CONTRIBUTING.md).
3. **Stay dependency-free (stdlib) where you can**, keep `CGO_ENABLED=0`, give
   every `*http.Client` a timeout, take `context.Context` first, and never log a
   secret.

---

## Example 1: a new harvester (Sources port)

> **On the import paths in these examples.** They read
> `github.com/ImmersiveFusion/if-sos-beacon/...` while the repository is named `sos-beacon`. That is
> the module path declared in `go.mod`, it is a public contract, and it is deliberately not being
> renamed alongside the repository. Copy the imports verbatim and please do not "fix" them to match
> the repository name.

Say you want a `devto` source. Create `internal/sources/devto.go`:

```go
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ImmersiveFusion/if-sos-beacon/internal/core"
)

type DevTo struct {
	tags     []string
	endpoint string
	client   *http.Client
}

func NewDevTo(tags []string) *DevTo {
	return &DevTo{
		tags:     tags,
		endpoint: "https://dev.to/api/articles",
		client:   &http.Client{Timeout: 20 * time.Second},
	}
}

func (d *DevTo) Name() string { return "devto" }

func (d *DevTo) Fetch(ctx context.Context) ([]core.Signal, error) {
	// GET per tag, map each article to core.Signal, dedupe by id. Set a
	// User-Agent, check the status code, JSON-decode the body. Return signals.
	return nil, fmt.Errorf("devto: not implemented in this example")
}

var _ = json.Marshal // placeholder so the example compiles in isolation
```

Then register it in [`internal/sources/registry.go`](../internal/sources/registry.go):

```go
var registry = map[string]Constructor{
	"hn": func(b config.BeaconConfig) (core.Fetcher, error) {
		return NewHN(b.Keywords), nil
	},
	"lobsters": func(b config.BeaconConfig) (core.Fetcher, error) {
		return NewLobsters(b.Lobsters.Tags), nil
	},
	"devto": func(b config.BeaconConfig) (core.Fetcher, error) { // <- one entry
		return NewDevTo(b.DevTo.Tags), nil
	},
}
```

A constructor returns an error so a source with prerequisites can refuse at
wiring time instead of failing identically on every poll. Reddit does this: no
subreddits, or empty credentials in the environment, and the beacon logs one
actionable "skipping source" line naming the env var to set. If your source has
no prerequisites, return `nil`.

If your source needs its own config (tags, instances, subreddits), add a typed
block to `config.BeaconConfig`, following the existing `reddit:` and `lobsters:`
fields. A beacon opts in by listing the name in its `sources:` list.

Two details worth copying from the existing sources:

- **Implement `core.KeywordPrefilter`** (returning true) if your API narrows by
  keyword server-side, like HN and Reddit. The engine then skips the client-side
  keyword gate for your signals, so a hit that matched in the body rather than
  the title is not thrown away.
- **Populate `Signal.Tags`** with whatever the platform uses for topic taxonomy
  (Lobsters tags, a subreddit, dev.to tags). Tags feed both the pre-filter and
  the classifier prompt, and on link-heavy platforms they carry more topical
  signal than the post text does.

---

## Example 2: a new classifier (AI port)

Say you want an `anthropic` backend. Create `internal/ai/anthropic.go` with a
constructor returning `*Anthropic` that implements `Classify`. Reuse the shared,
model-neutral prompt so the rubric is identical across backends:

```go
func (a *Anthropic) Classify(ctx context.Context, s core.Signal, bc core.BeaconContext) (core.Verdict, error) {
	system := prompts.BuildSystem(bc)          // same prompt every adapter uses
	// POST to the Messages API with system + a compact JSON user payload,
	// then json.Unmarshal the model's text into a core.Verdict.
	_ = system
	return core.Verdict{}, nil
}
```

Register it in [`internal/ai/registry.go`](../internal/ai/registry.go):

```go
var registry = map[string]Constructor{
	"openai-compatible": func(cfg config.AIConfig) (core.Classifier, error) { /* ... */ },
	"anthropic":         func(cfg config.AIConfig) (core.Classifier, error) { // <- one line
		return NewAnthropic(os.Getenv(cfg.BaseURLEnv), cfg.Model, os.Getenv(cfg.KeyEnv)), nil
	},
}
```

A beacon (or the global `ai:` block) selects it with `provider: anthropic`.
Secrets are resolved from the environment inside the registry entry; the config
only ever names the env var.

---

## Example 3: a new destination (Delivery port)

Say you want a generic `slack` sink. Create `internal/delivery/slack.go` with a
constructor returning `*Slack` that implements `Deliver`. Build the payload with
`encoding/json` (never string-concatenate fetched content into the body), and
redact the webhook URL from any transport error so a failed post cannot leak the
token into logs (see `redactURL` in [`discord.go`](../internal/delivery/discord.go)):

```go
func (s *Slack) Deliver(ctx context.Context, f core.Finding) error {
	body, err := json.Marshal(slackPayload{Text: buildText(f)}) // pointer text only
	if err != nil {
		return err
	}
	// POST body to the webhook; on transport error, return redactURL(err).
	_ = body
	return nil
}
```

Register it in [`internal/delivery/registry.go`](../internal/delivery/registry.go):

```go
var registry = map[string]Constructor{
	"discord": func(dest config.Destination) (core.Sink, error) { /* ... */ },
	"slack":   func(dest config.Destination) (core.Sink, error) { // <- one line
		webhook := os.Getenv(dest.WebhookEnv)
		if webhook == "" {
			return nil, fmt.Errorf("%w: %s", ErrMissingWebhook, dest.WebhookEnv)
		}
		return NewSlack(webhook), nil
	},
}
```

A beacon selects it with `destination: { type: slack, webhook_env: SOS_X_WEBHOOK }`.

---

## Persistence (Store port)

The Store is split into narrow roles so consumers depend only on what they use:
`Deduper` (novelty), `Recorder` (persist a finding), `Ledger` (claims + digest),
and `io.Closer`. The engine takes only `Deduper` + `Recorder`. A new backend
(for example `sqlite`) implements the whole `Store` and registers one line in
[`internal/store/registry.go`](../internal/store/registry.go). Keep it CGO-free:
use the pure-Go `modernc.org/sqlite`, never the cgo `mattn/go-sqlite3`, so the
static-binary / distroless build stays intact.

## Before you open the PR

- `make test` (or `go test -race -cover ./...`) is green.
- `make lint` (`go vet` + `golangci-lint run`) is clean, and `gofumpt -l .`
  prints nothing.
- No em dashes or arrow glyphs anywhere (R-LOCAL-005); CI fails closed on them.
- Your adapter surfaces a pointer. It does not draft prose for a human to post.
