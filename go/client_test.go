package smklog

// Replays testdata/samples/*.json from a local HTTP server. Nothing here
// touches the network; the one opt-in live call (SMKLOG_LIVE=1, Status only)
// lives in live_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Samples ---------------------------------------------------------------

type sampleEntry struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

type sampleFile struct {
	OperationID string                   `json:"operationId"`
	Method      string                   `json:"method"`
	Path        string                   `json:"path"`
	Examples    []sampleEntry            `json:"examples"`
	Responses   map[string][]sampleEntry `json:"responses"`
}

func sample(t *testing.T, name string) sampleFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "samples", name+".json"))
	if err != nil {
		t.Fatalf("read sample %s: %v", name, err)
	}
	var file sampleFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse sample %s: %v", name, err)
	}
	return file
}

func example(t *testing.T, file sampleFile, name string) json.RawMessage {
	t.Helper()
	for _, e := range file.Examples {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("%s has no request example %q", file.OperationID, name)
	return nil
}

func response(t *testing.T, file sampleFile, status int, name string) json.RawMessage {
	t.Helper()
	for _, r := range file.Responses[strconv.Itoa(status)] {
		if r.Name == name {
			return r.Value
		}
	}
	t.Fatalf("%s has no %d response example %q", file.OperationID, status, name)
	return nil
}

// decodeInto reads a sample value into a typed struct.
func decodeInto[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode sample into %T: %v", out, err)
	}
	return out
}

// asMap normalizes any JSON object for a comparison independent of key order.
func asMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return out
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return b
}

// --- Replay server ---------------------------------------------------------

type answer struct {
	status      int
	body        any // a string is sent as-is, anything else as JSON
	contentType string
	headers     map[string]string
	delay       time.Duration
}

type call struct {
	method  string
	url     string
	headers http.Header
	body    []byte
}

// replay serves queued answers; the last one repeats so a retry loop can be
// observed through calls without the server running dry.
type replay struct {
	server *httptest.Server
	mu     sync.Mutex
	queue  []answer
	calls  []call
}

