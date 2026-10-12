package smklog_test

import (
	"context"
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
	// The quote_id lets CreatePaymentSession reuse this quote for 15 minutes.
	fmt.Println("quote_id:", quote.QuoteID())
	// Output:
	// USPS Ground Advantage $38.42 (counter $46.95)
	// UPS Ground $41.10 (counter $67.35)
	// cheapest: USPS Ground Advantage
	// quote_id: q_8f3c2e1a4b7d
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
		// The hourly bucket is spent. The client never retries a 429; wait, then decide.
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
