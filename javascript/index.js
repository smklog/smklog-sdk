// SMKlog Quote API client. Thin by design: four operations, validation that
// saves a wasted request, and errors typed by the API's own error codes.
// Wire field names stay snake_case exactly as the API sends them.

export const VERSION = '0.1.0';

const DEFAULT_BASE_URL = 'https://quote-api.smklog.com';
// Carriers answer in 10-20 s on a cold lane, so the default is generous.
const DEFAULT_TIMEOUT_MS = 30_000;
// Retry only the idempotent GETs, and only a little: a status read that
// fails three times in a row is an outage the caller should see.
const MAX_GET_ATTEMPTS = 3;
const BACKOFF_MS = 200;

const COUNTRIES = new Set(['US', 'CA', 'GB', 'DE', 'AU']);
const US_ZIP = /^[0-9]{5}$/;
const MEASUREMENTS = ['length_cm', 'width_cm', 'height_cm', 'weight_kg'];

// --- Errors ---------------------------------------------------------------

export class SmklogError extends Error {
  constructor(message, { status = 0, error = 'network_error', missing = [], body = null, cause } = {}) {
    super(message, cause === undefined ? undefined : { cause });
    this.name = this.constructor.name;
    this.status = status;
    this.error = error;
    this.missing = Array.isArray(missing) ? missing : [];
    this.body = body;
  }
}

export class RateLimitedError extends SmklogError {
  constructor(message, options = {}) {
    super(message, options);
    // The API does not promise a Retry-After header; expose it when present so
    // the caller (never the SDK) can decide when to come back.
    this.retryAfter = options.retryAfter ?? null;
  }
}

export class InvalidRequestError extends SmklogError {}

export class NotFoundError extends SmklogError {}

export class SessionFailedError extends SmklogError {}

const ERROR_CLASS_BY_CODE = {
  rate_limited: RateLimitedError,
  slow_down: RateLimitedError,
  product_not_recognized: InvalidRequestError,
  product_url_not_detected: InvalidRequestError,
  missing_required_fields: InvalidRequestError,
  invalid_request: InvalidRequestError,
  invalid_json: InvalidRequestError,
  session_not_found: NotFoundError,
  session_failed: SessionFailedError,
};

function errorFromResponse(response, problem, rawText) {
  const isProblem = problem && typeof problem === 'object' && typeof problem.error === 'string';
  // An edge rate limit answers 429 with an HTML or empty body instead of the
  // worker's Problem; it still must not be retried, so the status alone
  // decides the class and the code when the body names none.
  const rateLimited = response.status === 429;
  const error = isProblem ? problem.error : rateLimited ? 'rate_limited' : 'http_error';
  const missing = isProblem && Array.isArray(problem.missing) ? problem.missing : [];
  // A Problem may carry no sentence (missing_required_fields lists fields
  // instead), and a log line of error.message should still say what went wrong.
  const message = (isProblem && problem.message)
    || (isProblem ? `${error}${missing.length ? ': missing ' + missing.join(', ') : ''}` : `HTTP ${response.status} from ${response.url}`);
  const ErrorClass = rateLimited ? RateLimitedError : ERROR_CLASS_BY_CODE[error] || SmklogError;
  const retryAfterHeader = Number(response.headers.get('retry-after'));
  return new ErrorClass(message, {
    status: response.status,
    error,
    missing,
    body: isProblem ? problem : rawText,
    retryAfter: Number.isFinite(retryAfterHeader) && retryAfterHeader > 0 ? retryAfterHeader : null,
  });
}

// Local validation failures reuse the API's own codes so one catch handles
// both; status 0 tells them apart from a refusal the server actually sent.
function invalid(message, missing = []) {
  return new InvalidRequestError(message, {
    status: 0,
    error: missing.length ? 'missing_required_fields' : 'invalid_request',
    missing,
  });
}

// --- Models ---------------------------------------------------------------

// The quote body plus two conveniences. Own properties are exactly the wire
// fields (unknown ones included), so JSON.stringify(quote) is the raw answer.
export class QuoteResponse {
  constructor(body) {
    Object.assign(this, body);
  }

  isPriced() {
    return this.mode === 'parcel_label_ready' && Array.isArray(this.rates) && this.rates.length > 0;
  }

  cheapest() {
    let best = null;
    for (const rate of Array.isArray(this.rates) ? this.rates : []) {
      if (typeof rate.amount === 'number' && (best === null || rate.amount < best.amount)) best = rate;
    }
    return best;
  }
}

// --- Validation -----------------------------------------------------------

function requireObject(request, name) {
  if (!request || typeof request !== 'object' || Array.isArray(request)) {
    throw invalid(`${name} must be an object`);
  }
}

// The origin is always a US ZIP; the destination is one only for US lanes.
function validateRoute(request, fromKey, toKey, countryKey) {
  const missing = [fromKey, toKey].filter((key) => request[key] === undefined || request[key] === null || request[key] === '');
  if (missing.length) throw invalid(`${missing.join(', ')} required`, missing);

  const country = request[countryKey];
  if (country !== undefined && !COUNTRIES.has(country)) {
    throw invalid(`${countryKey} must be one of ${[...COUNTRIES].join(', ')} (got ${JSON.stringify(country)})`);
  }
  if (!US_ZIP.test(String(request[fromKey]))) {
    throw invalid(`${fromKey} must be a 5-digit US ZIP (got ${JSON.stringify(request[fromKey])})`);
  }
  const to = request[toKey];
  if (country === undefined || country === 'US') {
    if (!US_ZIP.test(String(to))) throw invalid(`${toKey} must be a 5-digit US ZIP (got ${JSON.stringify(to)})`);
  } else if (typeof to !== 'string' || to.length > 12) {
    throw invalid(`${toKey} must be the destination postal code, up to 12 characters (got ${JSON.stringify(to)})`);
  }

  const quantity = request.quantity;
  if (quantity !== undefined && (!Number.isInteger(quantity) || quantity < 1)) {
    throw invalid(`quantity must be an integer of 1 or more (got ${JSON.stringify(quantity)})`);
  }
}

