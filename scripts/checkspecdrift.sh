#!/usr/bin/env bash
# Copyright 2020-2026 Buf Technologies, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Verify that the vendored copies of trendvidia/protowire's schema files are
# byte-identical to canonical from `syntax = "proto3";` down. Everything
# above that line is a protocompile-authored vendor header and may differ.
#
# v0.19.0 shipped stale report.pb.go bindings because nothing verified this
# (issue #116). The check that was added for it then never ran in CI and
# skipped into exit 0 whenever a sibling checkout was absent, which is the
# same failure one layer up (issue #198): a check that reports nothing wrong
# because it did not run is indistinguishable from one that passed.
#
# So this NEVER skips. With no local checkout it fetches canonical from
# GitHub at SPEC_REF, resolved to a commit SHA that is printed, so the basis
# of the comparison is always stated rather than implied.
#
#   SPEC_REF=main            ref to compare against when fetching
#   PROTOWIRE_DIR=../protowire  use a local checkout instead (offline, fast);
#                            its branch and commit are printed, because
#                            "whatever the sibling checkout happens to be on"
#                            was exactly the ambiguity #198 names
#
# On drift: re-vendor from canonical and run `make generate-protos`, or --
# when the vendored copy is the newer one -- upstream the change to
# protowire first.

set -euo pipefail

REPO="trendvidia/protowire"
SPEC_REF="${SPEC_REF:-main}"
PROTOWIRE_DIR="${PROTOWIRE_DIR:-}"

# vendored-in-this-repo : canonical-in-protowire
PAIRS=(
  "proto/protowire/schema/v1/report.proto:proto/schema/v1/report.proto"
  "proto/protowire/schema/v1/descriptor.proto:proto/schema/v1/descriptor.proto"
  "internal/proto/protowire/schema/config/v1/config.proto:proto/schema/config/v1/config.proto"
  "internal/proto/protowire/schema/catalog/v1/catalog.proto:proto/schema/catalog/v1/catalog.proto"
)

# body prints a schema file from the syntax line down, dropping the vendor
# header that is expected to differ.
body() { sed -n '/^syntax = "proto3";$/,$p' "$1"; }

tmp=""
cleanup() { [[ -n "$tmp" ]] && rm -rf "$tmp"; return 0; }
trap cleanup EXIT

if [[ -n "$PROTOWIRE_DIR" ]]; then
  if [[ ! -d "$PROTOWIRE_DIR/proto/schema" ]]; then
    echo "checkspecdrift: PROTOWIRE_DIR=$PROTOWIRE_DIR has no proto/schema" >&2
    exit 1
  fi
  sha="$(git -C "$PROTOWIRE_DIR" rev-parse --short HEAD 2>/dev/null || echo 'not-a-git-checkout')"
  branch="$(git -C "$PROTOWIRE_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')"
  dirty=""
  git -C "$PROTOWIRE_DIR" diff --quiet 2>/dev/null || dirty=" +uncommitted"
  basis="$PROTOWIRE_DIR ($branch @ $sha$dirty)"
  canonical() { cat "$PROTOWIRE_DIR/$1"; }
else
  if ! command -v curl >/dev/null; then
    echo "checkspecdrift: curl is required to fetch $REPO, and PROTOWIRE_DIR is unset" >&2
    exit 1
  fi
  # Accept: …github.sha returns the bare commit, so this needs no JSON parser
  # and pins the fetch below to one commit rather than to a moving ref.
  if ! sha="$(curl -fsSL -H 'Accept: application/vnd.github.sha' \
      "https://api.github.com/repos/$REPO/commits/$SPEC_REF")"; then
    echo "checkspecdrift: could not resolve $REPO@$SPEC_REF" >&2
    echo "  (offline? set PROTOWIRE_DIR=<path> to compare against a local checkout)" >&2
    exit 1
  fi
  tmp="$(mktemp -d)"
  basis="$REPO@$SPEC_REF ($sha)"
  canonical() {
    local dest="$tmp/${1//\//_}"
    if [[ ! -f "$dest" ]]; then
      curl -fsSL "https://raw.githubusercontent.com/$REPO/$sha/$1" -o "$dest" || {
        echo "checkspecdrift: could not fetch $1 from $REPO@$sha" >&2
        return 1
      }
    fi
    cat "$dest"
  }
fi

echo "checkspecdrift: comparing against $basis"

fail=0
for pair in "${PAIRS[@]}"; do
  vendored="${pair%%:*}"
  upstream="${pair##*:}"
  if [[ ! -f "$vendored" ]]; then
    echo "checkspecdrift: vendored file missing: $vendored" >&2
    fail=1
    continue
  fi
  if ! canonical "$upstream" > "${tmp:-/tmp}/canonical.$$" 2>/dev/null; then
    fail=1
    continue
  fi
  if ! diff -u <(body "$vendored") <(body "${tmp:-/tmp}/canonical.$$"); then
    echo "checkspecdrift: $vendored drifted from $upstream" >&2
    fail=1
  fi
done
rm -f "${tmp:-/tmp}/canonical.$$"

if [[ $fail -ne 0 ]]; then
  echo "checkspecdrift: FAILED" >&2
  exit 1
fi
echo "checkspecdrift: ${#PAIRS[@]} vendored files match $basis"
