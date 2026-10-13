# SMKlog SDK: design shared by every language

This document is the contract between the three clients. A reader who knows one of
them should be able to guess the other two.

## What the API is

The SMKlog Quote API (`https://quote-api.smklog.com`) has four operations, no API key,
and prices one parcel at a time. The authoritative contract is the OpenAPI 3.1 document
at `https://quote-api.smklog.com/openapi.json`; a copy is vendored at
`testdata/openapi.json` with the JSON Schemas in `testdata/schemas/` and the example
payloads in `testdata/samples/`. Nothing in an SDK may contradict those files.

| Operation | Method and path | SDK name | Returns |
| --- | --- | --- | --- |
| Price one parcel | `POST /quote` | `getParcelQuote` | `QuoteResponse` |
| Open a checkout for a human | `POST /agent/checkout-session` | `createPaymentSession` | `CheckoutSession` |
| Read where a checkout stands | `GET /agent/checkout-session/{session_id}` | `getCheckoutStatus` | `CheckoutStatus` |
| Liveness | `GET /status` | `status` | `Liveness` |

Field names on the wire are `snake_case` and identical in every language; the SDKs do
not rename them. Money is a JSON number in USD. The error body is always the `Problem`
shape: `error` (stable code), `message` (sentence), optional `missing` (field names).

## Client

- Constructor options: `baseUrl` (default `https://quote-api.smklog.com`),
  `timeout` (default 30 s; a quote can take 10-20 s while carriers answer),
  `token` (optional OAuth bearer; sent as `Authorization: Bearer ...` only when set),
  `userAgent` suffix (the SDK always sends `smklog-sdk-<language>/<version>` first).
- Headers on every call: `Accept: application/json`, `Content-Type: application/json`
  on POST, `User-Agent` as above.
- **No automatic retry on 429.** The buckets are hourly (80 quotes, 60 status reads,
  20 token requests per client); retrying in a loop only burns the bucket faster.
  A 429 raises `RateLimitedError` (a subclass of the API error) and the caller decides.
- Retry at most twice, with a short backoff, only on network failures and 5xx on the
  idempotent GETs (`getCheckoutStatus`, `status`). Never retry a POST automatically:
  `POST /quote` spends a live carrier rating.
- Input validation before the request: ZIP shape for US origins (5 digits), country
  in `US, CA, GB, DE, AU`, quantity >= 1, the four measurements all-or-none. Reject
  locally with a clear message rather than spending a request.
- `getParcelQuote` has one convenience: the result exposes `rates` best-value-first as
  the API returns them, plus `cheapest()` (lowest `amount`) and `isPriced()`
  (`mode == parcel_label_ready` and at least one rate).
- `createPaymentSession` accepts either a `quote_id` (preferred: reuses the quote, no
  second carrier call) or a shipment; the SDK does not invent one from the other and
  passes a `quote_id` through as given. The `quote_id` comes from the `getParcelQuote`
  answer: valid 15 minutes, `null` when nothing was priced or the handle could not be
  stored, absent on answers without rates. While the handle is valid the API reuses
  that quote's rates, ignores any shipment fields sent next to it and answers with
  `quote_reused: true`; an expired handle makes the API price the shipment fields
  live, so a caller that still has them sends them alongside an old id.

## Errors

One error type per language carrying `status` (HTTP), `error` (code), `message`,
`missing` (list, may be empty) and the raw body. Specializations by code:

| Code | Type |
| --- | --- |
| `rate_limited`, `slow_down` | `RateLimitedError` |
| `product_not_recognized`, `product_url_not_detected`, `missing_required_fields`, `invalid_request`, `invalid_json` | `InvalidRequestError` |
| `session_not_found` | `NotFoundError` |
| `session_failed` | `SessionFailedError` |
| anything else | the base `SmklogError` |