func newReplay(t *testing.T) *replay {
	t.Helper()
	r := &replay{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, call{method: req.Method, url: req.URL.RequestURI(), headers: req.Header.Clone(), body: body})
		if len(r.queue) == 0 {
			r.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"replay_queue_empty"}`))
			return
		}
		next := r.queue[0]
		if len(r.queue) > 1 {
			r.queue = r.queue[1:]
		}
		r.mu.Unlock()
		if next.delay > 0 {
			time.Sleep(next.delay)
		}
		ct := next.contentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		for k, v := range next.headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(next.status)
		switch b := next.body.(type) {
		case string:
			_, _ = w.Write([]byte(b))
		case json.RawMessage:
			_, _ = w.Write(b)
		default:
			_ = json.NewEncoder(w).Encode(b)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *replay) reply(answers ...answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = answers
	r.calls = nil
}

func (r *replay) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *replay) call(t *testing.T, i int) call {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.calls) {
		t.Fatalf("only %d calls recorded, wanted index %d", len(r.calls), i)
	}
	return r.calls[i]
}

func (r *replay) client(opts ...Option) *Client {
	return New(append([]Option{WithBaseURL(r.server.URL)}, opts...)...)
}

type fixtures struct {
	quoteRequests, quoteResponses, sessionRequests, sessionResponses, statusResponses, livenessResponses sampleFile
	quoteOK, livenessOK                                                                                  json.RawMessage
}

func load(t *testing.T) fixtures {
	t.Helper()
	f := fixtures{
		quoteRequests:     sample(t, "get-parcel-quote-request"),
		quoteResponses:    sample(t, "get-parcel-quote-responses"),
		sessionRequests:   sample(t, "create-payment-session-request"),
		sessionResponses:  sample(t, "create-payment-session-responses"),
		statusResponses:   sample(t, "get-checkout-status-responses"),
		livenessResponses: sample(t, "status-responses"),
	}
	f.quoteOK = response(t, f.quoteResponses, 200, "priced")
	f.livenessOK = response(t, f.livenessResponses, 200, "default")
	return f
}

func apiErr(t *testing.T, err error) *APIError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	return e
}

// The ids the vendored samples carry: the priced quote's handle, which the
// fromQuote session example passes back, and the session the examples return.
const (
	quoteID   = "q_8f3c2e1a4b7d"
	sessionID = "as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33"
)

// --- Round trips -----------------------------------------------------------

func TestGetParcelQuote(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()

	t.Run("sends the described example verbatim and returns the priced answer", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		req := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))
		quote, err := r.client().GetParcelQuote(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d, want 1", r.count())
		}
		c := r.call(t, 0)
		if c.method != http.MethodPost || c.url != f.quoteRequests.Path {
			t.Fatalf("sent %s %s, want POST %s", c.method, c.url, f.quoteRequests.Path)
		}
		if got, want := asMap(t, c.body), asMap(t, example(t, f.quoteRequests, "described")); !reflect.DeepEqual(got, want) {
			t.Fatalf("body sent = %v, want %v", got, want)
		}
		if got, want := asMap(t, marshal(t, quote)), asMap(t, f.quoteOK); !reflect.DeepEqual(got, want) {
			t.Fatalf("quote re-encodes to %v, want %v", got, want)
		}
		if !quote.IsPriced() {
			t.Fatal("IsPriced() = false for a parcel_label_ready answer with rates")
		}
		best := quote.Cheapest()
		if best == nil || best.Amount != 38.42 || best != &quote.Rates[0] {
			t.Fatalf("Cheapest() = %+v, want the first rate at 38.42", best)
		}
		if quote.QuoteID == nil {
			t.Fatal("QuoteID = nil for the priced sample")
		}
		if *quote.QuoteID != quoteID {
			t.Fatalf("QuoteID = %q, want %q", *quote.QuoteID, quoteID)
		}
	})

	t.Run("sends the measured example verbatim", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		req := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "measured"))
		if _, err := r.client().GetParcelQuote(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got, want := asMap(t, r.call(t, 0).body), asMap(t, example(t, f.quoteRequests, "measured")); !reflect.DeepEqual(got, want) {
			t.Fatalf("body sent = %v, want %v", got, want)
		}
	})

	t.Run("sends quantity 1 when the caller leaves it out", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		req := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))
		req.Quantity = 0
		if _, err := r.client().GetParcelQuote(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := asMap(t, r.call(t, 0).body)["quantity"]; got != float64(1) {
			t.Fatalf("quantity sent = %v, want 1", got)
		}
	})

	t.Run("measurements without package_source are sent as manual", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		req := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "measured"))
		req.PackageSource = ""
		if _, err := r.client().GetParcelQuote(ctx, req); err != nil {
			t.Fatal(err)
		}
		if got := asMap(t, r.call(t, 0).body)["package_source"]; got != "manual" {
			t.Fatalf("package_source sent = %v, want manual", got)
		}
	})

	t.Run("rates stay in API order; Cheapest picks the lowest amount wherever it sits", func(t *testing.T) {
		reordered := asMap(t, f.quoteOK)
		rates := reordered["rates"].([]any)
		for i, j := 0, len(rates)-1; i < j; i, j = i+1, j-1 {
			rates[i], rates[j] = rates[j], rates[i]
		}
		r.reply(answer{status: 200, body: reordered})
		quote, err := r.client().GetParcelQuote(ctx, decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
		if err != nil {
			t.Fatal(err)
		}
		if quote.Rates[0].Carrier == "USPS" {
			t.Fatal("rates were reordered by the client")
		}
		if best := quote.Cheapest(); best == nil || best.Carrier != "USPS" {
			t.Fatalf("Cheapest() = %+v, want the USPS rate", best)
		}
	})

	t.Run("notAParcel answer: IsPriced false, Cheapest nil, notice kept", func(t *testing.T) {
		notAParcel := response(t, f.quoteResponses, 200, "notAParcel")
		r.reply(answer{status: 200, body: notAParcel})
		quote, err := r.client().GetParcelQuote(ctx, decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
		if err != nil {
			t.Fatal(err)
		}
		if quote.Mode != ModeNotAParcel || quote.IsPriced() || quote.Cheapest() != nil {
			t.Fatalf("mode=%q priced=%v cheapest=%v", quote.Mode, quote.IsPriced(), quote.Cheapest())
		}
		if quote.Notice == nil || *quote.Notice != asMap(t, notAParcel)["notice"] {
			t.Fatalf("notice = %v", quote.Notice)
		}
		if quote.QuoteID != nil {
			t.Fatalf("QuoteID = %q for an answer without rates", *quote.QuoteID)
		}
		// The answer carries no quote_id, so the nullable field re-encodes as null.
		want := asMap(t, notAParcel)
		want["quote_id"] = nil
		if got := asMap(t, marshal(t, quote)); !reflect.DeepEqual(got, want) {
			t.Fatalf("quote re-encodes to %v, want %v", got, want)
		}
	})

	t.Run("unknown fields are preserved", func(t *testing.T) {
		extended := asMap(t, f.quoteOK)
		extended["added_next_year"] = map[string]any{"nested": true}
		r.reply(answer{status: 200, body: extended})
		quote, err := r.client().GetParcelQuote(ctx, decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
		if err != nil {
			t.Fatal(err)
		}
		if string(quote.Extra["added_next_year"]) != `{"nested":true}` {
			t.Fatalf("Extra = %s", quote.Extra["added_next_year"])
		}
		if got := asMap(t, marshal(t, quote))["added_next_year"]; !reflect.DeepEqual(got, map[string]any{"nested": true}) {
			t.Fatalf("re-encoded extra = %v", got)
		}
		// quote_id is a named field, so it never lands in Extra.
		if _, extra := quote.Extra["quote_id"]; extra {
			t.Fatal("quote_id landed in Extra instead of QuoteID")
		}
	})

	t.Run("a null quote_id (handle not stored) is nil and round-trips as null", func(t *testing.T) {
		unstored := asMap(t, f.quoteOK)
		unstored["quote_id"] = nil
		r.reply(answer{status: 200, body: unstored})
		quote, err := r.client().GetParcelQuote(ctx, decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
		if err != nil {
			t.Fatal(err)
		}
		if quote.QuoteID != nil || !quote.IsPriced() {
			t.Fatalf("QuoteID = %v (want nil), IsPriced() = %v (want true)", quote.QuoteID, quote.IsPriced())
		}
		if got, want := asMap(t, marshal(t, quote)), unstored; !reflect.DeepEqual(got, want) {
			t.Fatalf("quote re-encodes to %v, want %v", got, want)
		}
	})
}

func TestCreatePaymentSession(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	expected := response(t, f.sessionResponses, 200, "default")
	for _, name := range []string{"fromQuote", "fromShipment"} {
		t.Run("sends the "+name+" example verbatim and returns the session", func(t *testing.T) {
			r.reply(answer{status: 200, body: expected})
			req := decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, name))
			session, err := r.client().CreatePaymentSession(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			c := r.call(t, 0)
			if c.method != http.MethodPost || c.url != f.sessionRequests.Path {
				t.Fatalf("sent %s %s", c.method, c.url)
			}
			if got, want := asMap(t, c.body), asMap(t, example(t, f.sessionRequests, name)); !reflect.DeepEqual(got, want) {
				t.Fatalf("body sent = %v, want %v", got, want)
			}
			if got, want := asMap(t, marshal(t, session)), asMap(t, expected); !reflect.DeepEqual(got, want) {
				t.Fatalf("session re-encodes to %v, want %v", got, want)
			}
			if session.Intent != "session" || session.SessionID == nil || *session.SessionID != sessionID || session.HandoffURL == "" {
				t.Fatalf("session = %+v", session)
			}
		})
	}

	t.Run("a null session_id (record not stored) is nil and round-trips as null", func(t *testing.T) {
		unstored := asMap(t, expected)
		unstored["session_id"] = nil
		r.reply(answer{status: 200, body: unstored})
		session, err := r.client().CreatePaymentSession(context.Background(), decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, "fromQuote")))
		if err != nil {
			t.Fatal(err)
		}
		if session.SessionID != nil || session.HandoffURL == "" {
			t.Fatalf("session = %+v", session)
		}
		if got, want := asMap(t, marshal(t, session)), unstored; !reflect.DeepEqual(got, want) {
			t.Fatalf("session re-encodes to %v, want %v", got, want)
		}
	})

	t.Run("the quote_id a priced answer carries is sent verbatim, and nothing else", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK}, answer{status: 200, body: expected})
		client := r.client()
		quote, err := client.GetParcelQuote(context.Background(), decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
		if err != nil {
			t.Fatal(err)
		}
		if quote.QuoteID == nil {
			t.Fatal("QuoteID = nil for the priced sample")
		}
		session, err := client.CreatePaymentSession(context.Background(), CheckoutSessionRequest{QuoteID: *quote.QuoteID})
		if err != nil {
			t.Fatal(err)
		}
		// No quantity default and no shipment fields: the quote names the shipment.
		if got, want := asMap(t, r.call(t, 1).body), map[string]any{"quote_id": quoteID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("body sent = %v, want %v", got, want)
		}
		if !session.QuoteReused {
			t.Fatalf("quote_reused = false: %+v", session)
		}
	})
}

// --- The quickstart --------------------------------------------------------

// One carrier call prices the parcel; the session reuses it through QuoteID
// and GetCheckoutStatus polls the SessionID, each guarded against nil.
func TestQuoteToCheckoutFlow(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	r.reply(
		answer{status: 200, body: f.quoteOK},
		answer{status: 200, body: response(t, f.sessionResponses, 200, "default")},
		answer{status: 200, body: response(t, f.statusResponses, 200, "default")},
	)
	client := r.client()

	quote, err := client.GetParcelQuote(ctx, decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")))
	if err != nil {
		t.Fatal(err)
	}
	if !quote.IsPriced() || quote.QuoteID == nil {
		t.Fatalf("IsPriced() = %v, QuoteID = %v", quote.IsPriced(), quote.QuoteID)
	}
	// The fromQuote example passes back the very handle the priced answer carries.
	if fromQuote := asMap(t, example(t, f.sessionRequests, "fromQuote"))["quote_id"]; fromQuote != *quote.QuoteID {
		t.Fatalf("fromQuote example sends %v, the priced answer carries %q", fromQuote, *quote.QuoteID)
	}
	best := quote.Cheapest()

	session, err := client.CreatePaymentSession(ctx, CheckoutSessionRequest{QuoteID: *quote.QuoteID, Service: best.Service})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := asMap(t, r.call(t, 1).body), map[string]any{"quote_id": quoteID, "service": best.Service}; !reflect.DeepEqual(got, want) {
		t.Fatalf("session body sent = %v, want %v", got, want)
	}
	if !session.QuoteReused || session.SessionID == nil || session.HandoffURL == "" {
		t.Fatalf("session = %+v", session)
	}

	status, err := client.GetCheckoutStatus(ctx, *session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "/agent/checkout-session/" + *session.SessionID; r.call(t, 2).url != want {
		t.Fatalf("status read %s, want %s", r.call(t, 2).url, want)
	}
	if status.SessionID != *session.SessionID || !status.QuoteReused {
		t.Fatalf("status = %+v", status)
	}
	if r.count() != 3 {
		t.Fatalf("calls = %d, want one quote, one session, one status read", r.count())
	}
}

func TestGetCheckoutStatus(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	expected := response(t, f.statusResponses, 200, "default")

	t.Run("reads the session by id and returns the status", func(t *testing.T) {
		r.reply(answer{status: 200, body: expected})
		status, err := r.client().GetCheckoutStatus(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		c := r.call(t, 0)
		if want := strings.Replace(f.statusResponses.Path, "{session_id}", sessionID, 1); c.method != http.MethodGet || c.url != want {
			t.Fatalf("sent %s %s, want GET %s", c.method, c.url, want)
		}
		if len(c.body) != 0 {
			t.Fatalf("GET sent a body: %s", c.body)
		}
		if got, want := asMap(t, marshal(t, status)), asMap(t, expected); !reflect.DeepEqual(got, want) {
			t.Fatalf("status re-encodes to %v, want %v", got, want)
		}
		if status.Status != StatusLabelReady || status.Checkout == nil || status.Checkout.TrackingNumber == nil {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("URL-encodes the id", func(t *testing.T) {
		r.reply(answer{status: 200, body: expected})
		if _, err := r.client().GetCheckoutStatus(context.Background(), "as_x/y?z"); err != nil {
			t.Fatal(err)
		}
		if got := r.call(t, 0).url; got != "/agent/checkout-session/as_x%2Fy%3Fz" {
			t.Fatalf("url = %s", got)
		}
	})
}

func TestStatus(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	r.reply(answer{status: 200, body: f.livenessOK})
	liveness, err := r.client().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := r.call(t, 0)
	if c.method != http.MethodGet || c.url != f.livenessResponses.Path {
		t.Fatalf("sent %s %s", c.method, c.url)
	}
	if got, want := asMap(t, marshal(t, liveness)), asMap(t, f.livenessOK); !reflect.DeepEqual(got, want) {
		t.Fatalf("liveness re-encodes to %v, want %v", got, want)
	}
	if !liveness.Ok || liveness.Service != "smklog-quote-api" || liveness.Version != "1.4.0" {
		t.Fatalf("liveness = %+v", liveness)
	}
}

// --- Headers ---------------------------------------------------------------

func TestHeaders(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	described := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))

	t.Run("Accept, Content-Type on POST and the SDK User-Agent", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		if _, err := r.client().GetParcelQuote(ctx, described); err != nil {
			t.Fatal(err)
		}
		h := r.call(t, 0).headers
		if h.Get("Accept") != "application/json" || h.Get("Content-Type") != "application/json" {
			t.Fatalf("headers = %v", h)
		}
		if h.Get("User-Agent") != "smklog-sdk-go/"+Version {
			t.Fatalf("User-Agent = %q", h.Get("User-Agent"))
		}
		if h.Get("Authorization") != "" {
			t.Fatalf("Authorization sent without a token: %q", h.Get("Authorization"))
		}
	})

	t.Run("GET sends no Content-Type; the User-Agent suffix follows the SDK token", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.livenessOK})
		if _, err := r.client(WithUserAgent("acme-bot/2.1")).Status(ctx); err != nil {
			t.Fatal(err)
		}
		h := r.call(t, 0).headers
		if h.Get("Content-Type") != "" {
			t.Fatalf("Content-Type on GET: %q", h.Get("Content-Type"))
		}
		if h.Get("User-Agent") != "smklog-sdk-go/"+Version+" acme-bot/2.1" {
			t.Fatalf("User-Agent = %q", h.Get("User-Agent"))
		}
	})

	t.Run("token becomes a bearer Authorization header", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.livenessOK})
		if _, err := r.client(WithToken("opaque-token")).Status(ctx); err != nil {
			t.Fatal(err)
		}
		if got := r.call(t, 0).headers.Get("Authorization"); got != "Bearer opaque-token" {
			t.Fatalf("Authorization = %q", got)
		}
	})

	t.Run("a trailing slash on the base URL does not double up the path", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.livenessOK})
		if _, err := New(WithBaseURL(r.server.URL + "/")).Status(ctx); err != nil {
			t.Fatal(err)
		}
		if got := r.call(t, 0).url; got != "/status" {
			t.Fatalf("url = %s", got)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		c := New()
		if c.BaseURL() != DefaultBaseURL || c.timeout != DefaultTimeout || c.token != "" {
			t.Fatalf("defaults = %+v", c)
		}
	})
}

// --- Documented errors -----------------------------------------------------

func TestErrorMapping(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	described := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))
	fromQuote := decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, "fromQuote"))
	fromShipment := decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, "fromShipment"))

	quote := func(c *Client) error { _, err := c.GetParcelQuote(ctx, described); return err }
	sessionQ := func(c *Client) error { _, err := c.CreatePaymentSession(ctx, fromQuote); return err }
	sessionS := func(c *Client) error { _, err := c.CreatePaymentSession(ctx, fromShipment); return err }
	status := func(c *Client) error { _, err := c.GetCheckoutStatus(ctx, sessionID); return err }

	cases := []struct {
		name     string
		file     sampleFile
		status   int
		sentinel error
		call     func(*Client) error
	}{
		{"quote 400 missing_required_fields -> ErrInvalidRequest with missing", f.quoteResponses, 400, ErrInvalidRequest, quote},
		{"quote 422 product_url_not_detected -> ErrInvalidRequest", f.quoteResponses, 422, ErrInvalidRequest, quote},
		{"quote 429 rate_limited -> ErrRateLimited", f.quoteResponses, 429, ErrRateLimited, quote},
		{"session 400 invalid_request -> ErrInvalidRequest", f.sessionResponses, 400, ErrInvalidRequest, sessionQ},
		{"session 422 session_failed -> ErrSessionFailed", f.sessionResponses, 422, ErrSessionFailed, sessionS},
		{"session 429 rate_limited -> ErrRateLimited", f.sessionResponses, 429, ErrRateLimited, sessionQ},
		{"checkout status 404 session_not_found -> ErrNotFound", f.statusResponses, 404, ErrNotFound, status},
		{"checkout status 429 rate_limited -> ErrRateLimited", f.statusResponses, 429, ErrRateLimited, status},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problem := response(t, tc.file, tc.status, "default")
			r.reply(answer{status: tc.status, body: problem})
			e := apiErr(t, tc.call(r.client()))
			if !errors.Is(e, tc.sentinel) {
				t.Fatalf("errors.Is(%v, %v) = false", e, tc.sentinel)
			}
			want := decodeInto[Problem](t, problem)
			if e.Status != tc.status || e.Code != want.Error {
				t.Fatalf("got status=%d code=%q, want %d %q", e.Status, e.Code, tc.status, want.Error)
			}
			// A Problem without a sentence still gets one naming the status.
			if want.Message != "" && e.Message != want.Message || e.Message == "" {
				t.Fatalf("Message = %q, want %q", e.Message, want.Message)
			}
			if !reflect.DeepEqual(e.Missing, want.Missing) {
				t.Fatalf("Missing = %v, want %v", e.Missing, want.Missing)
			}
			if got, wantBody := asMap(t, e.Body), asMap(t, problem); !reflect.DeepEqual(got, wantBody) {
				t.Fatalf("Body = %s", e.Body)
			}
			if r.count() != 1 {
				t.Fatalf("calls = %d; a documented refusal is never retried", r.count())
			}
			if !strings.Contains(e.Error(), want.Error) {
				t.Fatalf("Error() = %q does not name the code", e.Error())
			}
		})
	}

	t.Run("the four Is helpers agree with the sentinels", func(t *testing.T) {
		checks := map[string]func(error) bool{
			"rate_limited": IsRateLimited, "invalid_request": IsInvalidRequest,
			"session_not_found": IsNotFound, "session_failed": IsSessionFailed,
		}
		for code, is := range checks {
			e := &APIError{Status: 400, Code: code}
			if !is(e) {
				t.Fatalf("%s not recognized by its helper", code)
			}
			for other, otherIs := range checks {
				if other != code && otherIs(e) {
					t.Fatalf("%s wrongly matches %s", code, other)
				}
			}
		}
	})

	t.Run("a code outside the table matches no sentinel", func(t *testing.T) {
		r.reply(answer{status: 401, body: map[string]any{"error": "invalid_token", "message": "Unknown bearer."}})
		e := apiErr(t, status(r.client(WithToken("stale"))))
		if e.Status != 401 || e.Code != "invalid_token" {
			t.Fatalf("e = %+v", e)
		}
		for _, s := range []error{ErrRateLimited, ErrInvalidRequest, ErrNotFound, ErrSessionFailed, ErrNetwork} {
			if errors.Is(e, s) {
				t.Fatalf("invalid_token matched %v", s)
			}
		}
	})

	t.Run("slow_down is also rate limited, with Retry-After exposed", func(t *testing.T) {
		r.reply(answer{status: 429, body: map[string]any{"error": "slow_down", "message": "Slow down."}, headers: map[string]string{"Retry-After": "120"}})
		e := apiErr(t, status(r.client()))
		if !IsRateLimited(e) || e.RetryAfter != 120*time.Second {
			t.Fatalf("e = %+v", e)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d", r.count())
		}
	})

	t.Run("a 429 without a Problem body is still rate limited", func(t *testing.T) {
		r.reply(answer{status: 429, body: "<html>limited</html>", contentType: "text/html"})
		e := apiErr(t, status(r.client()))
		if !IsRateLimited(e) || e.Code != "rate_limited" {
			t.Fatalf("e = %+v", e)
		}
	})

	t.Run("a non-JSON error body still raises http_error with the raw text", func(t *testing.T) {
		r.reply(answer{status: 502, body: "<html>bad gateway</html>", contentType: "text/html"})
		e := apiErr(t, quote(r.client()))
		if e.Status != 502 || e.Code != "http_error" || string(e.Body) != "<html>bad gateway</html>" {
			t.Fatalf("e = %+v", e)
		}
	})
}

// --- Retry policy ----------------------------------------------------------

func TestRetries(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	described := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))
	fromQuote := decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, "fromQuote"))

	t.Run("429 is returned at once, no retry (POST and GET)", func(t *testing.T) {
		r.reply(answer{status: 429, body: response(t, f.quoteResponses, 429, "default")})
		if _, err := r.client().GetParcelQuote(ctx, described); !IsRateLimited(err) {
			t.Fatalf("err = %v", err)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d", r.count())
		}
		r.reply(answer{status: 429, body: response(t, f.statusResponses, 429, "default")})
		if _, err := r.client().Status(ctx); !IsRateLimited(err) {
			t.Fatalf("err = %v", err)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d", r.count())
		}
	})

	t.Run("a 503 on Status is retried and then succeeds", func(t *testing.T) {
		r.reply(answer{status: 503, body: map[string]any{"error": "upstream_unavailable"}}, answer{status: 200, body: f.livenessOK})
		liveness, err := r.client().Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !liveness.Ok || r.count() != 2 {
			t.Fatalf("ok=%v calls=%d", liveness.Ok, r.count())
		}
	})

	t.Run("a 5xx on GetCheckoutStatus is retried at most twice, then returned", func(t *testing.T) {
		r.reply(answer{status: 500, body: map[string]any{"error": "internal", "message": "boom"}})
		_, err := r.client().GetCheckoutStatus(ctx, sessionID)
		e := apiErr(t, err)
		if e.Status != 500 || r.count() != 3 {
			t.Fatalf("status=%d calls=%d", e.Status, r.count())
		}
	})

	t.Run("a 5xx on a POST is never retried", func(t *testing.T) {
		r.reply(answer{status: 503, body: map[string]any{"error": "upstream_unavailable"}})
		_, err := r.client().GetParcelQuote(ctx, described)
		if e := apiErr(t, err); e.Status != 503 {
			t.Fatalf("e = %+v", e)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d", r.count())
		}
		r.reply(answer{status: 503, body: map[string]any{"error": "upstream_unavailable"}})
		_, err = r.client().CreatePaymentSession(ctx, fromQuote)
		if e := apiErr(t, err); e.Status != 503 {
			t.Fatalf("e = %+v", e)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d", r.count())
		}
	})

	t.Run("a 5xx that carries a rate-limit code is not retried", func(t *testing.T) {
		r.reply(answer{status: 503, body: map[string]any{"error": "rate_limited", "message": "Too many requests."}})
		_, err := r.client().Status(ctx)
		if !IsRateLimited(err) {
			t.Fatalf("err = %v", err)
		}
		if r.count() != 1 {
			t.Fatalf("calls = %d; a rate limit is never retried, whatever its status", r.count())
		}
	})

	t.Run("a canceled context stops the retry loop", func(t *testing.T) {
		r.reply(answer{status: 503, body: map[string]any{"error": "upstream_unavailable"}})
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := r.client().Status(canceled)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want a context.Canceled wrapper", err)
		}
	})
}

// --- Network and timeout ---------------------------------------------------

func TestNetwork(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	described := decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described"))

	t.Run("a timeout raises network_error with status 0 and is not retried on POST", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK, delay: 300 * time.Millisecond})
		_, err := r.client(WithTimeout(50*time.Millisecond)).GetParcelQuote(ctx, described)
		e := apiErr(t, err)
		if e.Status != 0 || e.Code != "network_error" || !errors.Is(e, ErrNetwork) {
			t.Fatalf("e = %+v", e)
		}
		if !strings.Contains(e.Message, "timed out after 50ms") {
			t.Fatalf("message = %q", e.Message)
		}
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("the transport error is not reachable: %v", e.Err)
		}
		time.Sleep(350 * time.Millisecond) // let the delayed handler finish before counting
		if r.count() != 1 {
			t.Fatalf("calls = %d; a timed-out POST is not retried", r.count())
		}
	})

	t.Run("a refused connection raises network_error wrapping the transport error", func(t *testing.T) {
		_, err := New(WithBaseURL("http://127.0.0.1:1")).GetParcelQuote(ctx, described)
		e := apiErr(t, err)
		if e.Status != 0 || e.Code != "network_error" || e.Err == nil {
			t.Fatalf("e = %+v", e)
		}
	})

	t.Run("a network failure on Status is retried before it is returned", func(t *testing.T) {
		started := time.Now()
		_, err := New(WithBaseURL("http://127.0.0.1:1"), WithTimeout(time.Second)).Status(ctx)
		if !errors.Is(err, ErrNetwork) {
			t.Fatalf("err = %v", err)
		}
		if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
			t.Fatalf("elapsed %s; two backoffs (200 ms + 400 ms) separate the three attempts", elapsed)
		}
	})

	t.Run("WithTimeout(0) turns the per-attempt deadline off instead of failing at once", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.livenessOK})
		liveness, err := r.client(WithTimeout(0)).Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !liveness.Ok || r.count() != 1 {
			t.Fatalf("ok=%v calls=%d", liveness.Ok, r.count())
		}
	})

	t.Run("a 200 that is not JSON raises invalid_response", func(t *testing.T) {
		r.reply(answer{status: 200, body: "not json at all", contentType: "text/plain"})
		_, err := r.client().GetParcelQuote(ctx, described)
		e := apiErr(t, err)
		if e.Status != 200 || e.Code != "invalid_response" {
			t.Fatalf("e = %+v", e)
		}
	})
}

// --- Local validation ------------------------------------------------------

func TestValidation(t *testing.T) {
	f := load(t)
	r := newReplay(t)
	ctx := context.Background()
	described := func() QuoteRequest { return decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "described")) }
	measured := func() QuoteRequest { return decodeInto[QuoteRequest](t, example(t, f.quoteRequests, "measured")) }
	shipment := func() CheckoutSessionRequest {
		return decodeInto[CheckoutSessionRequest](t, example(t, f.sessionRequests, "fromShipment"))
	}
	quoteWith := func(edit func(*QuoteRequest)) func(*Client) error {
		return func(c *Client) error {
			q := described()
			edit(&q)
			_, err := c.GetParcelQuote(ctx, q)
			return err
		}
	}
	measuredWith := func(edit func(*QuoteRequest)) func(*Client) error {
		return func(c *Client) error {
			q := measured()
			edit(&q)
			_, err := c.GetParcelQuote(ctx, q)
			return err
		}
	}
	sessionWith := func(edit func(*CheckoutSessionRequest)) func(*Client) error {
		return func(c *Client) error {
			s := shipment()
			edit(&s)
			_, err := c.CreatePaymentSession(ctx, s)
			return err
		}
	}

	cases := []struct {
		name    string
		call    func(*Client) error
		message string
	}{
		{"4-digit origin ZIP", quoteWith(func(q *QuoteRequest) { q.FromPostalCode = "0710" }), "from_postal_code must be a 5-digit US ZIP"},
		{"4-digit destination ZIP on a US lane", quoteWith(func(q *QuoteRequest) { q.ToPostalCode = "3030" }), "to_postal_code must be a 5-digit US ZIP"},
		{"letters in the origin ZIP", quoteWith(func(q *QuoteRequest) { q.FromPostalCode = "SW1A 1AA" }), "from_postal_code"},
		{"country outside the five sold", quoteWith(func(q *QuoteRequest) { q.ToCountryCode = "FR" }), "to_country_code must be one of US, CA, GB, DE, AU"},
		{"negative quantity", quoteWith(func(q *QuoteRequest) { q.Quantity = -1 }), "quantity must be 1 or more"},
		{"three of the four measurements", measuredWith(func(q *QuoteRequest) { q.WeightKg = 0 }), "go together; missing weight_kg"},
		{"a negative measurement", measuredWith(func(q *QuoteRequest) { q.HeightCm = -1 }), "height_cm must be between 1 and 300, got -1"},
		{"a side under the schema's 1 cm", measuredWith(func(q *QuoteRequest) { q.LengthCm = 0.5 }), "length_cm must be between 1 and 300, got 0.5"},
		{"a weight past the schema's 1000 kg", measuredWith(func(q *QuoteRequest) { q.WeightKg = 1200 }), "weight_kg must be between 0.1 and 1000, got 1200"},
		{"neither product nor product_url", quoteWith(func(q *QuoteRequest) { q.Product = "" }), "required: product"},
		{"missing to_postal_code", quoteWith(func(q *QuoteRequest) { q.ToPostalCode = "" }), "to_postal_code"},
		{"session: 4-digit from_zip", sessionWith(func(s *CheckoutSessionRequest) { s.FromZip = "0710" }), "from_zip must be a 5-digit US ZIP"},
		{"session: no quote_id and no product", func(c *Client) error {
			_, err := c.CreatePaymentSession(ctx, CheckoutSessionRequest{FromZip: "07102", ToZip: "30303"})
			return err
		}, "without quote_id the shipment itself is required: product"},
		{"session: negative quantity with a quote_id", func(c *Client) error {
			_, err := c.CreatePaymentSession(ctx, CheckoutSessionRequest{QuoteID: quoteID, Quantity: -2})
			return err
		}, "quantity must be 1 or more"},
		{"checkout status: empty id", func(c *Client) error {
			_, err := c.GetCheckoutStatus(ctx, "  ")
			return err
		}, "session_id must be a non-empty string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.reply(answer{status: 200, body: f.quoteOK})
			e := apiErr(t, tc.call(r.client()))
			if !IsInvalidRequest(e) || e.Status != 0 {
				t.Fatalf("e = %+v", e)
			}
			if !strings.Contains(e.Message, tc.message) {
				t.Fatalf("message %q does not contain %q", e.Message, tc.message)
			}
			if r.count() != 0 {
				t.Fatalf("calls = %d; validation must reject before any request", r.count())
			}
		})
	}

	t.Run("missing fields are listed the way the API lists them", func(t *testing.T) {
		_, err := r.client().GetParcelQuote(ctx, QuoteRequest{Product: "books", FromPostalCode: "07102"})
		e := apiErr(t, err)
		if e.Code != "missing_required_fields" || !reflect.DeepEqual(e.Missing, []string{"to_postal_code"}) {
			t.Fatalf("e = %+v", e)
		}
	})

	t.Run("an international destination takes the country's own postal code", func(t *testing.T) {
		r.reply(answer{status: 200, body: f.quoteOK})
		q := described()
		q.ToPostalCode, q.ToCountryCode = "SW1A 1AA", "GB"
		if _, err := r.client().GetParcelQuote(ctx, q); err != nil {
			t.Fatal(err)
		}
		if got := asMap(t, r.call(t, 0).body)["to_postal_code"]; got != "SW1A 1AA" {
			t.Fatalf("to_postal_code sent = %v", got)
		}
	})

	t.Run("a quote_id skips shipment validation and is sent as given", func(t *testing.T) {
		r.reply(answer{status: 200, body: response(t, f.sessionResponses, 200, "default")})
		if _, err := r.client().CreatePaymentSession(ctx, CheckoutSessionRequest{QuoteID: quoteID}); err != nil {
			t.Fatal(err)
		}
		if got, want := asMap(t, r.call(t, 0).body), map[string]any{"quote_id": quoteID}; !reflect.DeepEqual(got, want) {
			t.Fatalf("body sent = %v", got)
		}
	})

	t.Run("a shipment without quantity is sent with quantity 1", func(t *testing.T) {
		r.reply(answer{status: 200, body: response(t, f.sessionResponses, 200, "default")})
		s := shipment()
		s.Quantity = 0
		if _, err := r.client().CreatePaymentSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		if got := asMap(t, r.call(t, 0).body)["quantity"]; got != float64(1) {
			t.Fatalf("quantity sent = %v", got)
		}
	})
}
