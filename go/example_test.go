package smklog_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"

	smklog "github.com/smklog/smklog-sdk/go"
)

// The examples answer from a local server so they run offline: a real quote
// spends a live carrier rating. Drop WithBaseURL to talk to production.
func ExampleClient_GetParcelQuote() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"mode":"parcel_label_ready","quote_id":"q_8f3c2e1a4b7d","rates":[
		  {"carrier":"USPS","service":"Ground Advantage","amount":38.42,"retail_amount":46.95,"delivery_days":4,"currency":"USD"},
		  {"carrier":"UPS","service":"Ground","amount":41.1,"retail_amount":67.35,"delivery_days":3,"currency":"USD"}]}`)
	}))
	defer server.Close()

	client := smklog.New(smklog.WithBaseURL(server.URL))
	quote, err := client.GetParcelQuote(context.Background(), smklog.QuoteRequest{
		Product:        "Yamaha FG800 acoustic guitar in a hard case",
		FromPostalCode: "07102",
		ToPostalCode:   "30303",
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, rate := range quote.Rates {
		fmt.Printf("%s %s $%.2f (counter $%.2f)\n", rate.Carrier, rate.Service, rate.Amount, rate.RetailAmount)
	}
	best := quote.Cheapest()
	fmt.Println("cheapest:", best.Carrier, best.Service)
	// QuoteID lets CreatePaymentSession reuse this quote for 15 minutes; it is
	// nil when nothing was priced or the handle could not be stored.
	if quote.QuoteID != nil {
		fmt.Println("quote_id:", *quote.QuoteID)
	}
	// Output:
	// USPS Ground Advantage $38.42 (counter $46.95)
	// UPS Ground $41.10 (counter $67.35)
	// cheapest: USPS Ground Advantage
	// quote_id: q_8f3c2e1a4b7d
}

// One carrier call prices the parcel; the session opened with its QuoteID
// reuses the rates, and SessionID is what GetCheckoutStatus polls.
func ExampleClient_CreatePaymentSession() {
	const sessionID = "as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/quote":
			io.WriteString(w, `{"mode":"parcel_label_ready","quote_id":"q_8f3c2e1a4b7d","rates":[
			  {"carrier":"USPS","service":"Ground Advantage","amount":38.42,"retail_amount":46.95,"delivery_days":4,"currency":"USD"}]}`)
		case "/agent/checkout-session":
			var req smklog.CheckoutSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			// The API reuses the quote when its handle arrives and is still valid.
			fmt.Fprintf(w, `{"intent":"session","session_id":%q,"amount":38.42,"currency":"USD","service":"USPS Ground Advantage",
			  "handoff_url":"https://smklog.com/assets/calculator?agent_session=%s","quote_reused":%v}`,
				sessionID, sessionID, req.QuoteID == "q_8f3c2e1a4b7d")
		default: // GET /agent/checkout-session/{session_id}
			fmt.Fprintf(w, `{"session_id":%q,"status":"awaiting_checkout","amount":38.42,"quote_reused":true,"checkout":null}`, sessionID)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	client := smklog.New(smklog.WithBaseURL(server.URL))
	quote, err := client.GetParcelQuote(ctx, smklog.QuoteRequest{
		Product:        "Yamaha FG800 acoustic guitar in a hard case",
		FromPostalCode: "07102",
		ToPostalCode:   "30303",
	})
	if err != nil {
		log.Fatal(err)
	}
	if !quote.IsPriced() || quote.QuoteID == nil {
		log.Fatal("nothing to buy")
	}
	best := quote.Cheapest()
	session, err := client.CreatePaymentSession(ctx, smklog.CheckoutSessionRequest{
		QuoteID: *quote.QuoteID, Service: best.Service,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("quote reused:", session.QuoteReused, "amount:", session.Amount)
	if session.SessionID == nil { // nil only when the record was not stored
		log.Fatal("session not stored; nothing to poll")
	}
	status, err := client.GetCheckoutStatus(ctx, *session.SessionID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", status.Status)
	// Output:
	// quote reused: true amount: 38.42
	// status: awaiting_checkout
}

func ExampleIsRateLimited() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate_limited","message":"Too many Smart Quote API requests from this client. Try again later."}`)
	}))
	defer server.Close()

	client := smklog.New(smklog.WithBaseURL(server.URL))
	_, err := client.Status(context.Background())
	if smklog.IsRateLimited(err) {
		// The hourly bucket is spent. The client never retries a rate limit; wait, then decide.
		fmt.Println("rate limited")
	}
	var apiErr *smklog.APIError
	if errors.As(err, &apiErr) {
		fmt.Println(apiErr.Status, apiErr.Code)
	}
	// Output:
	// rate limited
	// 429 rate_limited
}
