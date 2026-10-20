# Code structure and maintenance

English | [简体中文](architecture.zh-CN.md) · [Protocol conversion](protocol-conversion.md)

## Entry points and direction

The root package owns public APIs, format validation, request loss policy, and SSE transport boundaries. Callers own HTTP, model capabilities, and retries.

Requests dispatch by **target protocol**. Ordinary responses and streams dispatch by **upstream protocol**. An adapter's response `outputFormat` always means the client's desired protocol. Keep the six direct conversion paths; same-protocol requests use `RewriteModelID` and only rewrite the model.

## Responsibilities

| Location | Responsibility |
| --- | --- |
| Root `request.go`, `response.go`, `stream.go` | Public dispatch, terminal error state, SSE framing, Responses sequence numbers |
| Adapter `request.go`, `convert.go` | Direct request and ordinary response mappings |
| Adapter `stream.go` | Upstream parsing, event dispatch, termination validation |
| Adapter `messages_stream.go`, `chat_stream.go`, `responses_stream.go` | Target stream output, as applicable to each adapter |
| Responses `native_output.go`, `stream_content.go` | Native tool restoration and text/reasoning/citation snapshot reconciliation |
| Shared `types_*.go`, `content.go`, `media.go` | Protocol decode types and content representation |
| Shared `events_*.go`, `output_assembler.go`, `results.go` | Target event and ordinary result construction |
| Shared `sse_framer.go`, `sse_encode.go` | SSE framing and encoding |
| Shared `usage.go`, `terminal.go` | Usage and termination mappings |
| Shared `tool_identity.go`, `responses_tools.go`, `normalize_responses.go` | Tool identity rules, cross-protocol normalization, explicit native normalization |
| Shared `shell.go`, `reasoning.go`, `cache_control.go`; Messages `cache_control.go` | Local shell mapping/restoration, reasoning history, and cache hint retention added in `v0.3.0` |
| Shared `context.go`, `diagnostic_collector.go`, `diagnostics.go` | Execution state, diagnostic collection, and loss rules |
| `thinking` | Explicit reasoning controls and model suffixes |

## Tools and execution state

The public `RequestState` API is unchanged. Internally it holds a `ConversionContext` containing a `ToolRegistry`, options, and a diagnostic collector. Diagnostics deduplicate in first-observed order and return independent copies.

Native Responses normalization uses `json.RawMessage` to preserve extensions and exact numbers. Cross-protocol conversion uses modeled types. Both paths share wire-name generation, declaration deduplication, identity collisions, and history/tool-choice reference checks without re-encoding the whole request through one representation.

Declaration deduplication is local to the current input: the first declaration wins. Existing state identities still participate in collision checks. History and choice references use original names and namespaces; a flattened name cannot introduce a different identity.

Both public normalization paths use `WithToolTransaction`. Success commits the registry; failure retains previous identities. Both outcomes retain diagnostics. Loss policy only rejects requests.

## Stream state

Target state is grouped separately: `messagesOutput` and `responsesOutput` in the Chat Completions adapter; `chatOutput` and `responsesOutput` in the Messages adapter; `messagesOutput` and `nativeOutput` in the Responses adapter. Source tool identities, argument reconciliation, and protocol termination remain upstream-adapter responsibilities.

Chat → Messages and Responses → Messages share `MessagesBlocks` for increasing indexes, close-before-start ordering, and suppressing deltas after closure. Queues retain their different completion signals: Chat tools usually wait for stream completion, whereas Responses tools can complete individually. Citation windows and search-result queueing remain adapter-specific.

`Feed` and `Finish` may return completed events together with an error. Errors must not discard events already generated in that call. The public converter retains the first error and emits no subsequent normal events or successful completion.

## Validation

Test new protocol behavior through public entry points where possible; use internal tests for parsers and state components. The 27 snapshots pin existing outputs and should stay unchanged during structural refactoring.

Error-stream tests cover whole input, bytewise input, missing final separators, and EOF without termination. They also pin content and event order when a single Chat chunk or Responses terminal snapshot emits content before a later failure. Tool tests compare native and both cross-protocol paths for first-declaration behavior, historical collisions, registry rollback, and diagnostic retention.

Validation commands are in the [README](../README.md#development-and-validation).
