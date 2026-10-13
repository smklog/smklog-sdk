// Replays testdata/samples/*.json from a local HTTP server. Nothing here
// touches the network except the one live test at the bottom, which runs only
// with SMKLOG_LIVE=1 and calls status() alone: a quote spends real money.
import { describe, test, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { readFileSync } from 'node:fs';

import {
  SmklogClient,
  QuoteResponse,
  SmklogError,
  RateLimitedError,
  InvalidRequestError,
  NotFoundError,
  SessionFailedError,
  VERSION,
} from '../index.js';

// --- Samples --------------------------------------------------------------

function sample(name) {
  return JSON.parse(readFileSync(new URL(`../../testdata/samples/${name}.json`, import.meta.url), 'utf8'));
}

function schema(name) {
  return JSON.parse(readFileSync(new URL(`../../testdata/schemas/${name}.schema.json`, import.meta.url), 'utf8'));
}

const quoteResponseSchema = schema('quote-response');

const quoteRequests = sample('get-parcel-quote-request');
const quoteResponses = sample('get-parcel-quote-responses');
const sessionRequests = sample('create-payment-session-request');
const sessionResponses = sample('create-payment-session-responses');
const statusResponses = sample('get-checkout-status-responses');
const livenessResponses = sample('status-responses');

function example(file, name) {
  const found = file.examples.find((entry) => entry.name === name);
  assert.ok(found, `${file.operationId} has no request example "${name}"`);
  return found.value;
}

function response(file, status, name = 'default') {
  const found = (file.responses[String(status)] || []).find((entry) => entry.name === name);
  assert.ok(found, `${file.operationId} has no ${status} response example "${name}"`);
  return found.value;
}

// --- Replay server --------------------------------------------------------

// Each test queues the answers it wants; the last one repeats so a retry loop
// can be observed through `calls` without the server running dry.
async function startReplay() {
  const calls = [];
  let queue = [];
  const server = createServer(async (req, res) => {
    let raw = '';
    for await (const chunk of req) raw += chunk;
    calls.push({ method: req.method, url: req.url, headers: req.headers, body: raw === '' ? null : JSON.parse(raw) });
    const next = queue.length > 1 ? queue.shift() : queue[0];
    if (next.delayMs) await new Promise((resolve) => setTimeout(resolve, next.delayMs));
    res.writeHead(next.status, { 'content-type': next.contentType || 'application/json', ...(next.headers || {}) });
    res.end(typeof next.body === 'string' ? next.body : JSON.stringify(next.body));
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  return {
    baseUrl: `http://127.0.0.1:${server.address().port}`,
    calls,
    reply(...answers) {
      queue = answers;
      calls.length = 0;
    },
    close() {
      server.closeAllConnections();
      return new Promise((resolve) => server.close(resolve));
    },
  };
}

const replay = await startReplay();
after(() => replay.close());

function client(options = {}) {
  return new SmklogClient({ baseUrl: replay.baseUrl, ...options });
}

const QUOTE_OK = response(quoteResponses, 200, 'priced');
const LIVENESS_OK = response(livenessResponses, 200);

// --- Round trips ----------------------------------------------------------

describe('getParcelQuote', () => {
  test('sends the "described" example verbatim and returns the priced answer', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));

    assert.equal(replay.calls.length, 1);
    assert.equal(replay.calls[0].method, 'POST');
    assert.equal(replay.calls[0].url, quoteRequests.path);
    assert.deepEqual(replay.calls[0].body, example(quoteRequests, 'described'));

    assert.ok(quote instanceof QuoteResponse);
    assert.deepEqual({ ...quote }, QUOTE_OK);
    assert.equal(JSON.stringify(quote), JSON.stringify(QUOTE_OK));
    assert.equal(quote.isPriced(), true);
    assert.deepEqual(quote.cheapest(), QUOTE_OK.rates[0]);
    assert.equal(quote.cheapest().amount, 38.42);
  });

  test('sends the "measured" example verbatim', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    await client().getParcelQuote(example(quoteRequests, 'measured'));
    assert.deepEqual(replay.calls[0].body, example(quoteRequests, 'measured'));
  });

  test('sends quantity 1 when the caller leaves it out', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    const { quantity, ...withoutQuantity } = example(quoteRequests, 'described');
    await client().getParcelQuote(withoutQuantity);
    assert.equal(replay.calls[0].body.quantity, 1);
  });

  test('rates stay in API order; cheapest() picks the lowest amount wherever it sits', async () => {
    const reordered = { ...QUOTE_OK, rates: [...QUOTE_OK.rates].reverse() };
    replay.reply({ status: 200, body: reordered });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.deepEqual(quote.rates, reordered.rates);
    assert.equal(quote.cheapest().carrier, 'USPS');
  });

  test('"notAParcel" answer: isPriced() false, cheapest() null, notice kept', async () => {
    const notAParcel = response(quoteResponses, 200, 'notAParcel');
    replay.reply({ status: 200, body: notAParcel });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.equal(quote.mode, 'not_a_parcel');
    assert.equal(quote.isPriced(), false);
    assert.equal(quote.cheapest(), null);
    assert.equal(quote.notice, notAParcel.notice);
    assert.deepEqual({ ...quote }, notAParcel);
  });

  test('unknown fields are preserved', async () => {
    replay.reply({ status: 200, body: { ...QUOTE_OK, added_next_year: { nested: true } } });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.deepEqual(quote.added_next_year, { nested: true });
  });
});

