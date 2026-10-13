/**
 * Type declarations for the SMKlog Quote API client (package "smklog").
 *
 * Models mirror testdata/schemas/*.schema.json field for field; property
 * names are snake_case exactly as they travel on the wire.
 */

/** SDK version, also the version in the User-Agent header. */
export const VERSION: string;

export interface SmklogClientOptions {
  /** API origin. Default "https://quote-api.smklog.com". */
  baseUrl?: string;
  /** Per-attempt timeout in milliseconds. Default 30000: a quote can take 10-20 s while carriers answer. */
  timeout?: number;
  /** Optional OAuth bearer for a dedicated rate-limit bucket; sent as "Authorization: Bearer ..." only when set. */
  token?: string;
  /** Appended after the SDK's own product token: "smklog-sdk-javascript/0.1.0 <userAgent>". */
  userAgent?: string;
}

export type CountryCode = 'US' | 'CA' | 'GB' | 'DE' | 'AU';
export type Carrier = 'USPS' | 'UPS' | 'FedEx' | 'DHL';
export type PackageConfidence = 'low' | 'medium' | 'high' | 'manual';
export type QuoteMode =
  | 'parcel_label_ready'
  | 'not_a_parcel'
  | 'multiple_boxes'
  | 'international_unsupported_online'
  | 'parcel_restricted'
  | 'review'
  | 'parcel_rates_unavailable';
export type CheckoutStage =
  | 'awaiting_checkout'
  | 'checkout_started'
  | 'paid'
  | 'label_ready'
  | 'delivered'
  | 'refunded';

/** One shipment to price (schema QuoteRequest). */
export interface QuoteRequest {
  /** Plain-words item description. Required unless product_url is given. */
  product?: string;
  /** Store product page URL; the item is identified from the page. Alternative to product. */
  product_url?: string;
  /** How many of the item ship together. The SDK sends 1 when omitted. */
  quantity?: number;
  /** 5-digit US ZIP; the origin is always in the United States. */
  from_postal_code: string;
  /** 5-digit US ZIP for US lanes, otherwise the destination country's own postal code. */
  to_postal_code: string;
  /** Destination country, default US. */
  to_country_code?: CountryCode;
  /** Packed box length, cm. Send all four measurements or none. */
  length_cm?: number;
  /** Packed box width, cm. */
  width_cm?: number;
  /** Packed box height, cm. */
  height_cm?: number;
  /** Packed weight, kg. */
  weight_kg?: number;
  /** Set to "manual" when supplying exact dimensions. */
  package_source?: 'manual';
}

/** The packed box used for pricing (schema Package). */
export interface Package {
  quantity?: number;
  length_cm?: number;
  width_cm?: number;
  height_cm?: number;
  weight_kg?: number;
  description?: string;
  /** "manual" when the caller supplied the measurements. */
  confidence?: PackageConfidence;
  assumptions?: string[];
}

/** One purchasable service (schema Rate). */
export interface Rate {
  carrier: Carrier;
  /** Service display name, e.g. "Ground Advantage". */
  service: string;
  /** Carrier service code, stable across calls. */
  service_code?: string;
  /** Checkout total, USD, SMKlog fee inside. */
  amount: number;
  /** Carrier retail counter price, USD; 0 when not published. */
  retail_amount?: number;
  delivery_days?: number | null;
  /** e.g. "2-5 business days". */
  delivery_window?: string | null;
  /** ISO date the carrier estimates. */
  delivery_estimated_date?: string | null;
  currency: 'USD';
}

/** The wire fields of a quote answer (schema QuoteResponse, additionalProperties true). */
export interface QuoteResponseData {
  /** Only parcel_label_ready carries rates. */
  mode: QuoteMode;
  identified_product_title?: string;
  /** Handle for createPaymentSession, valid 15 minutes; null when nothing was priced or the handle could not be stored, absent on answers without rates. */
  quote_id?: string | null;
  package?: Package;
  /** Every box the estimate produced; one entry when the shipment is one parcel. */
  packages?: Package[];
  /** Up to five purchasable services, best value first. */
  rates: Rate[];
  dangerous_goods?: Record<string, unknown> | null;
  /** Product pages the estimate read, when a search informed it. */
  sources?: Array<{ title?: string; url?: string }>;
  /** Human-readable reason when mode is not parcel_label_ready. */
  notice?: string | null;
  source?: { package?: string; rates?: string; [key: string]: unknown };
  [key: string]: unknown;
}

/**
 * What getParcelQuote resolves to: the wire fields as own properties (so
 * JSON.stringify gives the raw answer) plus two helpers on the prototype.
 */
export interface QuoteResponse extends QuoteResponseData {}
export class QuoteResponse {
  constructor(body: QuoteResponseData);
  /** The rate with the lowest amount, or null when there are no rates. */
  cheapest(): Rate | null;
  /** True when mode is parcel_label_ready and at least one rate came back. */
  isPriced(): boolean;
}

