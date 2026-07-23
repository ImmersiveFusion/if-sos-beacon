# Security Policy

## Reporting a vulnerability

Please report security issues privately, not in a public issue.

- **Preferred:** open a private report via GitHub's
  [Security advisories](https://github.com/ImmersiveFusion/if-sos-beacon/security/advisories/new)
  ("Report a vulnerability").
- **Email:** security@immersivefusion.com.

Include what you found, how to reproduce it, and the impact you expect. We aim to
acknowledge within a few business days and will keep you updated as we work on a
fix. Please give us reasonable time to remediate before any public disclosure.

## Scope and handling notes

sos-beacon is a single static binary that talks to third-party HTTP APIs
(sources, an LLM endpoint, a delivery webhook) using operator-supplied
credentials. A few properties are load-bearing for security; regressions in
these are in scope:

- **Secrets stay out of logs.** API keys and webhook URLs are read from the
  environment and are never logged. A webhook URL embeds an auth token, so
  transport errors are redacted before they reach a log line.
- **Fetched content is untrusted.** Everything harvested from a public forum is
  JSON-encoded into outbound payloads; it is never concatenated into a request
  body, nor templated/evaluated as code.
- **The binary is CGO-free** (`CGO_ENABLED=0`) and ships on a distroless base as
  non-root.

Out of scope: vulnerabilities in third-party services the operator points the
beacon at, and issues that require an operator to supply a deliberately hostile
config or malicious environment.