// The priced answer carries a handle that createPaymentSession accepts for
// 15 minutes; the vendored "fromQuote" request example passes that same
// handle, so the two samples pin the round trip end to end.
describe('quote_id', () => {
  const QUOTE_ID = new RegExp(quoteResponseSchema.properties.quote_id.pattern);

  test('the priced sample exposes quote_id in the shape the contract declares', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.equal(quote.quote_id, QUOTE_OK.quote_id);
    assert.match(quote.quote_id, QUOTE_ID);
    assert.equal(quote.quote_id, example(sessionRequests, 'fromQuote').quote_id, 'the samples share one handle');
  });

  test('createPaymentSession sends the answer\'s quote_id verbatim and the session reports the reuse', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.ok(quote.isPriced() && quote.quote_id);

    const fromQuote = example(sessionRequests, 'fromQuote');
    const expected = response(sessionResponses, 200);
    replay.reply({ status: 200, body: expected });
    const session = await client().createPaymentSession({ quote_id: quote.quote_id, service: fromQuote.service });

    assert.equal(replay.calls.length, 1);
    assert.equal(replay.calls[0].url, sessionRequests.path);
    assert.deepEqual(replay.calls[0].body, fromQuote);
    assert.equal(replay.calls[0].body.quote_id, 'q_8f3c2e1a4b7d');
    assert.equal(replay.calls[0].body.quantity, undefined, 'no quantity default rides along with a quote_id');
    assert.deepEqual(session, expected);
    assert.equal(session.quote_reused, true);
    assert.equal(session.session_id, expected.session_id);
  });

  test('a null quote_id (handle not stored) is kept as null on a still-priced answer', async () => {
    replay.reply({ status: 200, body: { ...QUOTE_OK, quote_id: null } });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.equal(quote.quote_id, null);
    assert.equal(quote.isPriced(), true);
  });

  test('the "notAParcel" sample carries no quote_id at all', async () => {
    replay.reply({ status: 200, body: response(quoteResponses, 200, 'notAParcel') });
    const quote = await client().getParcelQuote(example(quoteRequests, 'described'));
    assert.equal('quote_id' in quote, false);
    assert.equal(quote.quote_id, undefined);
  });

  test('shipment fields sent alongside a quote_id ride through untouched', async () => {
    replay.reply({ status: 200, body: response(sessionResponses, 200) });
    const shipment = example(sessionRequests, 'fromShipment');
    await client().createPaymentSession({ quote_id: 'q_8f3c2e1a4b7d', ...shipment });
    assert.deepEqual(replay.calls[0].body, { quote_id: 'q_8f3c2e1a4b7d', ...shipment });
  });
});

describe('createPaymentSession', () => {
  for (const name of ['fromQuote', 'fromShipment']) {
    test(`sends the "${name}" example verbatim and returns the session`, async () => {
      const expected = response(sessionResponses, 200);
      replay.reply({ status: 200, body: expected });
      const session = await client().createPaymentSession(example(sessionRequests, name));

      assert.equal(replay.calls[0].method, 'POST');
      assert.equal(replay.calls[0].url, sessionRequests.path);
      assert.deepEqual(replay.calls[0].body, example(sessionRequests, name));
      assert.deepEqual(session, expected);
      assert.equal(session.intent, 'session');
    });
  }
});

