package smklog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version, sent as smklog-sdk-go/<Version> in User-Agent.
// It is independent of the API's product version.
const Version = "0.1.0"

// DefaultBaseURL is the production server; there is no sandbox host.
const DefaultBaseURL = "https://quote-api.smklog.com"

// DefaultTimeout bounds each attempt. A quote can take 10-20 s while carriers
// answer, so the default is generous.
const DefaultTimeout = 30 * time.Second

const (
	defaultCountry      = "US"
	packageSourceManual = "manual"
)

// Countries the API sells to online, for the local check on the destination.
var countries = []string{"US", "CA", "GB", "DE", "AU"}

var usZip = regexp.MustCompile(`^[0-9]{5}$`)

// One pause per retry, GETs only. POST /quote spends a live carrier rating
// and POST /agent/checkout-session opens a session, so the client never
// repeats either; a status read that fails three times in a row is an outage
// the caller should see.
var retryBackoff = []time.Duration{200 * time.Millisecond, 400 * time.Millisecond}

// Client talks to the SMKlog Quote API. Construct it with New; the zero value
// is not usable. A Client is safe for concurrent use.
type Client struct {
	baseURL    string
	timeout    time.Duration
	token      string
	userAgent  string
	httpClient *http.Client
}

// Option configures a Client in New.
type Option func(*Client)

// WithBaseURL points the client at another host (a test server, a proxy).
// The default is DefaultBaseURL.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithTimeout bounds each attempt; the caller's context bounds the whole
// call. The default is DefaultTimeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.timeout = timeout }
}

// WithToken sends an OAuth bearer token, which buys the client its own
// rate-limit bucket (see https://smklog.com/auth.md). No key is needed
// otherwise, and nothing is sent when the token is empty.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithUserAgent appends a product token after the SDK's own, for example
// "acme-bot/2.1" gives "smklog-sdk-go/0.1.0 acme-bot/2.1".
func WithUserAgent(suffix string) Option {
	return func(c *Client) {
		if suffix = strings.TrimSpace(suffix); suffix != "" {
			c.userAgent = "smklog-sdk-go/" + Version + " " + suffix
		}
	}
}

// WithHTTPClient swaps the transport, for a custom proxy or TLS setup. Leave
// its Timeout at zero: the per-attempt deadline comes from WithTimeout.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// New returns a client for the production API, adjusted by the options.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL:    DefaultBaseURL,
		timeout:    DefaultTimeout,
		userAgent:  "smklog-sdk-go/" + Version,
		httpClient: &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// BaseURL is the host the client calls, without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// GetParcelQuote prices one parcel (POST /quote). It spends a live carrier
