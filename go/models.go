package smklog

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Typed views of the API's JSON shapes, one struct per schema in
// testdata/schemas. Timestamps and dates stay ISO 8601 strings as sent
// (parse with time.Parse(time.RFC3339, ...)); money is a float64 in USD.
//
// The API adds fields without a version bump, so every response struct keeps
// what it does not name in Extra, and marshals it back out. Fields the schema
// marks nullable are pointers (nil is the API's null).

// Values of QuoteResponse.Mode. Only ModeParcelLabelReady carries rates.
const (
	ModeParcelLabelReady         = "parcel_label_ready"
	ModeNotAParcel               = "not_a_parcel"
	ModeMultipleBoxes            = "multiple_boxes"
	ModeInternationalUnsupported = "international_unsupported_online"
	ModeParcelRestricted         = "parcel_restricted"
	ModeReview                   = "review"
	ModeParcelRatesUnavailable   = "parcel_rates_unavailable"
)

// Values of CheckoutStatus.Status, in the order a checkout moves through them.
const (
	StatusAwaitingCheckout = "awaiting_checkout"
	StatusCheckoutStarted  = "checkout_started"
	StatusPaid             = "paid"
	StatusLabelReady       = "label_ready"
	StatusDelivered        = "delivered"
	StatusRefunded         = "refunded"
)

// QuoteRequest is one shipment to price (quote-request.schema.json). Name the
// item in plain words (the packed box is estimated) or send all four packed
// measurements to skip the estimate. Quantity 0 is sent as the documented
// default of 1.
type QuoteRequest struct {
	Product        string  `json:"product,omitempty"`
	ProductURL     string  `json:"product_url,omitempty"`
	Quantity       int     `json:"quantity,omitempty"`
	FromPostalCode string  `json:"from_postal_code"`
	ToPostalCode   string  `json:"to_postal_code"`
	ToCountryCode  string  `json:"to_country_code,omitempty"`
	LengthCm       float64 `json:"length_cm,omitempty"`
	WidthCm        float64 `json:"width_cm,omitempty"`
	HeightCm       float64 `json:"height_cm,omitempty"`
	WeightKg       float64 `json:"weight_kg,omitempty"`
	PackageSource  string  `json:"package_source,omitempty"`
}

