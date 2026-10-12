<!-- Keep the summary short; reviewers read the diff and the snapshot changes first. -->

## Summary

<!-- What changes, and why. Reference the issue: "Fixes #123". -->

## Type of change

- [ ] Bug fix — wrong, dropped, or invalid output; crash; stream failure
- [ ] New capability — new representable field, tool shape, or mapping
- [ ] Refactor — internal change with no wire-output difference
- [ ] Documentation, tests, or pipeline only

## Affected conversion paths

<!-- Tick every direction whose output, diagnostics, or errors change. -->

- [ ] `chat-completions` → `responses`
- [ ] `chat-completions` → `messages`
- [ ] `responses` → `chat-completions`
- [ ] `responses` → `messages`
- [ ] `messages` → `chat-completions`
- [ ] `messages` → `responses`
- [ ] Same-protocol model rewrite or passthrough
- [ ] `NormalizeResponsesRequest` (native Responses tool normalization)
- [ ] Streaming only (`NewStreamConverter`, `SSEDecoder`)
- [ ] No output change

## Checklist

- [ ] `go test -race -shuffle=on -count=1 ./...` passes
- [ ] `gofmt -l .` is empty; `go vet ./...`, `go mod tidy -diff`, and `golangci-lint run` are clean
- [ ] No new dependencies
- [ ] Output-producing decodes handle their error; documented best-effort probes use `shared.DecodeProbe` or `shared.StringField`
- [ ] New losses are reported as `Diagnostic` values instead of being dropped silently, and their behavior under `LossPolicySafe` / `LossPolicyStrict` is intended
- [ ] Snapshot changes (`testdata/conversion/`) are reviewed line by line and explained below
- [ ] Streaming changes cover full input, bytewise input, missing final separator, and EOF without termination (or N/A)
- [ ] Tool identity registration and rollback behavior are covered by a test (or N/A)
- [ ] `README.md`/`README.zh-CN.md` and `docs/*.md`/`docs/*.zh-CN.md` are updated where behavior or limits changed
- [ ] Any `//nolint` directive states a reason, and `.golangci.yml` is unchanged unless the pull request explains the change

## Snapshot and protocol notes

<!-- Quote the interesting snapshot lines. Link the upstream specification for field-level changes. -->

## Reviewer notes

<!-- Trade-offs, follow-ups, and anything you deliberately left out. -->
