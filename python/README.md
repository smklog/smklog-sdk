# smklog-sdk (Python)

Official Python client and command line for the [SMKlog Quote API](https://smklog.com/developers/):
live USPS, UPS, FedEx and DHL Express parcel rates from a US origin, a checkout handed
to a human, and where that checkout stands. No API key, no account, no dependencies
beyond the standard library. Python 3.9 or newer.

```
pip install smklog-sdk
```

The distribution is `smklog-sdk`; the import package is `smklog`.

## Quickstart

```python
from smklog import SmklogClient

client = SmklogClient()
quote = client.get_parcel_quote(product="Yamaha FG800 acoustic guitar in a hard case",
                                from_postal_code="07102", to_postal_code="30303")
for rate in quote.rates:                       # best value first, as the API returns them
    print(rate.carrier, rate.service, rate.amount, "counter", rate.retail_amount)
if quote.is_priced() and quote.quote_id:       # the handle is good for 15 minutes
    best = quote.cheapest()
    session = client.create_payment_session(quote_id=quote.quote_id, service=best.service)
    print(session.handoff_url)                 # the human confirms and pays here
    if session.session_id:                     # None only when the record was not stored
        print(client.get_checkout_status(session.session_id).status)
```

The pair costs one carrier call: the quote answer carries `quote_id`, and a checkout
opened with it reuses those rates (`session.quote_reused` is true) instead of pricing
the shipment again.

Exact measurements skip the box estimate: pass all four of `length_cm`, `width_cm`,
`height_cm`, `weight_kg` (the client marks the package `manual`). A request can also be a
`QuoteRequest` / `CheckoutSessionRequest` model or a plain dict; fields keep the wire names.

Every `get_parcel_quote` call spends a live carrier rating on the server: one call per
shipment, cache on your side, never poll. `create_payment_session` takes one of two forms:

- `create_payment_session(quote_id=..., service=...)`: the quote is reused and no second
  carrier call is spent. `quote_id` is the handle every priced quote answer carries; it is
  `None` when nothing was priced or the handle could not be stored. The API ignores any
  shipment fields sent next to a valid `quote_id`.
- `create_payment_session(product=..., from_zip=..., to_zip=..., to_country=..., quantity=...,
  service=...)`: the API prices the shipment live and opens the checkout in one call.

A `quote_id` older than 15 minutes makes the API price the shipment fields live, so send
them along when the quote may have aged. The client never turns one form into the other.

Either form may also carry `external_id=...`, your own key for the session (an order or cart
id), when the client holds a `token`. The call is then a create-or-update: it creates the
session if none exists for that id and re-prices it in place (same `session_id`) if one does,
and `session.created` says which branch ran. A session whose checkout the human has already
started is left alone: the API answers 409 `session_in_checkout`, a `SmklogError` whose
`body["session_id"]` names it. Without a token, `external_id` is refused with 401
`token_required`.

## Client options

| Option | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://quote-api.smklog.com` | The API host. |
| `timeout` | `30.0` | Seconds per request; a quote can take 10-20 s while carriers answer. |
| `token` | `None` | Optional OAuth bearer ([auth.md](https://smklog.com/auth.md)): a dedicated rate-limit bucket for quotes, and required for `external_id`; sent as `Authorization: Bearer ...` only when set. |
| `user_agent` | `None` | Suffix appended after `smklog-sdk-python/0.1.0`. |

Operations: `get_parcel_quote(...)` -> `QuoteResponse` (with `quote_id`, `rates`,
`cheapest()`, `is_priced()`), `create_payment_session(...)` -> `CheckoutSession`,
`get_checkout_status(session_id)` -> `CheckoutStatus`, `status()` -> `Liveness`.

Every model is a dataclass whose fields are the wire names. Fields this version does
not know about land in `.extra`; `.raw` is the body exactly as received; `.to_dict()`
serializes back. Only the idempotent GETs (`get_checkout_status`, `status`) are retried,
at most twice, on network failures and 5xx. POSTs are never retried, and a 429 is never
retried anywhere: the buckets are hourly (about 80 quotes, 60 status reads, 20 token
requests per client), so a retry loop only burns them faster.

## Errors

Every failure raises `SmklogError` or one of its subclasses, carrying `status` (HTTP
status, 0 when nothing was sent), `error` (the stable code), `message`, `missing`
(field names, often empty) and `body` (the response body as received). When the Problem
carries no `message`, the error's message is the code plus the missing fields
(`missing_required_fields: missing to_postal_code`). Errors pickle, so they cross a
`multiprocessing` boundary intact.

| Code | Exception |
| --- | --- |
| `rate_limited`, `slow_down` | `RateLimitedError` |
| `product_not_recognized`, `product_url_not_detected`, `missing_required_fields`, `invalid_request`, `invalid_json` | `InvalidRequestError` |
| `session_not_found` | `NotFoundError` |
| `session_failed` | `SessionFailedError` |
| anything else | `SmklogError` |

A network failure or timeout is `SmklogError` with `status == 0` and
`error == "network_error"`; the native exception is its `__cause__`. Requests the client
refuses before sending (a 4-digit ZIP, a country outside US/CA/GB/DE/AU, a quantity
below 1, measurements that do not come as a set of four) raise `InvalidRequestError`
with `status == 0`.

## Command line

```
smklog quote "Yamaha FG800 acoustic guitar in a hard case" 07102 30303 [--country US] [--qty 1] [--dims 112x48x20 --kg 6.4] [--json]
smklog session --quote-id q_... [--service "USPS Ground Advantage"] [--json]
smklog session-status as_... [--json]
smklog status [--json]
```

Human output is a small table; `--json` prints the raw answer. A priced `smklog quote`
also prints the `quote_id` on its own line, ready to pass to `smklog session --quote-id`
within 15 minutes (`--json` carries it in the body):

```
$ smklog quote "Yamaha FG800 acoustic guitar in a hard case" 07102 30303
Yamaha FG800 acoustic guitar in a hard case
Box: 112 x 48 x 20 cm, 6.4 kg (estimate, medium confidence)
Carrier  Service           Checkout  Counter  Delivery
USPS     Ground Advantage  $38.42    $46.95   2-5 business days
UPS      Ground            $41.10    $67.35   3 days, by 2026-10-16
quote_id  q_8f3c2e1a4b7d
$ smklog session --quote-id q_8f3c2e1a4b7d --service "USPS Ground Advantage"
```

Exit code 0 on a priced answer or a successful read, 2 when the API refuses (the Problem
is printed) or answers a quote without a price, 3 on a rate limit, 1 when the host could
not be reached.
`--base-url` and `--token` (or `SMKLOG_BASE_URL` / `SMKLOG_TOKEN`) are accepted by every
command. `python -m smklog` runs the same program.

## Development

```
cd python
python3 -m unittest discover -s tests
```

The tests replay the vendored examples in `../testdata/` over a local HTTP server and
never reach the network. `SMKLOG_LIVE=1` adds one live test that calls `status()` on
production; nothing in the suite ever requests a quote.

Docs, schemas, playground and status page: [smklog.com/developers](https://smklog.com/developers/).
The contract is the OpenAPI 3.1 document at
[quote-api.smklog.com/openapi.json](https://quote-api.smklog.com/openapi.json).

MIT license; see [LICENSE](LICENSE).