// rating on the server: one call per shipment, never a poll. The request is
// checked locally first; a refusal has Status 0 and IsInvalidRequest true.
func (c *Client) GetParcelQuote(ctx context.Context, req QuoteRequest) (*QuoteResponse, error) {
	if err := prepareQuote(&req); err != nil {
		return nil, err
	}
	var out QuoteResponse
	if err := c.do(ctx, http.MethodPost, "/quote", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreatePaymentSession opens a checkout for a human (POST /agent/checkout-session)
// from a QuoteID (preferred: the quote is reused, no second carrier call) or
// from the shipment itself. Nothing is charged: the human pays on the
// returned HandoffURL.
func (c *Client) CreatePaymentSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSession, error) {
	if err := prepareSession(&req); err != nil {
		return nil, err
	}
	var out CheckoutSession
	if err := c.do(ctx, http.MethodPost, "/agent/checkout-session", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCheckoutStatus reads where a checkout stands
// (GET /agent/checkout-session/{session_id}). Sessions answer for 30 days.
func (c *Client) GetCheckoutStatus(ctx context.Context, sessionID string) (*CheckoutStatus, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, invalid("session_id must be a non-empty string", "session_id")
	}
	var out CheckoutStatus
	if err := c.do(ctx, http.MethodGet, "/agent/checkout-session/"+url.PathEscape(sessionID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status is the liveness answer (GET /status): the service is up and which
// version is serving.
func (c *Client) Status(ctx context.Context) (*Liveness, error) {
	var out Liveness
	if err := c.do(ctx, http.MethodGet, "/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Validation ------------------------------------------------------------

// Local checks that save a request the server would refuse anyway.
func prepareQuote(r *QuoteRequest) error {
	var missing []string
	if r.Product == "" && r.ProductURL == "" {
		missing = append(missing, "product")
	}
	if r.FromPostalCode == "" {
		missing = append(missing, "from_postal_code")
	}
	if r.ToPostalCode == "" {
		missing = append(missing, "to_postal_code")
	}
	if len(missing) > 0 {
		return invalid("required: "+strings.Join(missing, ", "), missing...)
	}
	if err := checkRoute("from_postal_code", r.FromPostalCode, "to_postal_code", r.ToPostalCode, "to_country_code", r.ToCountryCode); err != nil {
		return err
	}
	if r.Quantity == 0 {
		r.Quantity = 1
	}
	if r.Quantity < 1 {
		return invalid(fmt.Sprintf("quantity must be 1 or more, got %d", r.Quantity))
	}
	// An exact box needs all four numbers; a partial set would silently fall
	// back to the estimate and price a different parcel than the caller meant.
	measurements := []struct {
		name  string
		value float64
	}{{"length_cm", r.LengthCm}, {"width_cm", r.WidthCm}, {"height_cm", r.HeightCm}, {"weight_kg", r.WeightKg}}
	var given, absent []string
	for _, m := range measurements {
		if m.value == 0 {
			absent = append(absent, m.name)
			continue
		}
		if m.value < 0 {
			return invalid(fmt.Sprintf("%s must be a positive number, got %v", m.name, m.value))
		}
		given = append(given, m.name)
	}
	if len(given) > 0 && len(absent) > 0 {
		return invalid("length_cm, width_cm, height_cm and weight_kg go together; missing " + strings.Join(absent, ", "))
	}
	// Exact measurements are only honored when the request says so.
	if len(given) > 0 && r.PackageSource == "" {
		r.PackageSource = packageSourceManual
	}
	return nil
}

func prepareSession(r *CheckoutSessionRequest) error {
	if r.QuoteID != "" {
		if r.Quantity < 0 {
			return invalid(fmt.Sprintf("quantity must be 1 or more, got %d", r.Quantity))
		}
		return nil
	}
	var missing []string
	for _, f := range []struct{ name, value string }{{"product", r.Product}, {"from_zip", r.FromZip}, {"to_zip", r.ToZip}} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return invalid("without quote_id the shipment itself is required: "+strings.Join(missing, ", "), missing...)
	}
	if err := checkRoute("from_zip", r.FromZip, "to_zip", r.ToZip, "to_country", r.ToCountry); err != nil {
		return err
	}
	if r.Quantity == 0 {
		r.Quantity = 1
	}
	if r.Quantity < 1 {
		return invalid(fmt.Sprintf("quantity must be 1 or more, got %d", r.Quantity))
	}
	return nil
}

// The origin is always a US ZIP; the destination is one only on US lanes.
func checkRoute(fromName, from, toName, to, countryName, country string) error {
	if !usZip.MatchString(from) {
		return invalid(fmt.Sprintf("%s must be a 5-digit US ZIP, got %q", fromName, from))
	}
	if country == "" {
		country = defaultCountry
	}
	known := false
	for _, c := range countries {
		known = known || c == country
	}
	if !known {
		return invalid(fmt.Sprintf("%s must be one of %s, got %q", countryName, strings.Join(countries, ", "), country))
	}
	if country == defaultCountry && !usZip.MatchString(to) {
		return invalid(fmt.Sprintf("%s must be a 5-digit US ZIP for a US destination, got %q", toName, to))
	}
	if len(to) > 12 {
		return invalid(fmt.Sprintf("%s must be the destination postal code, up to 12 characters, got %q", toName, to))
	}
	return nil
}

// --- Transport -------------------------------------------------------------

// do runs one operation with the retry policy: GETs get two more attempts on
// a network failure or a 5xx, POSTs never do. A 429 is returned at once.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return invalid("request cannot be encoded as JSON: " + err.Error())
		}
	}
	attempts := 1
	if method == http.MethodGet {
		attempts += len(retryBackoff)
	}
	for attempt := 0; ; attempt++ {
		err := c.send(ctx, method, path, payload, out)
		if err == nil || attempt+1 == attempts || !retryable(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-time.After(retryBackoff[attempt]):
		case <-ctx.Done():
			return err
		}
	}
}

func retryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Code == "network_error" || apiErr.Status >= 500)
}

// send performs one attempt and turns the answer into out or an *APIError.
func (c *Client) send(ctx context.Context, method, path string, payload []byte, out any) error {
	target := c.baseURL + path
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, method, target, reader)
	if err != nil {
		return &APIError{Code: "network_error", Message: fmt.Sprintf("%s %s: %v", method, target, err), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return networkError(method, target, err, ctx, c.timeout)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return networkError(method, target, err, ctx, c.timeout)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFromResponse(resp, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &APIError{
			Status:  resp.StatusCode,
			Code:    "invalid_response",
			Message: fmt.Sprintf("HTTP %d from %s is not the expected JSON: %v", resp.StatusCode, target, err),
			Body:    raw,
			Err:     err,
		}
	}
	return nil
}

// A timeout is reported as such only when the per-attempt deadline fired;
// when the caller's own context ended, their reason is the message.
func networkError(method, target string, cause error, ctx context.Context, timeout time.Duration) *APIError {
	message := fmt.Sprintf("%s %s: %v", method, target, cause)
	if errors.Is(cause, context.DeadlineExceeded) && ctx.Err() == nil {
		message = fmt.Sprintf("%s %s timed out after %s", method, target, timeout)
	}
	return &APIError{Code: "network_error", Message: message, Err: cause}
}

// errorFromResponse builds the error for a non-2xx answer; the Problem's code
// picks the type. A 429 without a Problem body (an edge rate limit answers in
// HTML) is still a rate limit, because the caller's reaction is the same.
func errorFromResponse(resp *http.Response, raw []byte) *APIError {
	e := &APIError{Status: resp.StatusCode, Body: raw}
	var problem Problem
	if json.Unmarshal(raw, &problem) == nil && problem.Error != "" {
		e.Code, e.Message, e.Missing = problem.Error, problem.Message, problem.Missing
	} else if resp.StatusCode == http.StatusTooManyRequests {
		e.Code = "rate_limited"
	} else {
		e.Code = "http_error"
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("HTTP %d from %s", resp.StatusCode, resp.Request.URL)
	}
	// The API does not promise Retry-After; expose it when present so the
	// caller, never the client, decides when to come back.
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