describe('getCheckoutStatus', () => {
  test('reads the session by id and returns the status', async () => {
    const expected = response(statusResponses, 200);
    replay.reply({ status: 200, body: expected });
    const status = await client().getCheckoutStatus(expected.session_id);

    assert.equal(replay.calls[0].method, 'GET');
    assert.equal(replay.calls[0].url, statusResponses.path.replace('{session_id}', expected.session_id));
    assert.equal(replay.calls[0].body, null);
    assert.deepEqual(status, expected);
    assert.equal(status.checkout.tracking_number, expected.checkout.tracking_number);
  });

  test('URL-encodes the id', async () => {
    replay.reply({ status: 200, body: response(statusResponses, 200) });
    await client().getCheckoutStatus('as_x/y?z');
    assert.equal(replay.calls[0].url, '/agent/checkout-session/as_x%2Fy%3Fz');
  });
});

describe('status', () => {
  test('returns the liveness answer', async () => {
    replay.reply({ status: 200, body: LIVENESS_OK });
    const liveness = await client().status();
    assert.equal(replay.calls[0].method, 'GET');
    assert.equal(replay.calls[0].url, livenessResponses.path);
    assert.deepEqual(liveness, LIVENESS_OK);
  });
});

// --- Headers --------------------------------------------------------------

describe('headers', () => {
  test('Accept, Content-Type on POST and the SDK User-Agent', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    await client().getParcelQuote(example(quoteRequests, 'described'));
    const { headers } = replay.calls[0];
    assert.equal(headers.accept, 'application/json');
    assert.equal(headers['content-type'], 'application/json');
    assert.equal(headers['user-agent'], `smklog-sdk-javascript/${VERSION}`);
    assert.equal(headers.authorization, undefined);
  });

  test('GET sends no Content-Type; userAgent suffix follows the SDK token', async () => {
    replay.reply({ status: 200, body: LIVENESS_OK });
    await client({ userAgent: 'acme-bot/2.1' }).status();
    const { headers } = replay.calls[0];
    assert.equal(headers['content-type'], undefined);
    assert.equal(headers['user-agent'], `smklog-sdk-javascript/${VERSION} acme-bot/2.1`);
  });

  test('token becomes a bearer Authorization header', async () => {
    replay.reply({ status: 200, body: LIVENESS_OK });
    await client({ token: 'opaque-token' }).status();
    assert.equal(replay.calls[0].headers.authorization, 'Bearer opaque-token');
  });

  test('a trailing slash on baseUrl does not double up the path', async () => {
    replay.reply({ status: 200, body: LIVENESS_OK });
    await new SmklogClient({ baseUrl: `${replay.baseUrl}/` }).status();
    assert.equal(replay.calls[0].url, '/status');
  });
});

// --- Documented errors ----------------------------------------------------

