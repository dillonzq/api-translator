# Security policy

## Supported versions

Security fixes land on `main` and in the latest release. Older tags are not maintained.

| Version | Supported |
| --- | --- |
| `main` | ✅ |
| Latest release | ✅ |
| Older releases | ❌ |

## Reporting a vulnerability

Use [private vulnerability reporting](https://github.com/dillonzq/api-translator/security/advisories/new). Do not open a public issue for a suspected vulnerability.

A useful report contains:

- the affected version or commit;
- the entry point involved (`ConvertRequest`, `ConvertResponse`, `NewStreamConverter`, `NormalizeResponsesRequest`, `SSEDecoder`);
- the input bytes or SSE frames that trigger it, reduced as far as you can;
- the impact you observed or expect.

Redact credentials before sending anything. The library's redaction helpers are covered by tests, but they are not a substitute for removing real secrets from a report.

## What counts as a security issue here

This package performs no network calls, holds no credentials, and writes no files. The issues that matter are the ones that leak or corrupt data crossing a trust boundary:

- secrets echoed into an error message, a diagnostic, or stream output, for example an upstream body that repeats an `Authorization` header;
- malformed upstream or client input that makes a converter panic, hang, or emit data belonging to another request;
- conversion that silently rewrites or drops caller-visible protocol data in a way a hostile upstream can steer, because callers act on what this library reports.

Out of scope: HTTP transport, authentication, retries, timeouts, provider policy, model capability checks, and model-name validation. Those belong to the caller.

## What to expect

Acknowledgement within a few days. Fixes land on `main` and ship as a patch release when the report concerns released behavior. The advisory is published once a fix is available, crediting the reporter unless they prefer otherwise.
