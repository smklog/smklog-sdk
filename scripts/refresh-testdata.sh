#!/usr/bin/env bash
# Refreshes the vendored contract, schemas and example payloads from the live
# SMKlog surface. Run it when the API changes, then run every language's tests:
# a field that moved shows up here first, in a test, not in a user's stack trace.
set -euo pipefail
cd "$(dirname "$0")/.."
curl -fsS https://quote-api.smklog.com/openapi.json -o testdata/openapi.json
for schema in quote-request package rate quote-response checkout-session-request checkout-session checkout-status liveness problem; do
  curl -fsS "https://smklog.com/developers/schemas/${schema}.schema.json" -o "testdata/schemas/${schema}.schema.json"
done
curl -fsS https://smklog.com/developers/schemas/context.jsonld -o testdata/schemas/context.jsonld
for sample in get-parcel-quote-request get-parcel-quote-responses create-payment-session-request create-payment-session-responses get-checkout-status-responses status-responses; do
  curl -fsS "https://smklog.com/developers/samples/${sample}.json" -o "testdata/samples/${sample}.json"
done
echo "testdata refreshed from the live contract; now run the tests in javascript/, python/ and go/"