describe('error mapping', () => {
  const cases = [
    {
      name: 'quote 400 missing_required_fields -> InvalidRequestError with missing',
      file: quoteResponses, status: 400, type: InvalidRequestError,
      call: (c) => c.getParcelQuote(example(quoteRequests, 'described')),
    },
    {
      name: 'quote 422 product_url_not_detected -> InvalidRequestError',
      file: quoteResponses, status: 422, type: InvalidRequestError,
      call: (c) => c.getParcelQuote(example(quoteRequests, 'described')),
    },
    {
      name: 'quote 429 rate_limited -> RateLimitedError',
      file: quoteResponses, status: 429, type: RateLimitedError,
      call: (c) => c.getParcelQuote(example(quoteRequests, 'described')),
    },
    {
      name: 'session 400 invalid_request -> InvalidRequestError',
      file: sessionResponses, status: 400, type: InvalidRequestError,
      call: (c) => c.createPaymentSession(example(sessionRequests, 'fromQuote')),
    },
    {
      name: 'session 422 session_failed -> SessionFailedError',
      file: sessionResponses, status: 422, type: SessionFailedError,
      call: (c) => c.createPaymentSession(example(sessionRequests, 'fromShipment')),
    },
    {
      name: 'session 429 rate_limited -> RateLimitedError',
      file: sessionResponses, status: 429, type: RateLimitedError,
      call: (c) => c.createPaymentSession(example(sessionRequests, 'fromQuote')),
    },
    {
      name: 'checkout status 404 session_not_found -> NotFoundError',
      file: statusResponses, status: 404, type: NotFoundError,
      call: (c) => c.getCheckoutStatus('as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33'),
    },
    {
      name: 'checkout status 429 rate_limited -> RateLimitedError',
      file: statusResponses, status: 429, type: RateLimitedError,
      call: (c) => c.getCheckoutStatus('as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33'),
    },
  ];

  for (const { name, file, status, type, call } of cases) {
    test(name, async () => {
      const problem = response(file, status);
      replay.reply({ status, body: problem });
      const error = await call(client()).catch((caught) => caught);
      assert.ok(error instanceof type, `expected ${type.name}, got ${error && error.constructor.name}: ${error && error.message}`);
      assert.ok(error instanceof SmklogError);
      assert.equal(error.name, type.name);
      assert.equal(error.status, status);
      assert.equal(error.error, problem.error);
      assert.deepEqual(error.missing, problem.missing || []);
      assert.deepEqual(error.body, problem);
      if (problem.message) assert.equal(error.message, problem.message);
      assert.equal(replay.calls.length, 1, 'a documented refusal is never retried');
    });
  }

  test('a Problem without a message still says what was missing', async () => {
    const problem = response(quoteResponses, 400);
    assert.equal(problem.message, undefined, 'the vendored 400 sample carries no sentence');
    replay.reply({ status: 400, body: problem });
    const error = await client().getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.ok(error instanceof InvalidRequestError);
    assert.equal(error.message, 'missing_required_fields: missing to_postal_code');
  });

  test('a code outside the table is the base SmklogError', async () => {
    replay.reply({ status: 401, body: { error: 'invalid_token', message: 'Unknown bearer.' } });
    const error = await client({ token: 'stale' }).status().catch((caught) => caught);
    assert.equal(error.constructor, SmklogError);
    assert.equal(error.status, 401);
    assert.equal(error.error, 'invalid_token');
  });

  test('slow_down is also RateLimitedError, with Retry-After exposed', async () => {
    replay.reply({ status: 429, body: { error: 'slow_down', message: 'Slow down.' }, headers: { 'retry-after': '120' } });
    const error = await client().status().catch((caught) => caught);
    assert.ok(error instanceof RateLimitedError);
    assert.equal(error.retryAfter, 120);
    assert.equal(replay.calls.length, 1);
  });

  test('a 429 without a Problem body (edge rate limit) is still RateLimitedError, not retried', async () => {
    replay.reply({ status: 429, body: '<html>Too many requests</html>', contentType: 'text/html' });
    let error = await client().status().catch((caught) => caught);
    assert.ok(error instanceof RateLimitedError, `got ${error && error.constructor.name}`);
    assert.equal(error.error, 'rate_limited');
    assert.equal(error.status, 429);
    assert.equal(error.body, '<html>Too many requests</html>');
    assert.equal(replay.calls.length, 1);

    replay.reply({ status: 429, body: '', contentType: 'text/plain' });
    error = await client().getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.ok(error instanceof RateLimitedError);
    assert.equal(error.error, 'rate_limited');
    assert.equal(replay.calls.length, 1);
  });

  test('a non-JSON error body still raises SmklogError with the raw text', async () => {
    replay.reply({ status: 502, body: '<html>bad gateway</html>', contentType: 'text/html' });
    const error = await client().getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.equal(error.constructor, SmklogError);
    assert.equal(error.status, 502);
    assert.equal(error.error, 'http_error');
    assert.equal(error.body, '<html>bad gateway</html>');
  });
});

// --- Retry policy ---------------------------------------------------------

