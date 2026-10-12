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
item = "Yamaha FG800 acoustic guitar in a hard case"
quote = client.get_parcel_quote(product=item, from_postal_code="07102", to_postal_code="30303")
for rate in quote.rates:                       # best value first, as the API returns them
    print(rate.carrier, rate.service, rate.amount, "counter", rate.retail_amount)
if quote.is_priced():
    session = client.create_payment_session(product=item, from_zip="07102", to_zip="30303",
                                            service=quote.cheapest().service)
    print(session.handoff_url)                 # the human confirms and pays here
    print(client.get_checkout_status(session.session_id).status)
```

Exact measurements skip the box estimate: pass all four of `length_cm`, `width_cm`,
`height_cm`, `weight_kg` (the client marks the package `manual`). A request can also be a
`QuoteRequest` / `CheckoutSessionRequest` model or a plain dict; fields keep the wire names.

Every `get_parcel_quote` call spends a live carrier rating on the server: one call per
shipment, cache on your side, never poll. `create_payment_session` takes either a
`quote_id` (preferred when you hold one, for example from the MCP tool: the quote is
reused and no second carrier call is spent) or the shipment itself, as above; the client
never turns one into the other.

## Client options

| Option | Default | Meaning |
| --- | --- | --- |
| `base_url` | `https://quote-api.smklog.com` | The API host. |
| `timeout` | `30.0` | Seconds per request; a quote can take 10-20 s while carriers answer. |
| `token` | `None` | Optional OAuth bearer for a dedicated rate-limit bucket ([auth.md](https://smklog.com/auth.md)); sent as `Authorization: Bearer ...` only when set. |
| `user_agent` | `None` | Suffix appended after `smklog-sdk-python/0.1.0`. |

Operations: `get_parcel_quote(...)` -> `QuoteResponse` (with `rates`, `cheapest()`,
`is_priced()`), `create_payment_session(...)` -> `CheckoutSession`,
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
(field names, often empty) and `body` (the response body as received).

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

Human output is a small table; `--json` prints the raw answer. Exit code 0 on a priced
answer or a successful read, 2 when the API refuses (the Problem is printed) or answers a
quote without a price, 3 on a rate limit, 1 when the host could not be reached.
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

MIT (c) SMKLOG-2022 LLC.
