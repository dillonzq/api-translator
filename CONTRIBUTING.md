# Contributing

Thanks for helping improve `api-translator`. This library converts requests, non-streaming responses, and SSE streams between the Chat Completions, Responses, and Messages protocols. It is a pure Go standard-library package: it performs no HTTP calls, owns no provider policy, and never inspects model capabilities. Wire output is the product, so behavior changes are reviewed as protocol changes rather than as ordinary refactors.

By taking part you agree to keep discussion technical and respectful, and to license your contribution under the [MIT License](LICENSE).

## Ways to contribute

- **Bug reports** with a minimal, redacted JSON or SSE sample and the affected direction. These are the most valuable reports; see [the bug template](.github/ISSUE_TEMPLATE/bug_report.yml).
- **Conversion fixes** for fields that are dropped, mis-mapped, or re-encoded incorrectly.
- **New representable behavior** when a protocol gains a field with a real counterpart in another protocol. Open an issue first if the mapping is debatable.
- **Tests, snapshots, and documentation** for behavior that is already implemented, including the wrong-JSON-type cases that a buggy or hostile upstream can send.
- **Pipeline improvements** that make failures easier to diagnose.

## Out of scope

The following deliberately stay outside this repository. Changes that pull them in are usually rejected:

- HTTP transport, authentication, retries, cancellation, and timeouts.
- Provider-specific policy, model capability checks, and model-name validation.
- Lossy conveniences that silently invent data the upstream never sent.
- New protocols or formats. The three supported formats are the fixed surface; Gemini and a separate Codex format are not planned here.
- Third-party dependencies. CI checks the production and test dependency graphs for packages outside the standard library and this module (see [Design rules](#design-rules)).

## Requirements

| Tool | Version | Notes |
| --- | --- | --- |
| Go | 1.26.7 or newer | Pinned by `go.mod`; `actions/setup-go` installs the same version in CI. |
| golangci-lint | v2.14.0 | Only needed for the `lint` job. |
| git | any recent | — |
| GNU Make and Bash | any recent | Shared local/CI check commands. On Windows, use WSL for `make`; native Go commands also work. |

```sh
git clone https://github.com/dillonzq/api-translator.git
cd api-translator
make check
```

Install the pinned linter with the Go version from `go.mod` or newer. Package managers may provide a different version; check `golangci-lint version` before comparing results with CI:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
```

## Run the same checks as CI

[The CI workflow](.github/workflows/ci.yml) runs on pull requests, pushes to `main`, manual dispatch, and a weekly schedule. The release workflow calls the same checks after validating the tag. [The Makefile](Makefile) is the shared entry point for local and Linux CI checks; `make help` lists common commands.

```sh
make check # Fast pre-push checks, including race tests; no extra tools needed
make ci    # Full Linux CI checks; requires the pinned golangci-lint
```

Both Make and CI export `GOFLAGS=-mod=readonly`, so builds cannot silently add missing requirements, and `GOWORK=off`, so local workspaces cannot change the module under review. Coverage is informational and has no threshold. CI caches Go downloads and build results using `go.mod` as the dependency key because this module has no `go.sum`. The pinned actionlint and govulncheck versions live in the Makefile; their tool dependencies are downloaded outside this module and do not change `go.mod`.

| CI job | Local check |
| --- | --- |
| `lint` | `make fmt-check vet tidy-check deps lint workflow-lint` |
| `test` | `make build coverage` |
| `fuzz smoke` | `make fuzz` (override duration with `FUZZTIME=2m`) |
| `vulnerability scan` | `make vuln` (uses the live vulnerability database) |
| `test (macos-latest)` / `test (windows-latest)` | `bash scripts/check-dependencies.sh`, `go build ./...`, and `go test -shuffle=on -count=1 ./...` on that OS |
| `CI passed` | Aggregate gate: every validation job, including both OS tests, must succeed |

Linux runs race tests and coverage; macOS and Windows check their native production/test dependency graphs, builds, and tests using the same Go version from `go.mod`. GNU Make and Bash are only required for the shared command targets. On Windows without WSL, use the equivalent Go commands above for native tests, `go vet ./...`, and `go mod tidy -diff`; full `make ci` requires an environment that supports Go's race detector and a C compiler.

### Diagnose a failed check

- Formatting: run `make fmt` and review the diff.
- Module files: run `go mod tidy`, inspect the diff, then retry `make tidy-check deps`. The dependency check includes test imports and fails if package loading itself fails.
- Tests: download the `test-output` artifact for `test.log` and `coverage.out`, or use `make coverage` locally. The Actions summary includes package results and total coverage. Generated logs and coverage are ignored by Git.
- Shuffled tests: copy the printed seed and reproduce with `go test -race -shuffle=<seed> -count=1 ./...`. Use `-run '<TestName>'` for a focused regression.
- Fuzzing: download `fuzz-failures`, preserve the input under `testdata/fuzz/<TargetName>/`, then run `go test . -run '<TargetName>/<hash>'` to replay it. `make fuzz FUZZTIME=2m` runs both targets for longer.
- Workflow changes: run `make workflow-lint`. Changes to check commands must update the Makefile and this guide together.
- Vulnerabilities: reproduce with `make vuln`, inspect the affected call path, and update the Go patch version or code as appropriate. Follow [SECURITY.md](SECURITY.md) for private reports.

The weekly run catches new vulnerability database findings even when there are no code changes. Workflow concurrency cancels superseded runs on the same ref; separate release tags have separate groups.

## Project layout

| Path | Contents |
| --- | --- |
| Root package (`translator`) | Public API only: entry points, format validation, loss policy, SSE decoding, error and diagnostic types. |
| `internal/adapter/chatcompletions`, `messages`, `responses` | Per-protocol request, response, and stream conversion. |
| `internal/adapter/shared` | Protocol-agnostic pieces: decode types, content and media mapping, tool identity, diagnostics, SSE framing, usage. |
| `internal/errclass` | Error classification, status mapping, and secret redaction. |
| `thinking` | Optional reasoning controls and model-name suffixes. Not part of the default conversion path. |
| `testdata/conversion/{request,response,stream}` | The reviewed conversion snapshots, named `<from>_to_<to>.json`. |

[Code structure and maintenance](docs/architecture.md) describes dispatch rules and state contracts in more detail.

## Design rules

1. **No dependencies.** The `lint` job uses `scripts/check-dependencies.sh` to resolve production and test package graphs and checks module ownership. It rejects any external module, including similarly named sibling modules, and propagates package-loading errors. Discuss an import in an issue before proposing it.
2. **The root package is the API.** Everything under `internal/` may change at any time; do not re-export internals unless a caller genuinely needs the value.
3. **Keep all six cross-protocol directions working.** A mapping added to one direction usually needs its counterpart; same-protocol requests only rewrite the model.
4. **Requests dispatch by target protocol; responses and streams dispatch by upstream protocol.** Do not collapse the direct paths into a shared intermediate representation.
5. **State is per execution.** `RequestState` carries tool identities, options, and diagnostics for one request. No package-level mutable state, no shared caches, no goroutines that outlive a call.
6. **Callers own the edges.** Transport, retries, capability checks, and user-facing error responses belong to the caller. Report what happened through `*translator.Error` and diagnostics instead of guessing.
7. **Never emit secrets.** Error messages are logged by callers; wrap your own text with `translator.Redact`. Code under `internal/` cannot use the root package, so it calls `errclass.Redact` instead.
8. **Decode errors are handled, not discarded.** Output-producing decodes must check the error. A genuine best-effort probe goes through `shared.DecodeProbe`, whose doc comment states the contract: a shape mismatch leaves the target unchanged and the path keeps the original bytes. `shared.StringField` is the same idea for a single string field and reports `ok=false` so callers can stop instead of substituting an empty string.

## Changing conversion behavior

Work through this list; reviewers will check it against the diff.

1. **Identify the direction and the entry point.** Confirm whether the change belongs to a request, an ordinary response, or a stream, and whether it affects native normalization (`NormalizeResponsesRequest`) rather than cross-protocol conversion.
2. **Decide representability before writing code.** If the target protocol cannot carry a field faithfully, record a [`Diagnostic`](README.md#loss-diagnostics-and-errors) with a stable code and path instead of dropping it silently. `Diagnostic` carries `Code`, `Path`, `Message`, `Semantic`, `From`, and `To`: `Semantic` marks a loss that changes request semantics, which is what `LossPolicySafe` rejects.
3. **Respect the loss policy.** The policy decides request outcomes only: `LossPolicySafe` rejects semantic request losses, `LossPolicyStrict` rejects every reported request loss, and responses and streams report losses without being rejected because of them. They still fail on malformed data, upstream failures, and termination errors.
4. **Preserve what the current path preserves.** Paths that use `json.RawMessage` keep unknown fields and exact numbers; do not add a full re-encode through modeled types.
5. **Keep tool identities intact.** Register declarations and history references through the existing transaction helpers so a failed conversion leaves the registry unchanged.
6. **Streams have their own contract.** Preserve event order, emit `[DONE]` only for Chat output, keep `sequence_number` monotonic for synthesized Responses events, validate termination in `Finish`, and never discard events that were produced before an error.
7. **Update snapshots deliberately.** `go test . -run TestProtocolGolden -update` rewrites the 27 snapshots under `testdata/conversion/`. Read every changed line and explain it in the pull request. Never regenerate snapshots to make a red test green; if you cannot explain a diff, that is the bug.
8. **Update the documentation.** Behavior, limits, and direction matrices live in [protocol conversion](docs/protocol-conversion.md); module responsibilities live in [code structure](docs/architecture.md). Both documents have a `.zh-CN.md` counterpart that must be updated in the same pull request, as does the README pair when the public API or installation changes.

## Tests

- Test protocol behavior through the public entry points (`ConvertRequest`, `ConvertResponse`, `NewStreamConverter`, `NormalizeResponsesRequest`, `SSEDecoder`). Reach into `internal/...` only for parsers, collectors, and other state components.
- Prefer table-driven tests with a name per case, and add one regression case per fixed bug.
- Cover the shapes a hostile or buggy upstream can send, not only the happy path: a field carrying the wrong JSON type (a non-string `arguments`, a `citations` object where an array is expected, a query that is a number), an integer beyond 2^53, and a literal outside the `float64` range such as `1e400`. These are the cases where a discarded decode error becomes wrong output, and `DecodeProbe` must never be the reason a value reaches the caller as an empty string.
- Every stream change should cover: whole input, bytewise input, a missing final separator, and EOF without a terminal event.
- Fuzz targets must not depend on randomness or state beyond the input bytes they are handed. If fuzzing finds a crash, keep the finding: add the input to `testdata/fuzz/<TargetName>/` and a focused regression test alongside it.
- Benchmarks (`Benchmark*` in `internal/adapter/...`) are informative, not gated. Run them with `go test -run '^$' -bench . ./internal/adapter/...` when you touch hot paths.
- Tests must pass with `-race`. Do not add shared state to make a test convenient.

## Formatting, lint, and comments

- `.gitattributes` keeps text files in LF on all platforms, including Windows. Golden snapshots are compared byte for byte; keep these checkout rules when adding fixtures.
- Formatting is `gofmt` only; CI fails on any unformatted file. `gofumpt`-style rewrites are welcome in a dedicated pull request but are not enforced.
- `.golangci.yml` pins the checks. Deliberate deviations are documented there:
  - `errcheck` has no global exclusions. Output-producing decodes handle their error, and documented best-effort probes call `shared.DecodeProbe`, so a new bare `json.Unmarshal` fails lint.
  - `gocritic` keeps its default checks and adds the `diagnostic` tag; `ifElseChain` is disabled explicitly because the converters keep if/else chains for mixed predicates.
  - `staticcheck` keeps correctness (`SA*`) and simplicity (`S1*`) checks and disables style (`ST*`) and quickfix (`QF*`) classes.
- Suppress a finding inline only with a reason: `//nolint:nilerr // an undecodable body carries no protocol error envelope`. `nolintlint` requires a specific linter, an explanation, and no unused directive, so prefer a comment over widening the configuration.
- Changing `.golangci.yml` or the pinned versions in CI is a reviewed change: state what it catches and why the exclusions are safe.
- Write comments for the reason, not the mechanics, and keep exported identifiers documented; reviewers read them.
- Keep the tone of the existing code: complete sentences, protocol terms in backticks, no invented guarantees.

## Commits and branches

This repository follows [Conventional Commits](https://www.conventionalcommits.org/): lowercase type, optional scope, imperative subject, no trailing period.

```
fix: reject document media in Messages tool results
feat: support local Responses shell tools
refactor: isolate adapter state and preserve output on stream errors
docs: refresh READMEs and add project logo
```

| Prefix | Used for |
| --- | --- |
| `feat` | New representable behavior or public API |
| `fix` | Wrong, dropped, or invalid output; crashes; stream failures |
| `refactor` | Internal restructuring with no wire-output change |
| `docs` | README, `docs/`, and comment-only changes |
| `test` | Tests, snapshots, and fuzzing |
| `ci` | Workflows, lint configuration, dependency updates |
| `design` | Logo, branding, and other visual assets, as used in the existing history |
| `chore` | Anything else that is not user-visible |

Name branches `<type>/<short-topic>`, for example `feat/responses-shell-tool` or `fix/messages-document-tool-results`. Keep one logical change per pull request; a refactor plus a behavior change is two.

## Pull requests

1. Open an issue first for anything larger than a focused fix, so the mapping question is settled before the implementation.
2. Fill in the [pull request template](.github/pull_request_template.md), including the affected directions and every snapshot change.
3. Make sure `CI passed` is green, including all underlying validation jobs. Coverage is informational; a drop needs a reason, not a threshold.
4. Expect review on protocol semantics, not style: how the field maps in both directions, what happens under each loss policy, and what the snapshots now say.
5. Do not rewrite published history on `main`. Force-push only your own feature branch.

## Releases (maintainers)

Versioning is [Semantic Versioning](https://semver.org/), tagged `vX.Y.Z` from `main`. Before 1.0 the public API is still settling: a conversion change that alters wire output can land in a minor bump, but it still belongs in the release notes because callers may depend on the exact bytes. After 1.0 the same change requires a major bump. The module path has no `/vN` suffix, so only `v0` and `v1` tags are accepted; a `v2` tag would need a `/v2` module path first.

The release workflow gates the GitHub Release, not the module version: once a tag is pushed, the Go toolchain can already resolve it. Cut releases only from commits that are on `main` and already green.

1. Confirm `CI passed` is green on the exact `main` commit you intend to tag.
2. Land the release preparation as a normal pull request: the version in both READMEs (the `go get` snippet and the release badge), any "unreleased behavior" notes that the release makes obsolete, and the version/location tables in `docs/`.
3. Merge to `main`, then confirm the commit to tag: `git fetch origin && git log --oneline -1 origin/main`.
4. Tag and push:

   ```sh
   git tag -a vX.Y.Z origin/main -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```

5. [`.github/workflows/release.yml`](.github/workflows/release.yml) first validates the version against the module path and checks that the commit is an ancestor of `origin/main`. Only then does it run the full CI workflow on the tagged commit and create the GitHub Release with generated notes when every check passes. Tags with a suffix such as `v0.3.0-rc.1` are published as pre-releases.
6. Re-running the release job is safe: an existing release is left untouched, so a failed step can be retried. Never move a published tag; correct the notes with `gh release edit` instead.
7. Prereleases are for feedback; promote them by tagging the final version once the notes are accurate.
8. Configure branch protection or a ruleset to require `CI passed` (Settings → Branches or Rules → Rulesets). This stable check fails on failed, cancelled, or skipped validation jobs, including matrix jobs. When adding a validation job, add it to the aggregate job's `needs` list. Retire any old required check names only after the new check has run successfully on a pull request; these repository settings are maintained separately from workflow files.

## Reporting bugs and security issues

Use [the issue templates](.github/ISSUE_TEMPLATE) for bugs and feature requests. A useful bug report contains the library version, the Go version, the exact direction (or `NormalizeResponsesRequest`), the entry point, a minimal redacted sample, and the expected versus actual bytes.

Do not report security vulnerabilities in public issues. See [SECURITY.md](SECURITY.md) and use [private vulnerability reporting](https://github.com/dillonzq/api-translator/security/advisories/new). Reports about leaked credentials in error messages, diagnostic paths, or stream output are treated as security issues.