describe('retries', () => {
  test('429 is raised at once, no retry (POST and GET)', async () => {
    replay.reply({ status: 429, body: response(quoteResponses, 429) });
    await assert.rejects(client().getParcelQuote(example(quoteRequests, 'described')), RateLimitedError);
    assert.equal(replay.calls.length, 1);

    replay.reply({ status: 429, body: response(statusResponses, 429) });
    await assert.rejects(client().status(), RateLimitedError);
    assert.equal(replay.calls.length, 1);
  });

  test('a 503 on status() is retried and then succeeds', async () => {
    replay.reply({ status: 503, body: { error: 'upstream_unavailable' } }, { status: 200, body: LIVENESS_OK });
    const liveness = await client().status();
    assert.deepEqual(liveness, LIVENESS_OK);
    assert.equal(replay.calls.length, 2);
  });

  test('a 5xx on getCheckoutStatus is retried at most twice, then raised', async () => {
    replay.reply({ status: 500, body: { error: 'internal', message: 'boom' } });
    const error = await client().getCheckoutStatus('as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33').catch((caught) => caught);
    assert.ok(error instanceof SmklogError);
    assert.equal(error.status, 500);
    assert.equal(replay.calls.length, 3);
  });

  test('a 5xx on a POST is never retried', async () => {
    replay.reply({ status: 503, body: { error: 'upstream_unavailable' } });
    await assert.rejects(client().getParcelQuote(example(quoteRequests, 'described')), (error) => error.status === 503);
    assert.equal(replay.calls.length, 1);

    replay.reply({ status: 503, body: { error: 'upstream_unavailable' } });
    await assert.rejects(client().createPaymentSession(example(sessionRequests, 'fromQuote')), (error) => error.status === 503);
    assert.equal(replay.calls.length, 1);
  });
});

// --- Network and timeout --------------------------------------------------

describe('network', () => {
  test('a timeout aborts the request and raises network_error with status 0', async () => {
    replay.reply({ status: 200, body: QUOTE_OK, delayMs: 300 });
    const error = await client({ timeout: 50 }).getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.equal(error.constructor, SmklogError);
    assert.equal(error.status, 0);
    assert.equal(error.error, 'network_error');
    assert.match(error.message, /timed out after 50 ms/);
    assert.equal(error.cause && error.cause.name, 'AbortError');
    assert.equal(replay.calls.length, 1, 'a timed-out POST is not retried');
  });

  test('a refused connection raises network_error wrapping the native error', async () => {
    const unreachable = new SmklogClient({ baseUrl: 'http://127.0.0.1:1' });
    const error = await unreachable.getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.equal(error.constructor, SmklogError);
    assert.equal(error.status, 0);
    assert.equal(error.error, 'network_error');
    assert.ok(error.cause instanceof Error);
  });

  // A server that drops the socket mid-request stands in for a flaky network;
  // counting the requests it saw pins the attempt count, which a timing
  // assertion cannot (one slow retry or five fast ones would both pass).
  async function startFlaky(failures) {
    const seen = { requests: 0 };
    const server = createServer((req, res) => {
      seen.requests += 1;
      if (seen.requests <= failures) {
        req.socket.destroy();
        return;
      }
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify(LIVENESS_OK));
    });
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    seen.client = new SmklogClient({ baseUrl: `http://127.0.0.1:${server.address().port}` });
    seen.close = () => {
      server.closeAllConnections();
      return new Promise((resolve) => server.close(resolve));
    };
    return seen;
  }

  test('a network failure on status() is retried twice and the third attempt is used', async () => {
    const flaky = await startFlaky(2);
    try {
      const liveness = await flaky.client.status();
      assert.deepEqual(liveness, LIVENESS_OK);
      assert.equal(flaky.requests, 3, 'two dropped attempts and the one that answered');
    } finally {
      await flaky.close();
    }
  });

  test('a network failure on status() is raised after exactly three attempts', async () => {
    const flaky = await startFlaky(3);
    try {
      await assert.rejects(flaky.client.status(), (error) => error.error === 'network_error' && error.status === 0);
      assert.equal(flaky.requests, 3);
    } finally {
      await flaky.close();
    }
  });

  test('a 200 that is not JSON raises invalid_response', async () => {
    replay.reply({ status: 200, body: 'not json at all', contentType: 'text/plain' });
    const error = await client().getParcelQuote(example(quoteRequests, 'described')).catch((caught) => caught);
    assert.equal(error.error, 'invalid_response');
    assert.equal(error.status, 200);
  });
});

// --- Local validation -----------------------------------------------------

