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
//	client := smklog.New()
//	quote, err := client.GetParcelQuote(ctx, smklog.QuoteRequest{
//		Product:        "Yamaha FG800 acoustic guitar in a hard case",
//		FromPostalCode: "07102",
//		ToPostalCode:   "30303",
//	})
//	if err != nil {
//		if smklog.IsRateLimited(err) {
//			// The hourly bucket is spent; the SDK never retries a 429.
//		}
//		return err
//	}
//	if best := quote.Cheapest(); best != nil {
//		fmt.Println(best.Carrier, best.Service, best.Amount)
//	}
//
// Every GetParcelQuote call spends a live carrier rating that costs SMKlog
// money: one call per shipment, cache on your side, never poll. Pass the
// quote's QuoteID to CreatePaymentSession so the pair costs one carrier call.
//
// Rate limits are per client and hourly (about 80 quotes, 60 checkout-status
// reads, 20 token requests). A 429 comes back as an *APIError for which
// IsRateLimited reports true, and the client never retries it. Only the
// idempotent GETs (GetCheckoutStatus, Status) are retried, at most twice, on
// network failures and 5xx answers.
//
// The contract is the OpenAPI 3.1 document at
// https://quote-api.smklog.com/openapi.json; the tests replay the copy vendored
// in the repository's testdata/ folder and never reach the network.
package smklog
