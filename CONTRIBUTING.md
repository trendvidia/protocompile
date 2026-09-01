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

The `ci` workflow (`.github/workflows/ci.yaml`) runs on:

- **every pull request** — the full `test` and `lint` legs
- `release` (published) — gates every release end-to-end
- `push` to a tag matching `v*` — same protection at tag time
- `workflow_dispatch` — manual override (see below)

There is no `paths` filter. This repository parses `.proto`, lowers
annotations and emits the carriers the rest of the family reads, so a
change to the parser, the IR or a lowering pass must not be able to
merge without the suite having run against it.

Until #146 the `pull_request` trigger listed only manifests and build
config, so a PR that changed nothing but Go source ran no tests at all
and still showed a green tick — nothing distinguished "tests passed"
from "tests never ran". The suite is short; filtering it saved little
and cost the signal a reviewer uses to decide.

`make ci-local` is still the right thing to run before you push. It is
now a fast first opinion rather than the only one.

The two expensive legs stay off pull requests, each in its own workflow
whose trigger says so:

- `benchmarks` (`.github/workflows/benchmarks.yaml`) — release events,
  tag pushes, and manual dispatch. Benchmark numbers are only actionable
  against a release baseline.
- `windows` (`.github/workflows/windows.yaml`) — release events, tag
  pushes, and manual dispatch. Windows runners are expensive, and
  platform-specific bugs in our Go code are rare; we catch them at
  release time.

### Manual cloud CI when you want it

`ci` now runs on every pull request by itself. Dispatch a workflow by
hand when you want one that does not — the benchmark or Windows legs
before a risky merge or a release — or to re-run `ci` against a branch
without pushing to it:

```bash
gh workflow run ci.yaml --ref <your-branch>
gh workflow run benchmarks.yaml --ref <your-branch>
gh workflow run windows.yaml --ref <your-branch>
```

Then watch the run:

```bash
gh run watch
```

This consumes runner minutes only when you explicitly ask for them.

### Required status checks

The `trendvidia` branch ruleset requires **`test`** and **`lint`** — the
two `ci` jobs that run on every pull request — to pass before merge. A
red run now blocks the merge button rather than merely being visible.

`benchmarks` and `windows` are **not** required. They do not run on pull
requests at all (see above), so requiring them would block every merge.

The ruleset also enforces:

- block deletion / non-fast-forward / direct push
- required linear history
- required signed commits
- pull-request flow with code-owner review + thread resolution
- allowed merge methods: squash, rebase

**If the self-hosted runner is down**, `test` and `lint` never report and
no pull request can merge. The ruleset carries a bypass actor for exactly
this case; use it deliberately and say so in the PR, rather than
weakening the rule. Restarting the runner is the better fix when it is
available.

`make ci-local` is still worth running before you push — it is the same
gate, minutes sooner, and it keeps a red cloud run from being the first
time you learn something broke. If you find yourself pushing without
running it, add a pre-push hook:

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