describe('validation (no request is sent)', () => {
  const described = example(quoteRequests, 'described');
  const measured = example(quoteRequests, 'measured');
  const shipment = example(sessionRequests, 'fromShipment');

  const cases = [
    ['4-digit origin ZIP', (c) => c.getParcelQuote({ ...described, from_postal_code: '0710' }), /from_postal_code must be a 5-digit US ZIP/],
    ['4-digit destination ZIP on a US lane', (c) => c.getParcelQuote({ ...described, to_postal_code: '3030' }), /to_postal_code must be a 5-digit US ZIP/],
    ['letters in the origin ZIP', (c) => c.getParcelQuote({ ...described, from_postal_code: 'SW1A 1AA' }), /from_postal_code/],
    ['country outside the five sold', (c) => c.getParcelQuote({ ...described, to_country_code: 'FR' }), /to_country_code must be one of US, CA, GB, DE, AU/],
    ['quantity 0', (c) => c.getParcelQuote({ ...described, quantity: 0 }), /quantity must be an integer of 1 or more/],
    ['fractional quantity', (c) => c.getParcelQuote({ ...described, quantity: 1.5 }), /quantity/],
    ['three of the four measurements', (c) => c.getParcelQuote({ ...measured, weight_kg: undefined }), /send all of length_cm, width_cm, height_cm, weight_kg or none/],
    ['a negative measurement', (c) => c.getParcelQuote({ ...measured, height_cm: -1 }), /height_cm must be a positive number/],
    ['neither product nor product_url', (c) => c.getParcelQuote({ ...described, product: undefined }), /product or product_url required/],
    ['missing to_postal_code', (c) => c.getParcelQuote({ ...described, to_postal_code: undefined }), /to_postal_code required/],
    ['not an object', (c) => c.getParcelQuote('guitar'), /must be an object/],
    ['session: 4-digit from_zip', (c) => c.createPaymentSession({ ...shipment, from_zip: '0710' }), /from_zip must be a 5-digit US ZIP/],
    ['session: no quote_id and no product', (c) => c.createPaymentSession({ from_zip: '07102', to_zip: '30303' }), /product required when quote_id is not given/],
    ['session: empty quote_id', (c) => c.createPaymentSession({ quote_id: '' }), /quote_id must be a non-empty string/],
    ['checkout status: empty id', (c) => c.getCheckoutStatus(''), /session_id must be a non-empty string/],
  ];

  for (const [name, call, pattern] of cases) {
    test(name, async () => {
      replay.reply({ status: 200, body: QUOTE_OK });
      const error = await call(client()).catch((caught) => caught);
      assert.ok(error instanceof InvalidRequestError, `expected InvalidRequestError, got ${error && error.constructor.name}: ${error && error.message}`);
      assert.equal(error.status, 0);
      assert.match(error.message, pattern);
      assert.equal(replay.calls.length, 0, 'validation must reject before any request');
    });
  }

  test('missing fields are listed the way the API lists them', async () => {
    const error = await client().getParcelQuote({ product: 'books', from_postal_code: '07102' }).catch((caught) => caught);
    assert.equal(error.error, 'missing_required_fields');
    assert.deepEqual(error.missing, ['to_postal_code']);
  });

  test('an international destination takes the country\'s own postal code', async () => {
    replay.reply({ status: 200, body: QUOTE_OK });
    await client().getParcelQuote({ ...described, to_postal_code: 'SW1A 1AA', to_country_code: 'GB' });
    assert.equal(replay.calls[0].body.to_postal_code, 'SW1A 1AA');
  });

  test('a quote_id skips shipment validation and is sent as given', async () => {
    replay.reply({ status: 200, body: response(sessionResponses, 200) });
    await client().createPaymentSession({ quote_id: 'q_8f3c2e1a4b7d' });
    assert.deepEqual(replay.calls[0].body, { quote_id: 'q_8f3c2e1a4b7d' });
  });
});

// --- Live (opt-in) --------------------------------------------------------

// Only status(): it is free and read-only. A quote costs SMKlog a carrier call.
test('live: status() answers from production', { skip: process.env.SMKLOG_LIVE !== '1' && 'set SMKLOG_LIVE=1' }, async () => {
  const liveness = await new SmklogClient().status();
  assert.equal(liveness.ok, true);
  assert.equal(liveness.service, 'smklog-quote-api');
  assert.match(liveness.version, /^\d+\.\d+\.\d+$/);
});
