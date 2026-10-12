# smklog (Go)

Official Go client for the [SMKlog Quote API](https://smklog.com/developers/): live USPS,
UPS, FedEx and DHL Express parcel rates from a US origin, a checkout handed to a human, and
where that checkout stands. No API key, no account.

Go 1.21 or newer, standard library only.

## Install

```
go get github.com/smklog/smklog-sdk/go
```

```go
import smklog "github.com/smklog/smklog-sdk/go"
```

## Quickstart

```go
client := smklog.New()
quote, err := client.GetParcelQuote(ctx, smklog.QuoteRequest{
	Product:        "Yamaha FG800 acoustic guitar in a hard case",
	FromPostalCode: "07102",
	ToPostalCode:   "30303",
})
if err != nil {
	return err
}
if quote.IsPriced() {
	best := quote.Cheapest()
	fmt.Println(best.Carrier, best.Service, best.Amount)          // USPS Ground Advantage 38.42
	// The quote answer carries a quote_id for 15 minutes; reusing it spends no second carrier call.
	session, err := client.CreatePaymentSession(ctx, smklog.CheckoutSessionRequest{
		QuoteID: quote.QuoteID(), Service: best.Service,
	})
	if err != nil {
		return err
	}
	fmt.Println(session.HandoffURL)                               // the human pays here
	status, err := client.GetCheckoutStatus(ctx, session.SessionID)
	if err != nil {
		return err
	}
	fmt.Println(status.Status)                                    // awaiting_checkout … label_ready
}
```

Every `GetParcelQuote` call spends a live carrier rating that costs SMKlog money: one call per
shipment, cache on your side, never poll. Pass the `QuoteID` to `CreatePaymentSession` so the
pair costs one carrier call.

## Operations

| Method | Endpoint | Returns |
| --- | --- | --- |
| `GetParcelQuote(ctx, QuoteRequest)` | `POST /quote` | `*QuoteResponse` |
| `CreatePaymentSession(ctx, CheckoutSessionRequest)` | `POST /agent/checkout-session` | `*CheckoutSession` |
| `GetCheckoutStatus(ctx, sessionID)` | `GET /agent/checkout-session/{session_id}` | `*CheckoutStatus` |
| `Status(ctx)` | `GET /status` | `*Liveness` |

Every method takes a `context.Context`; the context bounds the whole call, the client's
timeout bounds each attempt.

Field names are `snake_case` on the wire exactly as the API sends and receives them; the
structs add exported Go names and json tags, nothing else. Fields the API adds later are kept
in each response struct's `Extra` map and marshal back out, so a decoded answer re-encodes to
what the API sent.

`QuoteResponse` has three helpers:

- `Cheapest()`: the rate with the lowest `Amount`, or `nil` when there are none.
- `IsPriced()`: `true` when `Mode` is `parcel_label_ready` and at least one rate came back.
- `QuoteID()`: the handle the quote answer carries for 15 minutes (read from `Extra`, since
  the published schema admits it as an additional field rather than naming it).

`Rates` stays in the API's order (best value first).

`CreatePaymentSession` takes either `QuoteID` (preferred: no second carrier call) or the
shipment itself (`Product`, `FromZip`, `ToZip`, optional `ToCountry` and `Quantity`). The
client does not turn one into the other.

## Options

```go
smklog.New(
	smklog.WithBaseURL("https://quote-api.smklog.com"), // default
	smklog.WithTimeout(30*time.Second),                 // per attempt; a quote can take 10-20 s
	smklog.WithToken("..."),                            // OAuth bearer for a dedicated rate-limit bucket
	smklog.WithUserAgent("my-app/2.0"),                 // appended after smklog-sdk-go/0.1.0
	smklog.WithHTTPClient(&http.Client{}),              // your own transport (proxy, TLS)
)
```

## Errors

Every failure is an `*APIError` with `Status` (HTTP status, `0` when no response arrived),
`Code` (the API's stable code), `Message`, `Missing` (field names, may be empty), `Body`
(the raw response) and, on a 429, `RetryAfter` when the API sent the header. Classify it with
`errors.Is` against the sentinels, or the matching helpers:

| `Code` | Sentinel | Helper |
| --- | --- | --- |
| `rate_limited`, `slow_down` | `ErrRateLimited` | `IsRateLimited(err)` |
| `product_not_recognized`, `product_url_not_detected`, `missing_required_fields`, `invalid_request`, `invalid_json` | `ErrInvalidRequest` | `IsInvalidRequest(err)` |
| `session_not_found` | `ErrNotFound` | `IsNotFound(err)` |
| `session_failed` | `ErrSessionFailed` | `IsSessionFailed(err)` |
| `network_error` (no HTTP answer: connection failure or timeout) | `ErrNetwork` | `errors.Is(err, smklog.ErrNetwork)` |
| anything else | none | `errors.As(err, &apiErr)` |

The transport error behind a `network_error` is wrapped, so
`errors.Is(err, context.DeadlineExceeded)` keeps working.

Local validation runs before any request and returns an `*APIError` with `Status` `0` for
which `IsInvalidRequest` is true: a 5-digit US origin ZIP (and destination ZIP on US lanes),
`ToCountryCode` in `US, CA, GB, DE, AU`, `Quantity` of 1 or more, and the four measurements
(`LengthCm`, `WidthCm`, `HeightCm`, `WeightKg`) all or none. `Quantity` `0` is sent as `1`.

```go
quote, err := client.GetParcelQuote(ctx, req)
switch {
case err == nil:
	// priced or explained: check quote.IsPriced()
case smklog.IsRateLimited(err):
	// the hourly bucket is spent; the client never retries this
case smklog.IsInvalidRequest(err):
	var apiErr *smklog.APIError
	errors.As(err, &apiErr)
	log.Println(apiErr.Message, apiErr.Missing)
default:
	return err
}
```

### Retries and rate limits

- **A 429 is never retried.** The buckets are hourly (about 80 quotes, 60 status reads, 20
  token requests per client); a retry loop only burns the bucket faster. The caller decides.
- `GetCheckoutStatus` and `Status` are retried at most twice, with a short backoff, on
  network failures and 5xx answers.
- `GetParcelQuote` and `CreatePaymentSession` are never retried automatically: a quote spends
  a live carrier rating, a session opens a checkout.
- A cancelled context stops the loop at once.

## Tests

```
go test ./...
```

The tests replay the example payloads vendored in `../testdata/` from a local HTTP server and
never reach the network. `SMKLOG_LIVE=1 go test ./...` adds one live call to `Status`
against production; nothing in the suite ever calls `POST /quote` for real.

## Links

- Developer portal: https://smklog.com/developers/
- OpenAPI 3.1 contract: https://quote-api.smklog.com/openapi.json
- Rate limits: https://smklog.com/developers/rate-limits
- Status: https://smklog.com/developers/status

MIT © SMKLOG-2022 LLC
