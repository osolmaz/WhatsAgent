#!/usr/bin/env bash
# Runs the tests with the race detector and fails when total coverage is below the minimum.
set -euo pipefail

minimum="85.0"
profile="coverage.out"

# Every package needs a test file: Go toolchains from 1.25 on build the covdata tool on demand
# for packages without tests, and toolchains in the module cache cannot do that.
go test -race -coverprofile="$profile" ./...

total="$(go tool cover -func="$profile" | awk '/^total:/ {print substr($3, 1, length($3)-1)}')"
printf 'Total coverage: %s%% (minimum %s%%)\n' "$total" "$minimum"
awk -v total="$total" -v minimum="$minimum" 'BEGIN { exit !(total + 0 >= minimum + 0) }'
