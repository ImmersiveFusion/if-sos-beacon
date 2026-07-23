---
name: Bug report
about: Something in sos-beacon isn't working as expected
title: "[bug] "
labels: bug
---

**What happened**
A clear description of the bug.

**Config**
The relevant beacon config block (redact secrets, env var names are fine):

```yaml
beacons:
  - name: ...
```

**Command**
The exact `sos-beacon` invocation (flags, env vars):

```
sos-beacon -config config.yaml
```

**Expected vs. actual**
What you expected, and what actually happened.

**Environment**
- sos-beacon version / tag or digest:
- OS + architecture (e.g. `linux/arm64`):
- Run mode: binary or container (`immersivefusion/sos-beacon`):
- Source (hn / …), AI provider (openai-compatible / …), destination (discord / …):

**Logs / output**
Relevant output (run with `-log-level debug` for more detail; redact webhook URLs and keys).