function validateQuoteRequest(request) {
  requireObject(request, 'quote request');
  if (!request.product && !request.product_url) throw invalid('product or product_url required', ['product']);
  validateRoute(request, 'from_postal_code', 'to_postal_code', 'to_country_code');

  // An exact box needs all four numbers; a partial set would silently fall
  // back to the estimate and price a different parcel than the caller meant.
  const given = MEASUREMENTS.filter((key) => request[key] !== undefined && request[key] !== null);
  if (given.length !== 0 && given.length !== MEASUREMENTS.length) {
    throw invalid(`send all of ${MEASUREMENTS.join(', ')} or none of them (got ${given.join(', ')})`);
  }
  for (const key of given) {
    if (typeof request[key] !== 'number' || !Number.isFinite(request[key]) || request[key] <= 0) {
      throw invalid(`${key} must be a positive number (got ${JSON.stringify(request[key])})`);
    }
  }
}

function validateCheckoutSessionRequest(request) {
  requireObject(request, 'checkout session request');
  if (request.quote_id !== undefined && request.quote_id !== null) {
    if (typeof request.quote_id !== 'string' || request.quote_id === '') throw invalid('quote_id must be a non-empty string');
    return;
  }
  if (!request.product) throw invalid('product required when quote_id is not given', ['product']);
  validateRoute(request, 'from_zip', 'to_zip', 'to_country');
}

// --- Client ---------------------------------------------------------------

export class SmklogClient {
  constructor({ baseUrl = DEFAULT_BASE_URL, timeout = DEFAULT_TIMEOUT_MS, token, userAgent } = {}) {
    this.baseUrl = String(baseUrl).replace(/\/+$/, '');
    this.timeout = timeout;
    this.token = token || null;
    this.userAgent = userAgent ? `smklog-sdk-javascript/${VERSION} ${userAgent}` : `smklog-sdk-javascript/${VERSION}`;
  }

  async getParcelQuote(request) {
    validateQuoteRequest(request);
    // quantity is required on the wire with a documented default of 1.
    const body = await this.#request('POST', '/quote', { quantity: 1, ...request });
    return new QuoteResponse(body);
  }

  async createPaymentSession(request) {
    validateCheckoutSessionRequest(request);
    // A quote_id names the shipment, so no quantity default is added; shipment
    // fields sent alongside it ride through untouched for the case where the
    // handle has expired and the API prices them live instead.
    const body = request.quote_id ? { ...request } : { quantity: 1, ...request };
    return this.#request('POST', '/agent/checkout-session', body);
  }

  async getCheckoutStatus(sessionId) {
    if (typeof sessionId !== 'string' || sessionId === '') throw invalid('session_id must be a non-empty string', ['session_id']);
    return this.#request('GET', `/agent/checkout-session/${encodeURIComponent(sessionId)}`);
  }

  async status() {
    return this.#request('GET', '/status');
  }

  async #request(method, path, body) {
    const headers = { Accept: 'application/json', 'User-Agent': this.userAgent };
    if (this.token) headers.Authorization = `Bearer ${this.token}`;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const init = { method, headers, body: body === undefined ? undefined : JSON.stringify(body) };

    // A POST is never retried: /quote spends a live carrier rating and
    // /agent/checkout-session opens a session, so a repeat is not free.
    const attempts = method === 'GET' ? MAX_GET_ATTEMPTS : 1;
    for (let attempt = 1; ; attempt++) {
      let response;
      let text;
      try {
        ({ response, text } = await this.#send(this.baseUrl + path, init));
      } catch (error) {
        if (attempt < attempts) {
          await sleep(BACKOFF_MS * attempt);
          continue;
        }
        throw error;
      }
      if (response.status >= 500 && attempt < attempts) {
        await sleep(BACKOFF_MS * attempt);
        continue;
      }
      return parseBody(response, text);
    }
  }

  async #send(url, init) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeout);
    try {
      const response = await fetch(url, { ...init, signal: controller.signal });
      const text = await response.text();
      return { response, text };
    } catch (cause) {
      const timedOut = cause && cause.name === 'AbortError';
      const detail = cause && cause.cause && cause.cause.message ? cause.cause.message : cause && cause.message;
      throw new SmklogError(
        timedOut ? `Request to ${url} timed out after ${this.timeout} ms` : `Request to ${url} failed: ${detail}`,
        { status: 0, error: 'network_error', cause },
      );
    } finally {
      clearTimeout(timer);
    }
  }
}

function parseBody(response, text) {
  let parsed = null;
  let parsable = text === '';
  if (text !== '') {
    try {
      parsed = JSON.parse(text);
      parsable = true;
    } catch {
      parsable = false;
    }
  }
  if (!response.ok) throw errorFromResponse(response, parsable ? parsed : null, text);
  if (!parsable) {
    throw new SmklogError(`HTTP ${response.status} from ${response.url} is not JSON`, {
      status: response.status,
      error: 'invalid_response',
      body: text,
    });
  }
  return parsed;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
