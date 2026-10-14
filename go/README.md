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

One carrier call prices the parcel; the session reuses it through `QuoteID`:

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
if quote.IsPriced() && quote.QuoteID != nil {
	best := quote.Cheapest()
	fmt.Println(best.Carrier, best.Service, best.Amount)          // USPS Ground Advantage 38.42
	// QuoteID is good for 15 minutes; a session opened with it reuses these rates.
	session, err := client.CreatePaymentSession(ctx, smklog.CheckoutSessionRequest{
		QuoteID: *quote.QuoteID, Service: best.Service,
	})
	if err != nil {
		return err
	}
	fmt.Println(session.HandoffURL)                               // the human pays here
	if session.SessionID == nil {                                 // nil only when the record was not stored
		return errors.New("session not stored; nothing to poll")
	}
	status, err := client.GetCheckoutStatus(ctx, *session.SessionID)
	if err != nil {
		return err
	}
	fmt.Println(status.Status)                                    // awaiting_checkout ... label_ready
}
```

Every `GetParcelQuote` call spends a live carrier rating that costs SMKlog money: one call per
shipment, cache on your side, never poll. A priced answer carries `QuoteID`; pass it to
`CreatePaymentSession` so the pair costs one carrier call.

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
in each response struct's `Extra` map and marshal back out, so every value the API sent comes
back out of a decoded answer; a field the API left out re-encodes as its Go zero value (`null`,
`0`, `false`). Fields the schema marks nullable are pointers (`nil` is the API's `null`), among
them `QuoteResponse.QuoteID` and `CheckoutSession.SessionID`.

`QuoteResponse` has two helpers and one handle:

- `Cheapest()`: the rate with the lowest `Amount`, or `nil` when there are none.
- `IsPriced()`: `true` when `Mode` is `parcel_label_ready` and at least one rate came back.
- `QuoteID` (`*string`): the handle a priced answer carries for 15 minutes. It is `nil` when the
  API sent `null` (nothing was priced, or the handle could not be stored) or left the field out
  (answers without rates), so check it before dereferencing; `nil` re-encodes as `null`.

`Rates` stays in the API's order (best value first).

`CreatePaymentSession` takes one of two forms of `CheckoutSessionRequest`, and the client does
not turn one into the other:

- `{QuoteID: *quote.QuoteID, Service: best.Service}` (preferred): the session reuses that
  quote's rates and spends no second carrier call; `QuoteReused` comes back `true`. The shipment
  fields are optional here and ignored while the handle is valid, and `Quantity` is not
  defaulted: the quote names the shipment.
- `{Product, FromZip, ToZip, ToCountry, Quantity}`: the shipment itself, priced live inside the
  call (one carrier call). `Product`, `FromZip` and `ToZip` are required and checked locally like
  a quote request; `Quantity` `0` is sent as `1`.

`Service` is optional in both: a case-insensitive fragment of the display name that anchors the
amount, cheapest when omitted or unmatched. A `QuoteID` older than 15 minutes makes the API
price the shipment fields live instead, so send them alongside an old handle when you have them.

Either form may also carry `ExternalID`, your own key for the session (an order or cart id),
when the client holds a token. The call is then a create-or-update: it creates the session if
none exists for that id and re-prices it in place (same `SessionID`) if one does, and `Created`
(`*bool`) says which branch ran. A session whose checkout the human has already started is left
alone: the API answers 409 `session_in_checkout`, an `*APIError` whose `Body` names the session.
Without a token, `ExternalID` is refused with 401 `token_required`.

## Options

```go
smklog.New(
	smklog.WithBaseURL("https://quote-api.smklog.com"), // default
	smklog.WithTimeout(30*time.Second),                 // per attempt; a quote can take 10-20 s; 0 turns it off
	smklog.WithToken("..."),                            // OAuth bearer: a dedicated bucket for quotes; needed for ExternalID
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
(`LengthCm`, `WidthCm`, `HeightCm`, `WeightKg`) all or none and within the schema's bounds
(1-300 cm, 0.1-1000 kg). `Quantity` `0` is sent as `1`.

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

- **A rate limit is never retried**, whatever status carries it. The buckets are hourly (about
  80 quotes, 60 status reads, 20 token requests per client); a retry loop only burns the bucket
  faster. The caller decides.
- `GetCheckoutStatus` and `Status` are retried at most twice, with a short backoff, on
  network failures and 5xx answers.
- `GetParcelQuote` and `CreatePaymentSession` are never retried automatically: a quote spends
  a live carrier rating, a session opens a checkout.
- A canceled context stops the loop at once.

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

MIT license; see [LICENSE](LICENSE).
