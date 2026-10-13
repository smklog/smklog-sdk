# SMKlog SDK

Official client libraries for the [SMKlog Quote API](https://smklog.com/developers/):
live USPS, UPS, FedEx and DHL Express parcel rates from a US origin, a checkout handed
to a human, and where that checkout stands. No API key, no account. Four operations:

| Operation | What it does |
| --- | --- |
| `getParcelQuote` | Price one parcel: describe the item in plain words (the packed box is estimated) or send exact measurements. Up to five purchasable rates, each a checkout total with the carrier's counter price beside it, and a `quote_id` good for 15 minutes. |
| `createPaymentSession` | Open a checkout for a human from a `quote_id` (no second carrier call) or from a shipment. Returns the amount and a handoff URL; nothing is charged. |
| `getCheckoutStatus` | Where that checkout stands: awaiting, started, paid, label ready (with the tracking number), delivered, refunded. |
| `status` | Liveness and the serving version. |

| Language | Folder | Install |
| --- | --- | --- |
| JavaScript / TypeScript (Node >= 18) | [`javascript/`](javascript/) | `npm install smklog` |
| Python >= 3.9, with the `smklog` command line | [`python/`](python/) | `pip install smklog-sdk` |
| Go >= 1.21 | [`go/`](go/) | `go get github.com/smklog/smklog-sdk/go` |

All three follow one design ([docs/DESIGN.md](docs/DESIGN.md)): the same operation
names, the same error types, snake_case fields exactly as the API sends them, no
third-party runtime dependencies, and no automatic retry on a rate limit.

## Quickstart

Quote once, then open the checkout with the `quote_id` the answer carries: the session
reuses those rates, so the pair costs one carrier call.

```js
import { SmklogClient } from 'smklog';
const client = new SmklogClient();
const quote = await client.getParcelQuote({
  product: 'Yamaha FG800 acoustic guitar in a hard case',
  quantity: 1, from_postal_code: '07102', to_postal_code: '30303'
});
for (const rate of quote.rates) console.log(rate.carrier, rate.service, rate.amount);
const best = quote.cheapest();
if (best && quote.quote_id) {           // every priced answer carries quote_id, good for 15 minutes
  const session = await client.createPaymentSession({ quote_id: quote.quote_id, service: best.service });
  console.log(session.handoff_url);     // the human pays here; session.quote_reused is true
}
```

```python
from smklog import SmklogClient
client = SmklogClient()
quote = client.get_parcel_quote(product="Yamaha FG800 acoustic guitar in a hard case",
                                from_postal_code="07102", to_postal_code="30303")
for rate in quote.rates: print(rate.carrier, rate.service, rate.amount)
if quote.is_priced() and quote.quote_id:   # every priced answer carries quote_id, good for 15 minutes
    session = client.create_payment_session(quote_id=quote.quote_id, service=quote.cheapest().service)
    print(session.handoff_url)             # the human pays here; session.quote_reused is True
```

```go
client := smklog.New()
quote, err := client.GetParcelQuote(ctx, smklog.QuoteRequest{Product: "Yamaha FG800 acoustic guitar in a hard case",
    Quantity: 1, FromPostalCode: "07102", ToPostalCode: "30303"})
if err != nil {
    return err
}
if quote.IsPriced() && quote.QuoteID() != "" { // every priced answer carries a quote_id, good for 15 minutes
    session, err := client.CreatePaymentSession(ctx, smklog.CheckoutSessionRequest{
        QuoteID: quote.QuoteID(), Service: quote.Cheapest().Service,
    })
    if err != nil {
        return err
    }
    fmt.Println(session.HandoffURL) // the human pays here; session.QuoteReused is true
}
```

## Before you call it

- **Every quote spends a live carrier rating that costs SMKlog money.** One call per
  shipment; cache on your side; never poll. Every priced quote answer carries a
  `quote_id`, good for 15 minutes: pass it to `createPaymentSession` and the pair costs
  one carrier call (the session answers with `quote_reused: true`). It is `null` when
  nothing was priced or the handle could not be stored, and an expired handle makes the
  API price the shipment fields live, so send those along with an old id when you have
  them.
- **Rate limits** are per client and hourly: about 80 quotes, 60 checkout-status reads,
  20 token requests. A 429 raises `RateLimitedError`; the SDK never retries it. A client
  that needs its own bucket holds OAuth client credentials, issued by email
  ([auth.md](https://smklog.com/auth.md)) and passed as the `token` option.
- **A human always pays.** `createPaymentSession` returns a handoff URL; the person
  confirms the shipping consents and pays on Stripe hosted checkout. Nothing in this
  API can charge anything.
- The contract is the OpenAPI 3.1 document at
  [quote-api.smklog.com/openapi.json](https://quote-api.smklog.com/openapi.json); the
  copy in [`testdata/`](testdata/) is what the tests replay. On the developer portal:
  the [playground](https://smklog.com/developers/playground), the
  [status page](https://smklog.com/developers/status), the
  [rate limits](https://smklog.com/developers/rate-limits) and the
  [schemas](https://smklog.com/developers/); agents can also use the
  [MCP server](https://quote-api.smklog.com/mcp).

## Development

Tests run offline against a local replay of the vendored examples:

```
cd javascript && node --test
cd python && python3 -m unittest discover -s tests
cd go && go vet ./... && go test ./...
```

Set `SMKLOG_LIVE=1` to also run the one live test (it calls `status` only). Refresh the
vendored contract with `scripts/refresh-testdata.sh` when the API changes.

Questions: [info@smklog.com](mailto:info@smklog.com).

MIT license; see [LICENSE](LICENSE).
