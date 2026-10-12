# smklog

Official JavaScript / TypeScript client for the [SMKlog Quote API](https://smklog.com/developers/):
live USPS, UPS, FedEx and DHL Express parcel rates from a US origin, a checkout handed to a
human, and where that checkout stands. No API key, no account.

ESM, Node 18 or newer (global `fetch`), no dependencies, no build step: one `index.js` and
its `index.d.ts`.

## Install

```
npm install smklog
```

## Quickstart

```js
import { SmklogClient, RateLimitedError } from 'smklog';

const client = new SmklogClient();
const quote = await client.getParcelQuote({
  product: 'Yamaha FG800 acoustic guitar in a hard case',
  quantity: 1, from_postal_code: '07102', to_postal_code: '30303',
});
if (quote.isPriced()) {
  const best = quote.cheapest();
  console.log(best.carrier, best.service, best.amount);          // USPS Ground Advantage 38.42
  // The quote answer carries a quote_id for 15 minutes; reusing it spends no second carrier call.
  const session = await client.createPaymentSession({ quote_id: quote.quote_id, service: best.service });
  console.log(session.handoff_url);                               // the human pays here
  const status = await client.getCheckoutStatus(session.session_id);
  console.log(status.status);                                     // awaiting_checkout … label_ready
}
```

Every `getParcelQuote` call spends a live carrier rating that costs SMKlog money: one call per
shipment, cache on your side, never poll. Pass the `quote_id` to `createPaymentSession` so the
pair costs one carrier call.

## Operations

| Method | Endpoint | Returns |
| --- | --- | --- |
| `getParcelQuote(request)` | `POST /quote` | `QuoteResponse` |
| `createPaymentSession(request)` | `POST /agent/checkout-session` | `CheckoutSession` |
| `getCheckoutStatus(sessionId)` | `GET /agent/checkout-session/{session_id}` | `CheckoutStatus` |
| `status()` | `GET /status` | `Liveness` |

Field names are `snake_case` exactly as the API sends and receives them; the client renames
nothing and keeps fields it does not know about.

`getParcelQuote` resolves to a `QuoteResponse` object: the wire fields are its own properties
(so `JSON.stringify(quote)` is the raw answer) and two helpers live on its prototype:

- `quote.cheapest()`: the rate with the lowest `amount`, or `null` when there are none.
- `quote.isPriced()`: `true` when `mode` is `parcel_label_ready` and at least one rate came back.

`quote.rates` stays in the API's order (best value first).

`createPaymentSession` takes either `{ quote_id, service? }` (preferred: no second carrier
call) or a shipment `{ product, from_zip, to_zip, to_country?, quantity?, service? }`. The
`quote_id` is the handle the quote answer carries for 15 minutes; the published
`QuoteResponse` schema admits it as one of its additional fields rather than naming it, so the
client keeps it as-is and TypeScript sees it as `unknown` (`quote.quote_id as string`).

## Options

```js
new SmklogClient({
  baseUrl: 'https://quote-api.smklog.com', // default
  timeout: 30_000,                         // ms per attempt; a quote can take 10-20 s
  token: undefined,                        // OAuth bearer for a dedicated rate-limit bucket
  userAgent: 'my-app/2.0',                 // appended after smklog-sdk-javascript/0.1.0
});
```

## Errors

Every failure is a `SmklogError` with `status` (HTTP status, `0` when no response arrived),
`error` (the API's stable code), `message`, `missing` (field names, may be empty) and `body`
(the parsed `Problem`). Subclasses by code:

| `error` code | Class |
| --- | --- |
| `rate_limited`, `slow_down` | `RateLimitedError` (`retryAfter` seconds when the API sent the header) |
| `product_not_recognized`, `product_url_not_detected`, `missing_required_fields`, `invalid_request`, `invalid_json` | `InvalidRequestError` |
| `session_not_found` | `NotFoundError` |
| `session_failed` | `SessionFailedError` |
| anything else | `SmklogError` |

Network failures and timeouts raise `SmklogError` with `status: 0`, `error: 'network_error'`
and the native error as `cause`.

Local validation runs before any request and raises `InvalidRequestError` with `status: 0`:
a 5-digit US origin ZIP (and destination ZIP on US lanes), `to_country_code` in `US, CA, GB,
DE, AU`, `quantity` an integer of 1 or more, and the four measurements (`length_cm`,
`width_cm`, `height_cm`, `weight_kg`) all or none. `quantity` is sent as `1` when omitted.

```js
try {
  await client.getParcelQuote(request);
} catch (error) {
  if (error instanceof RateLimitedError) scheduleForLater(error.retryAfter);
  else if (error instanceof SmklogError) console.error(error.status, error.error, error.message);
  else throw error;
}
```

### Retries and rate limits

- **A 429 is never retried.** The buckets are hourly (about 80 quotes, 60 status reads, 20
  token requests per client); a retry loop only burns the bucket faster. The caller decides.
- `getCheckoutStatus` and `status` are retried at most twice, with a short backoff, on
  network failures and 5xx answers.
- `getParcelQuote` and `createPaymentSession` are never retried automatically: a quote spends
  a live carrier rating, a session opens a checkout.
- Each attempt is bounded by `timeout` through an `AbortController`.

## TypeScript

`index.d.ts` declares the client, its options, every model from the published JSON Schemas
(`QuoteRequest`, `QuoteResponse`, `Rate`, `Package`, `CheckoutSessionRequest`,
`CheckoutSession`, `CheckoutStatus`, `Liveness`, `Problem`) and the error classes.

## Tests

```
node --test
```

The tests replay the example payloads vendored in `../testdata/` from a local HTTP server and
never reach the network. `SMKLOG_LIVE=1 node --test` adds one live call to `status()`
against production; nothing in the suite ever calls `POST /quote` for real.

## Links

- Developer portal: https://smklog.com/developers/
- OpenAPI 3.1 contract: https://quote-api.smklog.com/openapi.json
- Rate limits: https://smklog.com/developers/rate-limits
- Status: https://smklog.com/developers/status

MIT © SMKLOG-2022 LLC
