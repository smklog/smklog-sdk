// Package smklog is the official Go client for the SMKlog Quote API
// (https://smklog.com/developers/): live USPS, UPS, FedEx and DHL Express
// parcel rates from a US origin, a checkout handed to a human, and where that
// checkout stands. No API key is needed.
//
// The client is thin by design: four operations, local validation that saves
// a wasted request, and errors typed by the API's own error codes. Field
// names stay snake_case on the wire exactly as the API sends them; the Go
// structs only add exported names and json tags.
//
// One carrier call prices the parcel; the session reuses it through QuoteID:
//
//	client := smklog.New()
//	quote, err := client.GetParcelQuote(ctx, smklog.QuoteRequest{
//		Product:        "Yamaha FG800 acoustic guitar in a hard case",
//		FromPostalCode: "07102",
//		ToPostalCode:   "30303",
//	})
//	if err != nil {
//		if smklog.IsRateLimited(err) {
//			// The hourly bucket is spent; the client never retries a rate limit.
//		}
//		return err
//	}
//	if quote.IsPriced() && quote.QuoteID != nil { // the handle is good for 15 minutes
//		best := quote.Cheapest()
//		fmt.Println(best.Carrier, best.Service, best.Amount)
//		session, err := client.CreatePaymentSession(ctx, smklog.CheckoutSessionRequest{
//			QuoteID: *quote.QuoteID, Service: best.Service,
//		})
//		if err != nil {
//			return err
//		}
//		fmt.Println(session.HandoffURL) // the human pays here
//		if session.SessionID != nil {   // nil only when the record was not stored
//			status, err := client.GetCheckoutStatus(ctx, *session.SessionID)
//			if err != nil {
//				return err
//			}
//			fmt.Println(status.Status) // awaiting_checkout ... label_ready
//		}
//	}
//
// Every GetParcelQuote call spends a live carrier rating that costs SMKlog
// money: one call per shipment, cache on your side, never poll. A priced
// answer carries QuoteID; pass it to CreatePaymentSession so the pair costs
// one carrier call. CreatePaymentSession also takes the shipment itself
// (Product, FromZip, ToZip, optional ToCountry and Quantity) and prices it
// live. A QuoteID older than 15 minutes makes the API price the shipment
// fields live too, so send them alongside an old handle when you have them.
//
// Rate limits are per client and hourly (about 80 quotes, 60 checkout-status
// reads, 20 token requests). A rate limit (rate_limited or slow_down, normally
// a 429) comes back as an *APIError for which IsRateLimited reports true, and
// the client never retries it. Only the idempotent GETs (GetCheckoutStatus,
// Status) are retried, at most twice, on network failures and 5xx answers.
//
// The contract is the OpenAPI 3.1 document at
// https://quote-api.smklog.com/openapi.json; the tests replay the copy vendored
// in the repository's testdata/ folder and never reach the network.
package smklog
