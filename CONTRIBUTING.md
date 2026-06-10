# Contributing to trendvidia/protocompile

Thanks for working on this fork. This document covers the few
conventions that differ from upstream `bufbuild/protocompile`. For
the bigger picture — branch model, sync workflow, fork point — see
[`FORK.md`](FORK.md).

## Local CI is the gate, cloud CI is the exception

This repo deliberately runs **most** CI gates **locally**, not in
GitHub-hosted runners. Cloud minutes add up fast on a fork with an
active development cadence, and most issues we hit (lint failures,
generated-code drift, test regressions) are catchable on a laptop in
under a minute.

### Before opening a PR

Run the local CI gate:

```bash
make ci-local
```

That target runs, in sequence:

1. `make generate` — regenerate everything (`y.go`, `.protoset`,
   `.pb.go`, license headers, etc.)
2. `make checkgenerate` — fail if the working tree is dirty after
   regeneration
3. `make lint` — `golangci-lint` against root and `internal/benchmarks`
4. `make test` — the full test suite (Go race detector on amd64;
   no race on 386)

If all four pass, open the PR. If not, fix and re-run.

For changes intended for a release (or any pre-release tag), also run:

```bash
make ci-local-full
```

which adds `make benchmarks` to confirm no perf regressions.

### When cloud CI fires automatically

The `ci` workflow (`.github/workflows/ci.yaml`) is restricted to four
event types:

- `release` (published) — gates every release end-to-end
- `push` to a tag matching `v*` — same protection at tag time
- `pull_request` **but only when** one of the following paths changes:
  - `.github/workflows/**`
  - `Makefile`
  - `.golangci.yml`
  - `go.mod`, `go.sum`, `go.work`, `go.work.sum`
  - `internal/benchmarks/go.mod` / `go.sum`
  - `internal/tools/go.mod` / `go.sum`
- `workflow_dispatch` — manual override (see below)

PRs that change only Go code, AST, parser grammar, or tests **do not**
trigger CI in the cloud. The trust contract is: you ran `make ci-local`
and it passed.

The `windows` workflow is even more restrictive — it fires only on
release events, tag pushes, and manual dispatch. Windows runners are
expensive, and platform-specific bugs in our Go code are rare; we
catch them at release time.

### Manual cloud CI when you want it

If you want a cloud sanity-check on a code-only PR (e.g., before a
risky merge, or to verify against a Go version you don't have
locally), trigger CI manually:

```bash
gh workflow run ci.yaml --ref <your-branch>
gh workflow run windows.yaml --ref <your-branch>
```

Then watch the run:

```bash
gh run watch
```

This consumes runner minutes only when you explicitly ask for them.

### Required status checks

The `trendvidia` branch ruleset does **not** require `test`, `lint`,
`benchmarks`, or `windows` checks to pass before merge. The ruleset
still enforces:

- block deletion / non-fast-forward / direct push
- required linear history
- required signed commits
- pull-request flow with code-owner review + thread resolution
- allowed merge methods: squash, rebase

Code-quality enforcement happens at the laptop level via
`make ci-local`. If you find yourself merging without running it,
add a pre-push hook:

```bash
cat > .git/hooks/pre-push <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
remote="$1"
url="$2"
while read local_ref local_sha remote_ref remote_sha; do
  case "$remote_ref" in
    refs/heads/trendvidia|refs/heads/main) ;; # don't gate direct pushes (blocked anyway)
    refs/heads/*)
      echo "Running ci-local before push to $remote_ref..."
      make ci-local
      ;;
  esac
done
EOF
chmod +x .git/hooks/pre-push
```

## PR title format

PR titles must follow this regex (enforced by the `Lint PR Title`
workflow):

```
^[A-Z][\w-]*[^s](?<!ing) .*[^.?!,\-;:]$
```

Plain English:

- Start with a capital letter
- First word is imperative present tense — not ending in `s` or `ing`
- No conventional-commits prefix (`feat:`, `parser:`, etc.)
- No trailing punctuation
- No contractions (`Don't`) as the first word

Examples:

| Good | Bad |
|---|---|
| `Add type declaration grammar and AST node` | `parser: add type declarations` |
| `Run CI workflows on the trendvidia branch` | `Runs CI on trendvidia.` |
| `Remove bufbuild project-automation workflow` | `removing bufbuild workflow` |

## Two-branch model

All PRs target `trendvidia` (default). `main` mirrors upstream
`bufbuild/protocompile` and is updated only via fast-forward pulls
from upstream. See [`FORK.md`](FORK.md) for the full policy,
cherry-pick workflow, and areas to avoid when pulling upstream
changes.

## Generated code

Always run `make generate` (not bare `goyacc`) for parser changes —
the make target applies the Apache 2.0 license-header step that
`make checkgenerate` enforces in CI. See `parser/proto.y` for the
grammar source of truth.

## Questions

Open a discussion on the umbrella tracking issue for the active
work:
[trendvidia/protowire#55](https://github.com/trendvidia/protowire/issues/55).