// Package is the packed box used for pricing (package.schema.json).
type Package struct {
	Quantity    int                        `json:"quantity"`
	LengthCm    float64                    `json:"length_cm"`
	WidthCm     float64                    `json:"width_cm"`
	HeightCm    float64                    `json:"height_cm"`
	WeightKg    float64                    `json:"weight_kg"`
	Description string                     `json:"description,omitempty"`
	Confidence  string                     `json:"confidence,omitempty"`
	Assumptions []string                   `json:"assumptions"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// Rate is one purchasable service (rate.schema.json). Amount is the checkout
// total the customer pays, SMKlog fee inside; RetailAmount is the carrier's
// own counter price (0 when it publishes none). The two are not interchangeable.
type Rate struct {
	Carrier               string                     `json:"carrier"`
	Service               string                     `json:"service"`
	ServiceCode           string                     `json:"service_code,omitempty"`
	Amount                float64                    `json:"amount"`
	RetailAmount          float64                    `json:"retail_amount"`
	DeliveryDays          *int                       `json:"delivery_days"`
	DeliveryWindow        *string                    `json:"delivery_window"`
	DeliveryEstimatedDate *string                    `json:"delivery_estimated_date"`
	Currency              string                     `json:"currency"`
	Extra                 map[string]json.RawMessage `json:"-"`
}

// QuoteSource says where the package estimate and the rates came from.
type QuoteSource struct {
	Package string                     `json:"package,omitempty"`
	Rates   string                     `json:"rates,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// QuoteSourcePage is a product page the estimate read.
type QuoteSourcePage struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

// QuoteResponse is a priced parcel, or the reason it was not priced
// (quote-response.schema.json). Rates stay in the API's order, best value first.
type QuoteResponse struct {
	Mode                   string                     `json:"mode"`
	IdentifiedProductTitle string                     `json:"identified_product_title,omitempty"`
	Package                *Package                   `json:"package,omitempty"`
	Packages               []Package                  `json:"packages,omitempty"`
	Rates                  []Rate                     `json:"rates"`
	DangerousGoods         map[string]any             `json:"dangerous_goods"`
	Sources                []QuoteSourcePage          `json:"sources,omitempty"`
	Notice                 *string                    `json:"notice,omitempty"`
	Source                 *QuoteSource               `json:"source,omitempty"`
	Extra                  map[string]json.RawMessage `json:"-"`
}

// IsPriced is true only when the shipment can be bought: ModeParcelLabelReady
// with at least one rate.
func (q *QuoteResponse) IsPriced() bool {
	return q.Mode == ModeParcelLabelReady && len(q.Rates) > 0
}

// Cheapest is the rate with the lowest checkout total, nil when nothing was
// priced. It points into Rates.
func (q *QuoteResponse) Cheapest() *Rate {
	var best *Rate
	for i := range q.Rates {
		if best == nil || q.Rates[i].Amount < best.Amount {
			best = &q.Rates[i]
		}
	}
	return best
}

// QuoteID is the handle a priced answer carries for 15 minutes; pass it to
// CreatePaymentSession to reuse the quote without a second carrier call. The
// published schema admits it as an additional field rather than naming it, so
// it is read from Extra; empty when absent.
func (q *QuoteResponse) QuoteID() string {
	var id string
	if raw, ok := q.Extra["quote_id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	return id
}

// CheckoutSessionRequest is what to open a checkout for: a QuoteID from a
// previous quote (preferred, no second carrier call) or the shipment itself
// (checkout-session-request.schema.json). The client does not turn one into
// the other. Without a QuoteID, Quantity 0 is sent as 1.
type CheckoutSessionRequest struct {
	QuoteID   string `json:"quote_id,omitempty"`
	Service   string `json:"service,omitempty"`
	Product   string `json:"product,omitempty"`
	FromZip   string `json:"from_zip,omitempty"`
	ToZip     string `json:"to_zip,omitempty"`
	ToCountry string `json:"to_country,omitempty"`
	Quantity  int    `json:"quantity,omitempty"`
}

// CheckoutSession is a payment session, not a charge (checkout-session.schema.json):
// a live-quoted amount and the handoff URL where the human pays. SessionID is
// empty only when the session record could not be stored.
type CheckoutSession struct {
	Intent                 string                     `json:"intent"`
	SessionID              string                     `json:"session_id,omitempty"`
	Method                 []string                   `json:"method,omitempty"`
	Amount                 float64                    `json:"amount"`
	Currency               string                     `json:"currency"`
	Service                string                     `json:"service,omitempty"`
	HandoffURL             string                     `json:"handoff_url"`
	IdentifiedProductTitle string                     `json:"identified_product_title,omitempty"`
	QuoteReused            bool                       `json:"quote_reused"`
	Note                   string                     `json:"note,omitempty"`
	Extra                  map[string]json.RawMessage `json:"-"`
}

// Checkout is the checkout a human started, nested in CheckoutStatus. The
// nullable fields fill in as the order moves along: TrackingNumber from
// label_ready on, DeliveredAt once delivered.
type Checkout struct {
	OrderID           string                     `json:"order_id,omitempty"`
	StartedAt         string                     `json:"started_at,omitempty"`
	Paid              bool                       `json:"paid"`
	LabelReady        bool                       `json:"label_ready"`
	Refunded          bool                       `json:"refunded"`
	Carrier           *string                    `json:"carrier"`
	Service           *string                    `json:"service"`
	TrackingNumber    *string                    `json:"tracking_number"`
	TrackingStatus    *string                    `json:"tracking_status"`
	EstimatedDelivery *string                    `json:"estimated_delivery"`
	DeliveredAt       *string                    `json:"delivered_at"`
	Extra             map[string]json.RawMessage `json:"-"`
}

// CheckoutStatus is where a checkout stands (checkout-status.schema.json).
// Checkout is nil while the status is StatusAwaitingCheckout.
type CheckoutStatus struct {
	SessionID   string                     `json:"session_id"`
	Status      string                     `json:"status"`
	CreatedAt   string                     `json:"created_at,omitempty"`
	ExpiresAt   string                     `json:"expires_at,omitempty"`
	Amount      float64                    `json:"amount"`
	Currency    string                     `json:"currency,omitempty"`
	Service     string                     `json:"service,omitempty"`
	QuoteReused bool                       `json:"quote_reused"`
	Checkout    *Checkout                  `json:"checkout"`
	Next        string                     `json:"next,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// Liveness is the answer of GET /status (liveness.schema.json).
type Liveness struct {
	Ok               bool                       `json:"ok"`
	Service          string                     `json:"service"`
	Version          string                     `json:"version"`
	Time             string                     `json:"time"`
	LastEstimateRace map[string]any             `json:"last_estimate_race"`
	Extra            map[string]json.RawMessage `json:"-"`
}

// Problem is every error body on the host (problem.schema.json).
type Problem struct {
	Error   string                     `json:"error"`
	Message string                     `json:"message,omitempty"`
	Missing []string                   `json:"missing,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

// The `type plain X` alias in each method drops X's own methods, so the
// standard decoder fills the named fields without recursing back here.

func (p *Package) UnmarshalJSON(b []byte) error {
	type plain Package
	return decodeExtra(b, (*plain)(p), &p.Extra)
}
func (p Package) MarshalJSON() ([]byte, error) {
	type plain Package
	return encodeExtra(plain(p), p.Extra)
}

func (r *Rate) UnmarshalJSON(b []byte) error {
	type plain Rate
	return decodeExtra(b, (*plain)(r), &r.Extra)
}
func (r Rate) MarshalJSON() ([]byte, error) { type plain Rate; return encodeExtra(plain(r), r.Extra) }

func (s *QuoteSource) UnmarshalJSON(b []byte) error {
	type plain QuoteSource
	return decodeExtra(b, (*plain)(s), &s.Extra)
}
func (s QuoteSource) MarshalJSON() ([]byte, error) {
	type plain QuoteSource
	return encodeExtra(plain(s), s.Extra)
}

func (q *QuoteResponse) UnmarshalJSON(b []byte) error {
	type plain QuoteResponse
	return decodeExtra(b, (*plain)(q), &q.Extra)
}
func (q QuoteResponse) MarshalJSON() ([]byte, error) {
	type plain QuoteResponse
	return encodeExtra(plain(q), q.Extra)
}

func (s *CheckoutSession) UnmarshalJSON(b []byte) error {
	type plain CheckoutSession
	return decodeExtra(b, (*plain)(s), &s.Extra)
}
func (s CheckoutSession) MarshalJSON() ([]byte, error) {
	type plain CheckoutSession
	return encodeExtra(plain(s), s.Extra)
}

func (c *Checkout) UnmarshalJSON(b []byte) error {
	type plain Checkout
	return decodeExtra(b, (*plain)(c), &c.Extra)
}
func (c Checkout) MarshalJSON() ([]byte, error) {
	type plain Checkout
	return encodeExtra(plain(c), c.Extra)
}

func (s *CheckoutStatus) UnmarshalJSON(b []byte) error {
	type plain CheckoutStatus
	return decodeExtra(b, (*plain)(s), &s.Extra)
}
func (s CheckoutStatus) MarshalJSON() ([]byte, error) {
	type plain CheckoutStatus
	return encodeExtra(plain(s), s.Extra)
}

func (l *Liveness) UnmarshalJSON(b []byte) error {
	type plain Liveness
	return decodeExtra(b, (*plain)(l), &l.Extra)
}
func (l Liveness) MarshalJSON() ([]byte, error) {
	type plain Liveness
	return encodeExtra(plain(l), l.Extra)
}

func (p *Problem) UnmarshalJSON(b []byte) error {
	type plain Problem
	return decodeExtra(b, (*plain)(p), &p.Extra)
}
func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem
	return encodeExtra(plain(p), p.Extra)
}

// decodeExtra fills dst from b and stores in *extra the members of b that dst
// has no field for, so a field the API adds tomorrow is not thrown away today.
func decodeExtra(b []byte, dst any, extra *map[string]json.RawMessage) error {
	if err := json.Unmarshal(b, dst); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	for name := range wireNames(reflect.TypeOf(dst).Elem()) {
		delete(all, name)
	}
	if len(all) == 0 {
		all = nil
	}
	*extra = all
	return nil
}

// encodeExtra marshals v and merges extra back in, so a decoded answer
// re-encodes to what the API sent. A named field always wins over an extra.
func encodeExtra(v any, extra map[string]json.RawMessage) ([]byte, error) {
	known, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return known, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(known, &all); err != nil {
		return nil, err
	}
	for name, raw := range extra {
		if _, taken := all[name]; !taken {
			all[name] = raw
		}
	}
	return json.Marshal(all)
}

// wireNames reads the json tags of a struct type: the names a decoder
// would have consumed, and therefore not extras.
func wireNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		names[name] = true
	}
	return names
}