Network failures and timeouts raise the language's native error wrapped as
`SmklogError` with `status = 0` and `error = "network_error"`. Go, which has no
subclasses, keeps the one `*APIError` and classifies it through `errors.Is` against
the sentinels `ErrRateLimited`, `ErrInvalidRequest`, `ErrNotFound`, `ErrSessionFailed`
and `ErrNetwork`.

## Models

Typed from `testdata/schemas/*.schema.json`: `QuoteRequest`, `QuoteResponse`, `Rate`,
`Package`, `CheckoutSessionRequest`, `CheckoutSession`, `CheckoutStatus`, `Liveness`,
`Problem`. `QuoteResponse` carries `quote_id`, the handle described above. Unknown
fields are preserved (the API adds fields without a version bump; `QuoteResponse` is
`additionalProperties: true`).

## Tests (every language)

Each language folder carries the same three kinds of test:

1. Unit tests against a local HTTP server that replays `testdata/samples/*.json`:
   each operation's request example round-trips, each documented error maps to the
   right error type, a 429 raises `RateLimitedError` without a retry, a 503 on
   `status()` is retried and then succeeds, and validation rejects a 4-digit ZIP
   locally. The Python folder drives the `smklog` command line against the same
   replay server.
2. A contract test that reads `testdata/openapi.json` and asserts that every operation
   the document declares has a method on the client, so no operation goes silently
   missing.
3. A live test that runs only when `SMKLOG_LIVE=1` is set and calls `status()` against
   production. Nothing in any suite calls `getParcelQuote` for real: a quote spends a
   live carrier rating.

No third-party runtime dependencies in any language. Test dependencies: none beyond
each language's standard tooling (`node:test`, `unittest`, `go test`).

## Versioning

All three start at `0.1.0` and move together. The SDK version is independent of the
API's product version (1.4.0 today); the API changes additively and the SDK is
rebuilt against the vendored contract when `testdata/openapi.json` is refreshed from
`https://quote-api.smklog.com/openapi.json` (`scripts/refresh-testdata.sh`).

## Packaging

| Language | Folder | Package | Install |
| --- | --- | --- | --- |
| JavaScript / TypeScript | `javascript/` | `smklog` (npm; ESM, Node >= 18, no build step: plain `.js` + `.d.ts`) | `npm install smklog` |
| Python | `python/` | distribution `smklog-sdk` on PyPI (the bare name `smklog` is already taken there), import package `smklog` (`pyproject.toml`, `src/smklog/`, Python >= 3.9, stdlib `urllib` only); CLI entry point `smklog` | `pip install smklog-sdk` |
| Go | `go/` | module `github.com/smklog/smklog-sdk/go`, package `smklog`, Go >= 1.21, stdlib only | `go get github.com/smklog/smklog-sdk/go` |

## CLI (Python)

```
smklog quote "Yamaha FG800 acoustic guitar in a hard case" 07102 30303 [--country US] [--qty 1] [--dims 112x48x20 --kg 6.4] [--json]
smklog session --quote-id q_... [--service "USPS Ground Advantage"] [--json]
smklog session-status as_... [--json]
smklog status [--json]
```

Human output is a small table (carrier, service, checkout total, counter price,
delivery); a priced `smklog quote` also prints the `quote_id` on its own line, ready
for `smklog session --quote-id` within 15 minutes; `--json` prints the raw answer.
Exit code 0 on a priced answer or a successful read, 2 on an API refusal (the
`Problem` is printed) or a quote answered without a price, 3 on a rate limit, 1 when
the host could not be reached. `--base-url` and `--token` (or `SMKLOG_BASE_URL` /
`SMKLOG_TOKEN`) are accepted by every command; `python -m smklog` runs the same
program.

## Documentation

Each folder's README covers install, a short quickstart (a quote, then a checkout
opened with its `quote_id`), the error table and a link to
`https://smklog.com/developers/`. The root README introduces the API in one paragraph,
links the three folders, states the rate limits and that no key is needed, and points
at the OpenAPI document, the playground and the status page. Every README ends with
the line `MIT license; see [LICENSE](LICENSE).`
