#!/usr/bin/env bash
# check_coverage.sh — fail if test coverage drops below the floor.
#
# ─── Two measurements, two floors ───────────────────────────────────────────
#
# Repository code in this project is exercised almost entirely under
# `-tags=integration` against a real PostgreSQL, because the properties it has
# to hold — CHECK constraints, partial unique indexes under genuine concurrency,
# migration up/down — cannot exist against a fake. Measuring coverage without
# that tag therefore reports a number that is not wrong so much as answering a
# different question, and it drifts further from the truth every time a slice
# adds persistence.
#
#   unit  — `go test ./...`                 no database needed. The fast local
#                                           signal, and the one CI's no-service
#                                           job enforces.
#   full  — `go test -tags=integration ./...` needs DB_URL. AUTHORITATIVE.
#
# The two are not merged, and that is the point. A build tag is ADDITIVE: with
# `-tags=integration` Go compiles the untagged test files as well as the tagged
# ones, so the full run already executes a strict superset of the unit run in a
# single pass. There is no second profile, so there is no merge step in which a
# package could be counted twice and inflate the result. The numbers are
# directly comparable because both runs use the same `-coverpkg` list (see
# "What counts toward the denominator" below) and thus the same denominator.
#
# The gap between them is meaningful on its own: it is the share of the codebase
# that only a database can reach. When this was introduced it measured
# 73.2% unit against 79.8% full — six points of repository code that a run
# without a database simply cannot see.
#
# ─── Why floors and not a ratchet ───────────────────────────────────────────
#
# A strict ratchet punishes the honest case where a PR adds a large,
# hard-to-test integration shim, and it makes the gate flaky at the margins. A
# floor set just under the current value blocks real erosion while leaving
# normal noise alone. Raise a floor deliberately, in its own commit, when
# coverage has genuinely moved up and stayed there.
#
# ─── CRITICAL: -count=1 is mandatory ────────────────────────────────────────
#
# Without it Go serves cached per-package results and the aggregate is wrong.
# During the 2026-07-26 audit a cached run reported 69.1% for a tree that
# actually measured 74.1% — a 5-point phantom regression. Never remove it.
#
# Usage:
#   scripts/check_coverage.sh                 # auto: full if DB_URL is set, else unit
#   COVERAGE_MODE=unit scripts/check_coverage.sh
#   COVERAGE_MODE=full scripts/check_coverage.sh
#   scripts/check_coverage.sh 80              # override the floor for this run
set -euo pipefail

# Floors of record. Two numbers because there are two measurements; neither is
# a relaxation of the other.
UNIT_FLOOR="${COVERAGE_FLOOR_UNIT:-73.0}"
FULL_FLOOR="${COVERAGE_FLOOR_FULL:-80.0}"

PROFILE="${COVERAGE_PROFILE:-coverage.out}"

# Mode: explicit, else inferred from whether a database is reachable at all.
# Inferring means a developer runs the fast gate by default and CI's
# database-backed job runs the authoritative one without extra configuration.
MODE="${COVERAGE_MODE:-}"
if [ -z "$MODE" ]; then
  if [ -n "${DB_URL:-}" ]; then MODE=full; else MODE=unit; fi
fi

case "$MODE" in
  unit)
    TAGS=""
    FLOOR="${1:-${COVERAGE_FLOOR:-$UNIT_FLOOR}}"
    LABEL="unit (no build tag)"
    ;;
  full)
    if [ -z "${DB_URL:-}" ]; then
      echo "✗ COVERAGE_MODE=full needs DB_URL — the integration suite skips without it,"
      echo "  which would report the unit number under the authoritative floor."
      exit 1
    fi
    TAGS="-tags=integration"
    FLOOR="${1:-${COVERAGE_FLOOR:-$FULL_FLOOR}}"
    LABEL="full (-tags=integration, authoritative)"
    ;;
  *)
    echo "✗ COVERAGE_MODE must be 'unit' or 'full', got '$MODE'"
    exit 1
    ;;
esac

# ─── What counts toward the denominator ─────────────────────────────────────
#
# Everything that SHIPS, and nothing else.
#
# `-coverpkg=./...` would also measure the test harnesses that happen to be
# written in Go. cmd/lwprobe is the external M2M consumer: it is exercised only
# by scripts/m2m-harness.sh against a live installation, by design — its entire
# value is that it reaches the product the way a stranger would, over HTTP. Unit
# coverage of it answers a question nobody asked, and its 350 uncovered lines
# would move the aggregate by six points, which is the difference between this
# gate measuring product test coverage and measuring how much harness was
# written this week.
#
# The equivalent harness in shell — scripts/two-realm-demo.sh — has never been
# in the denominator. Writing the next one in Go, for type safety and for the
# import guard that keeps it honest, should not be penalised by the gate.
#
# This is NOT a general escape hatch, and the list is deliberately explicit
# rather than a pattern: a package earns a place here by shipping to nobody.
# cmd/api and cmd/bootstrap DO ship and stay in the denominator at 0%, which is
# a real, visible gap rather than a hidden one.
HARNESS_PACKAGES='/cmd/lwprobe$'

COVERPKG="$(go list ./... | grep -Ev "$HARNESS_PACKAGES" | paste -sd, - | tr -d ' ')"
if [ -z "$COVERPKG" ]; then
  echo "✗ could not build the -coverpkg list"
  exit 1
fi

echo "  measuring ${LABEL} coverage (-count=1 disables the test cache)…"

# $TAGS is deliberately unquoted: it is either empty or a single
# whitespace-free flag, and an empty quoted "" would be passed to go test as a
# literal empty argument. Arrays would express this better but bash 3.2 (the
# system bash on macOS) errors on an empty array expansion under `set -u`.
# shellcheck disable=SC2086
if ! go test -count=1 $TAGS -coverprofile="$PROFILE" -coverpkg="$COVERPKG" ./... >/dev/null 2>&1; then
  echo "✗ tests failed — fix them before worrying about coverage"
  # shellcheck disable=SC2086
  go test -count=1 $TAGS ./... 2>&1 | grep -E '^(FAIL|---)' | head -20
  exit 1
fi

TOTAL=$(go tool cover -func="$PROFILE" | awk '/^total:/ {gsub(/%/,"",$NF); print $NF}')

if [ -z "$TOTAL" ]; then
  echo "✗ could not parse coverage total from $PROFILE"
  exit 1
fi

PASS=$(awk -v t="$TOTAL" -v f="$FLOOR" 'BEGIN { print (t >= f) ? 1 : 0 }')

if [ "$PASS" -ne 1 ]; then
  echo "✗ ${LABEL} coverage ${TOTAL}% is below the floor of ${FLOOR}%"
  echo
  echo "  Lowest-covered packages:"
  go tool cover -func="$PROFILE" \
    | awk '{print $1, $NF}' | grep -v '^total:' \
    | sed 's#github.com/JoaoGabrielVianna/lightweight-saas-backend/##' \
    | sort -t' ' -k2 -n | head -10 | sed 's/^/    /'
  echo
  if [ "$MODE" = unit ]; then
    echo "  Note: this run EXCLUDES the integration-tagged suite, which is where"
    echo "  most repository code is exercised. If the code you added is covered"
    echo "  only there, run the authoritative gate instead:"
    echo "      DB_URL=… make coverage-gate-full"
  fi
  echo "  Add tests, or justify the drop and lower the floor deliberately."
  echo "  See docs/QUALITY_GATE.md §Tests."
  exit 1
fi

echo "  + ${LABEL} coverage ${TOTAL}% (floor ${FLOOR}%)"