/** The shipment to open a checkout for (schema CheckoutSessionRequest). */
export interface CheckoutSessionRequest {
  /** The quote_id a getParcelQuote answer carries, valid 15 minutes: the session reuses that quote's rates and the shipment fields are ignored. An expired handle makes the API price the shipment fields live, so send them alongside an old id when you have them. */
  quote_id?: string;
  /** Required unless quote_id is given. */
  product?: string;
  /** 5-digit US ZIP. Required unless quote_id is given. */
  from_zip?: string;
  /** 5-digit US ZIP, or the destination country's own postal code. Required unless quote_id is given. */
  to_zip?: string;
  to_country?: CountryCode;
  /** The SDK sends 1 when omitted and no quote_id is given. */
  quantity?: number;
  /** Service to anchor the amount to, matched as a case-insensitive fragment; cheapest when omitted. */
  service?: string;
}

/** A payment session, not a charge (schema CheckoutSession). */
export interface CheckoutSession {
  intent: 'session';
  /** Handle for getCheckoutStatus, good for 30 days; null only when the session record could not be stored. */
  session_id?: string | null;
  method?: string[];
  /** Live-quoted checkout total for the anchored service, USD. */
  amount: number;
  currency: 'USD';
  service?: string;
  /** Opens the SMKlog checkout prefilled with this shipment. */
  handoff_url: string;
  identified_product_title?: string;
  /** True when the amount came from a quote_id, so no carrier call was spent. */
  quote_reused?: boolean;
  note?: string;
}

/** The checkout a human started, nested in CheckoutStatus. */
export interface CheckoutStatusCheckout {
  /** SMKlog order id, the reference for support. */
  order_id?: string;
  started_at?: string;
  paid?: boolean;
  label_ready?: boolean;
  refunded?: boolean;
  carrier?: string | null;
  /** The service the human actually chose. */
  service?: string | null;
  /** Present from label_ready on. */
  tracking_number?: string | null;
  tracking_status?: string | null;
  /** ISO date when the carrier gives one. */
  estimated_delivery?: string | null;
  delivered_at?: string | null;
}

/** Where a checkout stands (schema CheckoutStatus). Never carries names, addresses or emails. */
export interface CheckoutStatus {
  session_id: string;
  status: CheckoutStage;
  created_at?: string;
  expires_at?: string;
  amount?: number;
  currency?: 'USD';
  service?: string;
  quote_reused?: boolean;
  /** Null while awaiting_checkout. */
  checkout?: CheckoutStatusCheckout | null;
  /** What, if anything, the agent should do now. */
  next?: string;
}

/** The liveness answer (schema Liveness). */
export interface Liveness {
  ok: true;
  service: 'smklog-quote-api';
  /** Product version shared by the OpenAPI, the MCP server and the agent card. */
  version: string;
  /** Server time, ISO 8601. */
  time: string;
  last_estimate_race?: Record<string, unknown> | null;
}

/** Every error answer on the host (schema Problem). */
export interface Problem {
  /** Stable code, e.g. rate_limited, missing_required_fields, session_not_found. */
  error: string;
  message?: string;
  /** Field names still needed. */
  missing?: string[];
}

export interface SmklogErrorOptions {
  status?: number;
  error?: string;
  missing?: string[];
  body?: unknown;
  cause?: unknown;
}

/** Base error: every failure the client raises, local or from the API. */
export class SmklogError extends Error {
  /** HTTP status; 0 when no response arrived (network failure, timeout) or the request was rejected locally. */
  readonly status: number;
  /** Stable code: the API's own (rate_limited, session_not_found, ...), network_error, http_error, invalid_response, or invalid_request / missing_required_fields for local validation. A 429 without a Problem body reads rate_limited. */
  readonly error: string;
  /** Field names still needed; empty unless the code says otherwise. */
  readonly missing: string[];
  /** The parsed Problem, the raw body text when it was not JSON, or null. */
  readonly body: unknown;
  /** The native error behind a network_error, else undefined. Declared here so it exists whatever lib the consumer targets. */
  readonly cause?: unknown;
  constructor(message: string, options?: SmklogErrorOptions);
}

/** Every 429: rate_limited, slow_down, or an edge rate limit without a Problem body. The SDK never retries it; the caller decides. */
export class RateLimitedError extends SmklogError {
  /** Seconds from the Retry-After header when the API sent one, else null. */
  readonly retryAfter: number | null;
  constructor(message: string, options?: SmklogErrorOptions & { retryAfter?: number | null });
}

/** product_not_recognized, product_url_not_detected, missing_required_fields, invalid_request, invalid_json, and local validation failures (status 0). */
export class InvalidRequestError extends SmklogError {}

/** session_not_found. */
export class NotFoundError extends SmklogError {}

/** session_failed: the API could not open a checkout for this shipment. */
export class SessionFailedError extends SmklogError {}

export class SmklogClient {
  readonly baseUrl: string;
  readonly timeout: number;
  readonly token: string | null;
  /** The full User-Agent sent, SDK product token first. */
  readonly userAgent: string;
  constructor(options?: SmklogClientOptions);
  /** POST /quote. Spends a live carrier rating; never retried. */
  getParcelQuote(request: QuoteRequest): Promise<QuoteResponse>;
  /** POST /agent/checkout-session. Opens a checkout for a human; nothing is charged. Never retried. */
  createPaymentSession(request: CheckoutSessionRequest): Promise<CheckoutSession>;
  /** GET /agent/checkout-session/{session_id}. Retried up to twice on network failure or 5xx. */
  getCheckoutStatus(sessionId: string): Promise<CheckoutStatus>;
  /** GET /status. Retried up to twice on network failure or 5xx. */
  status(): Promise<Liveness>;
}
