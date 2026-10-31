# Agent notes

## Project rules

- Write the project name as `WhatsAgent`, with the same capitals as WhatsApp. The binary is `whatsagent`.
- Use the terms in the plan's "Terms" table, one name per concept, in code and docs.
- The plan is in `docs/2026-10-31-whatsagent-plan.md`. Update it when a design decision changes.
- Docs follow SimpleDoc. Run `npx -y @simpledoc/simpledoc check` after a docs change.
- WhatsAgent runs on Linux and macOS. Keep OS-specific behavior in `internal/paths`, in `internal/service` (systemd and launchd, chosen by a GOOS argument so both are tested on every OS), or behind build tags, and keep the cross-build in CI passing for `linux` and `darwin` on amd64 and arm64.
- The core knows nothing about whatsmeow, HTTP or a harness. WhatsApp access goes through `core.WhatsApp`, and each harness's pushes go through `core.Pusher`. Tests use the fakes in `internal/testkit` and the per-package stand-ins for `claude` and the Codex app-server.
- Use one library per concern, as listed in the plan's "Stack" table. Do not add a dependency that the standard library or an existing dependency already covers.
- Keep whatsmeow types inside the WhatsApp wrapper package. Keep interfaces next to the code that uses them, and keep package APIs small.
- Write WhatsApp code directly against whatsmeow. Do not copy code from other projects into WhatsAgent.
- Every Go package needs a test file. Go toolchains from 1.25 on build the `covdata` tool on demand for packages without tests, and toolchains in the module cache cannot do that, so coverage runs fail.

## Required checks

Run these before finishing a code change. If a parent folder has a `go.work` file, set `GOWORK=off` first.

```sh
gofmt -l .
go vet ./...
go test ./...
./scripts/check-coverage.sh
go build ./cmd/whatsagent
golangci-lint run
govulncheck ./...
slophammer-go dry .
slophammer-go crap .
slophammer-go check . --execute
```

Mutation testing is off by the owner's decision: do not add `slophammer-go mutate` or mutation targets.

`gofmt -l .` must print nothing. Coverage must stay at 85% or above, CRAP scores at 8 or below, and DRY findings at 0. `slophammer.yml` declares the dependency boundaries between packages; extend it when you add a package. The Slophammer standards are described in its [agent entrypoint](https://github.com/osolmaz/slophammer/blob/main/docs/AGENT_ENTRYPOINT.md).
