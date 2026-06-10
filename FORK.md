# Fork notice

`trendvidia/protocompile` was originally forked from
[`bufbuild/protocompile`](https://github.com/bufbuild/protocompile)
on 2026-06-04. The repository's GitHub-level fork relationship has
been detached; it is maintained independently as part of the
trendvidia **protowire** ecosystem.

## Branch model

This repository uses a two-branch model:

| Branch | Purpose | Default? |
|---|---|---|
| **`trendvidia`** | All trendvidia-owned work, including the v1.2 schema-extension parser. Default branch. PRs target this branch. | ✓ |
| `main` | Mirrors upstream `bufbuild/protocompile:main`. Updated by fast-forward sync from upstream; no direct commits. | |

The `trendvidia` branch is the default for all development. The
`main` branch exists only to track upstream so cherry-picking and
rebasing can use the standard git plumbing against a known-clean
upstream reference.

This mirrors the pattern in
[`trendvidia/protobuf-go`](https://github.com/trendvidia/protobuf-go),
which uses the same two-branch arrangement.

## Why we forked

The fork extends the protobuf schema language with the constructs
specified in protowire's **RFC-001 — Protowire Schema Extensions**
(target spec version protowire v1.2.0):

- `type` declarations — refinement aliases over primitives, enums,
  wrappers, and messages
- `function` declarations — signatures for named validation
  predicates (`(bool, *Violation)` contract)
- `annotation` declarations — declarable metadata attachments
- `@annotation(...)` use-site syntax with hybrid placement
- Carrier extensions on every `*Options` message at numbers
  `50100`–`50104`

These additions are vendor-specific and not expected to land in
upstream `bufbuild/protocompile`, which serves the broader
protobuf community. The two-branch model expresses that cleanly:
`main` stays a faithful mirror; `trendvidia` carries the divergence.

References:
- [RFC-001](https://github.com/trendvidia/protowire/blob/main/docs/RFC-001-schema-extensions.md)
- [IETF draft `-01`](https://github.com/trendvidia/protowire/blob/main/docs/draft-trendvidia-protowire-01.md)
- [Umbrella tracker](https://github.com/trendvidia/protowire/issues/55)

## Fork point

| | |
|---|---|
| Forked from | `bufbuild/protocompile` |
| Fork date | 2026-06-04 |
| Fork-point commit (upstream) | `64e6ad0` — *Fix whitespace handling for synthetic tokens (#731)* |
| Tag (set at first divergence) | `fork-from-bufbuild` |

To inspect the fork point locally:

```bash
git fetch --tags
git show fork-from-bufbuild
```

## Local setup

Add `upstream` as a remote to your local clone:

```bash
git remote add upstream git@github.com:bufbuild/protocompile.git
git fetch upstream
```

## Maintenance policy

### Syncing `main` with upstream

The `main` branch is a one-way mirror of `bufbuild/protocompile:main`.
To pull a new upstream commit:

```bash
git fetch upstream main
git checkout main
git merge --ff-only upstream/main
git push origin main
```

This must be a fast-forward only — `main` should never carry a
trendvidia-specific commit. The ruleset that protects the default
branch (`trendvidia`) deliberately leaves `main` unprotected so this
mirror update can happen without PR ceremony.

### Cherry-picking upstream commits onto `trendvidia`

After updating `main`, decide which upstream commits to pick into
`trendvidia`:

```bash
# Single non-conflicting commit
git checkout -b cherry-pick-upstream-SHORTSHA trendvidia
git cherry-pick UPSTREAM_SHA
git push -u origin cherry-pick-upstream-SHORTSHA
gh pr create --base trendvidia

# Range
git checkout -b cherry-pick-upstream-batch-YYYYMMDD trendvidia
git cherry-pick UPSTREAM_FROM..UPSTREAM_TO
git push -u origin cherry-pick-upstream-batch-YYYYMMDD
gh pr create --base trendvidia
```

Cherry-picks go through the same PR + CI flow as any other change.

### Adding new work

All new work — bug fixes, features, schema-extension PRs — goes on
a feature branch off `trendvidia` and targets `trendvidia` in its PR:

```bash
git checkout -b feature-name trendvidia
# ... work ...
git push -u origin feature-name
gh pr create --base trendvidia
```

The default-branch ruleset enforces:
- block deletion / non-fast-forward / direct-push updates
- required linear history + signed commits
- pull-request flow with code-owner review + thread resolution
- required status checks: full CI matrix passes before merge
- allowed merge methods: squash, rebase

### Skipping the wrong upstream commits

Avoid cherry-picking from upstream:

- **Anything that touches `parser/proto.y` near the contextual-keyword
  productions** (`identifier`, `nonDeclIdent`, `*Name`) — conflicts
  with v1.2 reservations.
- **Anything in `options/` that overlaps with the annotation-carrier
  lowering pass** — once that pass lands.
- **Anything in `ast/` that overlaps with `TypeDeclNode`,
  `FunctionDeclNode`, `AnnotationDeclNode`, or `AnnotationNode`** —
  once those land.

When in doubt, open the PR with the upstream cherry-pick and let CI
+ review catch conflicts before merge.

## Other repos in the trendvidia ecosystem

- [`protowire`](https://github.com/trendvidia/protowire) — canonical
  spec; not a fork
- [`protowire-go`](https://github.com/trendvidia/protowire-go) —
  reference Go runtime; not a fork
- [`protobuf-go`](https://github.com/trendvidia/protobuf-go) —
  Go codegen + runtime; two-branch model (default `trendvidia`)
- [`protocheck`](https://github.com/trendvidia/protocheck) — Go
  validator engine; private
- [`protolsp`](https://github.com/trendvidia/protolsp) — LSP for
  `.proto` files; private
- [`pxfed`](https://github.com/trendvidia/pxfed) — Electron editor;
  private

`protocompile` and `protobuf-go` are the only repos that consume
upstream codebases; both use the same two-branch arrangement.
