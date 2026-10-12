"""The client against a local replay of testdata/samples (DESIGN.md, "Tests", item 1)."""

import unittest
from unittest import mock

import support
from support import ReplayServer, problem, request_example, response_example

import smklog
from smklog import (
    CheckoutSession,
    CheckoutSessionRequest,
    CheckoutStatus,
    InvalidRequestError,
    Liveness,
    NotFoundError,
    QuoteRequest,
    QuoteResponse,
    Rate,
    RateLimitedError,
    SessionFailedError,
    SmklogClient,
    SmklogError,
)
from smklog import client as client_module

SESSION_ID = "as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33"
STATUS_PATH = "/agent/checkout-session/" + SESSION_ID


class ReplayCase(unittest.TestCase):
    def setUp(self) -> None:
        self.server = ReplayServer()
        self.addCleanup(self.server.close)
        self.client = SmklogClient(base_url=self.server.base_url, timeout=5)
        # Retries would otherwise pause the suite; the count of attempts is what matters.
        patcher = mock.patch.object(client_module, "RETRY_BACKOFF_SECONDS", (0, 0))
        patcher.start()
        self.addCleanup(patcher.stop)


class GetParcelQuoteTests(ReplayCase):
    def test_described_example_round_trips(self) -> None:
        example = request_example("get-parcel-quote-request", "described")
        answer = response_example("get-parcel-quote-responses", 200, "priced")
        self.server.expect("POST", "/quote", 200, answer)

        quote = self.client.get_parcel_quote(**example)

        sent = self.server.requests_for("POST", "/quote")
        self.assertEqual(len(sent), 1)
        self.assertEqual(sent[0].body, example, "the body must be the example, nothing added")
        self.assertEqual(sent[0].headers["Content-Type"], "application/json")
        self.assertEqual(sent[0].headers["Accept"], "application/json")
        self.assertTrue(sent[0].headers["User-Agent"].startswith("smklog-sdk-python/0.1.0"))
        self.assertNotIn("Authorization", sent[0].headers)

        self.assertIsInstance(quote, QuoteResponse)
        self.assertEqual(quote.mode, "parcel_label_ready")
        self.assertTrue(quote.is_priced())
        self.assertEqual(quote.identified_product_title, answer["identified_product_title"])
        self.assertEqual(quote.package.length_cm, 112)
        self.assertEqual(quote.package.confidence, "medium")
        self.assertEqual([r.carrier for r in quote.rates], ["USPS", "UPS"])
        self.assertIsInstance(quote.rates[1], Rate)
        self.assertIsNone(quote.rates[1].delivery_window)
        self.assertEqual(quote.rates[1].delivery_estimated_date, "2026-10-16")
        self.assertEqual(quote.cheapest().amount, 38.42)
        self.assertEqual(quote.source, answer["source"])
        self.assertEqual(quote.extra, {})
        self.assertEqual(quote.raw, answer)

    def test_measured_example_round_trips_from_a_request_model(self) -> None:
        example = request_example("get-parcel-quote-request", "measured")
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        self.client.get_parcel_quote(QuoteRequest(**example))

        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body, example)

    def test_measurements_mark_the_package_as_manual(self) -> None:
        example = dict(request_example("get-parcel-quote-request", "measured"))
        del example["package_source"]
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        self.client.get_parcel_quote(example)

        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body["package_source"], "manual")

    def test_quantity_defaults_to_one(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        self.client.get_parcel_quote(product="paperback books", from_postal_code="07030", to_postal_code="33101")

        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body["quantity"], 1)

    def test_not_a_parcel_is_not_priced(self) -> None:
        answer = response_example("get-parcel-quote-responses", 200, "notAParcel")
        self.server.expect("POST", "/quote", 200, answer)

        quote = self.client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(quote.mode, "not_a_parcel")
        self.assertFalse(quote.is_priced())
        self.assertIsNone(quote.cheapest())
        self.assertEqual(quote.rates, [])
        self.assertEqual(quote.notice, answer["notice"])

    def test_unknown_fields_are_kept_in_extra(self) -> None:
        answer = response_example("get-parcel-quote-responses", 200, "priced")
        answer["quote_id"] = "q_8f3c2e1a4b7d"
        answer["rates"][0]["surcharge_note"] = "peak season"
        self.server.expect("POST", "/quote", 200, answer)

        quote = self.client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(quote.extra, {"quote_id": "q_8f3c2e1a4b7d"})
        self.assertEqual(quote.rates[0].extra, {"surcharge_note": "peak season"})
        self.assertEqual(quote.rates[0].to_dict()["surcharge_note"], "peak season")
        self.assertEqual(quote.raw, answer)

    def test_400_missing_required_fields_is_invalid_request(self) -> None:
        answer = response_example("get-parcel-quote-responses", 400)
        self.server.expect("POST", "/quote", 400, answer)

        with self.assertRaises(InvalidRequestError) as caught:
            self.client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(caught.exception.status, 400)
        self.assertEqual(caught.exception.error, "missing_required_fields")
        self.assertEqual(caught.exception.missing, ["to_postal_code"])
        self.assertEqual(caught.exception.body, answer)

    def test_422_product_url_not_detected_is_invalid_request(self) -> None:
        answer = response_example("get-parcel-quote-responses", 422)
        self.server.expect("POST", "/quote", 422, answer)

        with self.assertRaises(InvalidRequestError) as caught:
            self.client.get_parcel_quote(product_url="https://smklog.com/", from_postal_code="07102", to_postal_code="30303")

        self.assertEqual(caught.exception.error, "product_url_not_detected")
        self.assertEqual(caught.exception.message, answer["message"])
        self.assertEqual(caught.exception.missing, ["product"])

    def test_429_raises_rate_limited_without_a_retry(self) -> None:
        self.server.expect("POST", "/quote", 429, response_example("get-parcel-quote-responses", 429))
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        with self.assertRaises(RateLimitedError) as caught:
            self.client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(caught.exception.status, 429)
        self.assertEqual(len(self.server.requests_for("POST", "/quote")), 1)

    def test_5xx_on_a_post_is_never_retried(self) -> None:
        self.server.expect("POST", "/quote", 503, problem("upstream_unavailable", "Carriers are not answering."))
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        with self.assertRaises(SmklogError) as caught:
            self.client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(len(self.server.requests_for("POST", "/quote")), 1)

    def test_timeout_is_a_network_error(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"), delay=1.0)
        client = SmklogClient(base_url=self.server.base_url, timeout=0.2)

        with self.assertRaises(SmklogError) as caught:
            client.get_parcel_quote(**request_example("get-parcel-quote-request", "described"))

        self.assertEqual(caught.exception.status, 0)
        self.assertEqual(caught.exception.error, "network_error")
        self.assertIsInstance(caught.exception.__cause__, OSError)


class LocalValidationTests(ReplayCase):
    def assert_rejected_locally(self, message_part: str, **fields) -> InvalidRequestError:
        with self.assertRaises(InvalidRequestError) as caught:
            self.client.get_parcel_quote(**fields)
        self.assertEqual(caught.exception.status, 0, "a local rejection has no HTTP status")
        self.assertIn(message_part, caught.exception.message)
        self.assertEqual(self.server.exchanges, [], "a rejected request must not reach the server")
        return caught.exception

    def test_four_digit_zip_is_rejected_locally(self) -> None:
        self.assert_rejected_locally("5-digit", product="paperback books", from_postal_code="0710", to_postal_code="33101")

    def test_us_destination_needs_a_five_digit_zip(self) -> None:
        self.assert_rejected_locally("to_postal_code", product="paperback books", from_postal_code="07030", to_postal_code="SW1A 1AA")

    def test_other_destination_countries_take_their_own_postal_codes(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))
        self.client.get_parcel_quote(product="paperback books", from_postal_code="07030",
                                     to_postal_code="SW1A 1AA", to_country_code="GB")
        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body["to_country_code"], "GB")

    def test_unsupported_country_is_rejected_locally(self) -> None:
        self.assert_rejected_locally("to_country_code", product="paperback books", from_postal_code="07030",
                                     to_postal_code="75001", to_country_code="FR")

    def test_quantity_below_one_is_rejected_locally(self) -> None:
        self.assert_rejected_locally("quantity", product="paperback books", from_postal_code="07030",
                                     to_postal_code="33101", quantity=0)

    def test_partial_measurements_are_rejected_locally(self) -> None:
        self.assert_rejected_locally("go together", product="paperback books", from_postal_code="07030",
                                     to_postal_code="33101", length_cm=30, width_cm=22)

    def test_missing_product_and_zip_are_named(self) -> None:
        exc = self.assert_rejected_locally("Required", to_postal_code="33101")
        self.assertEqual(exc.error, "missing_required_fields")
        self.assertEqual(exc.missing, ["from_postal_code", "product"])


class CreatePaymentSessionTests(ReplayCase):
    def test_from_quote_example_round_trips(self) -> None:
        example = request_example("create-payment-session-request", "fromQuote")
        answer = response_example("create-payment-session-responses", 200)
        self.server.expect("POST", "/agent/checkout-session", 200, answer)

        session = self.client.create_payment_session(**example)

        self.assertEqual(self.server.requests_for("POST", "/agent/checkout-session")[0].body, example)
        self.assertIsInstance(session, CheckoutSession)
        self.assertEqual(session.intent, "session")
        self.assertEqual(session.session_id, SESSION_ID)
        self.assertEqual(session.amount, 38.42)
        self.assertEqual(session.method, ["stripe", "card"])
        self.assertTrue(session.quote_reused)
        self.assertEqual(session.handoff_url, answer["handoff_url"])
        self.assertEqual(session.raw, answer)

    def test_from_shipment_example_round_trips(self) -> None:
        example = request_example("create-payment-session-request", "fromShipment")
        self.server.expect("POST", "/agent/checkout-session", 200, response_example("create-payment-session-responses", 200))

        self.client.create_payment_session(CheckoutSessionRequest(**example))

        self.assertEqual(self.server.requests_for("POST", "/agent/checkout-session")[0].body, example)

    def test_neither_quote_id_nor_shipment_is_rejected_locally(self) -> None:
        with self.assertRaises(InvalidRequestError) as caught:
            self.client.create_payment_session(service="USPS Ground Advantage")
        self.assertEqual(caught.exception.missing, ["product", "from_zip", "to_zip"])
        self.assertEqual(self.server.exchanges, [])

    def test_400_invalid_request(self) -> None:
        self.server.expect("POST", "/agent/checkout-session", 400, response_example("create-payment-session-responses", 400))
        with self.assertRaises(InvalidRequestError) as caught:
            self.client.create_payment_session(quote_id="q_8f3c2e1a4b7d")
        self.assertEqual(caught.exception.error, "invalid_request")

    def test_422_session_failed(self) -> None:
        answer = response_example("create-payment-session-responses", 422)
        self.server.expect("POST", "/agent/checkout-session", 422, answer)
        with self.assertRaises(SessionFailedError) as caught:
            self.client.create_payment_session(quote_id="q_8f3c2e1a4b7d")
        self.assertEqual(caught.exception.status, 422)
        self.assertEqual(caught.exception.message, answer["message"])

    def test_429_rate_limited(self) -> None:
        self.server.expect("POST", "/agent/checkout-session", 429, response_example("create-payment-session-responses", 429))
        with self.assertRaises(RateLimitedError):
            self.client.create_payment_session(quote_id="q_8f3c2e1a4b7d")
        self.assertEqual(len(self.server.exchanges), 1)


class GetCheckoutStatusTests(ReplayCase):
    def test_example_round_trips(self) -> None:
        answer = response_example("get-checkout-status-responses", 200)
        self.server.expect("GET", STATUS_PATH, 200, answer)

        status = self.client.get_checkout_status(SESSION_ID)

        sent = self.server.requests_for("GET", STATUS_PATH)
        self.assertEqual(len(sent), 1)
        self.assertIsNone(sent[0].body)
        self.assertNotIn("Content-Type", sent[0].headers)
        self.assertIsInstance(status, CheckoutStatus)
        self.assertEqual(status.status, "label_ready")
        self.assertEqual(status.checkout.tracking_number, "9400100000000000000000")
        self.assertTrue(status.checkout.paid)
        self.assertIsNone(status.checkout.delivered_at)
        self.assertEqual(status.next, answer["next"])
        self.assertEqual(status.raw, answer)

    def test_session_id_is_url_escaped(self) -> None:
        self.server.expect("GET", "/agent/checkout-session/as_a%2Fb%3Fc", 200, response_example("get-checkout-status-responses", 200))
        self.client.get_checkout_status("as_a/b?c")
        self.assertEqual(len(self.server.requests_for("GET", "/agent/checkout-session/as_a%2Fb%3Fc")), 1)

    def test_404_session_not_found(self) -> None:
        answer = response_example("get-checkout-status-responses", 404)
        self.server.expect("GET", STATUS_PATH, 404, answer)
        with self.assertRaises(NotFoundError) as caught:
            self.client.get_checkout_status(SESSION_ID)
        self.assertEqual(caught.exception.status, 404)
        self.assertEqual(caught.exception.error, "session_not_found")
        self.assertEqual(caught.exception.message, answer["message"])

    def test_429_is_not_retried_even_on_a_get(self) -> None:
        self.server.expect("GET", STATUS_PATH, 429, response_example("get-checkout-status-responses", 429))
        self.server.expect("GET", STATUS_PATH, 200, response_example("get-checkout-status-responses", 200))
        with self.assertRaises(RateLimitedError):
            self.client.get_checkout_status(SESSION_ID)
        self.assertEqual(len(self.server.requests_for("GET", STATUS_PATH)), 1)

    def test_empty_session_id_is_rejected_locally(self) -> None:
        with self.assertRaises(InvalidRequestError):
            self.client.get_checkout_status("")
        self.assertEqual(self.server.exchanges, [])


class StatusTests(ReplayCase):
    def test_example_round_trips(self) -> None:
        answer = response_example("status-responses", 200)
        self.server.expect("GET", "/status", 200, answer)

        liveness = self.client.status()

        self.assertIsInstance(liveness, Liveness)
        self.assertTrue(liveness.ok)
        self.assertEqual(liveness.service, "smklog-quote-api")
        self.assertEqual(liveness.version, "1.4.0")
        self.assertIsNone(liveness.last_estimate_race)
        self.assertEqual(liveness.raw, answer)

    def test_503_is_retried_then_succeeds(self) -> None:
        self.server.expect("GET", "/status", 503, problem("unavailable", "Warming up."))
        self.server.expect("GET", "/status", 200, response_example("status-responses", 200))

        liveness = self.client.status()

        self.assertTrue(liveness.ok)
        self.assertEqual(len(self.server.requests_for("GET", "/status")), 2)

    def test_non_json_5xx_is_retried_then_raised_as_unexpected_response(self) -> None:
        for _ in range(3):
            self.server.expect("GET", "/status", 502, "<html>bad gateway</html>", content_type="text/html")

        with self.assertRaises(SmklogError) as caught:
            self.client.status()

        self.assertEqual(caught.exception.status, 502)
        self.assertEqual(caught.exception.error, "unexpected_response")
        self.assertEqual(caught.exception.body, "<html>bad gateway</html>")
        self.assertEqual(len(self.server.requests_for("GET", "/status")), 3, "initial try plus two retries")

    def test_429_without_a_problem_body_is_still_rate_limited(self) -> None:
        self.server.expect("GET", "/status", 429, "slow down", content_type="text/plain")
        with self.assertRaises(RateLimitedError):
            self.client.status()
        self.assertEqual(len(self.server.exchanges), 1)

    def test_unknown_code_is_the_base_error(self) -> None:
        self.server.expect("GET", "/status", 401, problem("invalid_token", "Bearer token not recognized."))
        with self.assertRaises(SmklogError) as caught:
            self.client.status()
        self.assertIs(type(caught.exception), SmklogError)
        self.assertEqual(caught.exception.error, "invalid_token")

    def test_connection_refused_is_a_network_error_after_retries(self) -> None:
        client = SmklogClient(base_url="http://127.0.0.1:%d" % support.unused_port(), timeout=2)
        with self.assertRaises(SmklogError) as caught:
            client.status()
        self.assertEqual(caught.exception.status, 0)
        self.assertEqual(caught.exception.error, "network_error")
        self.assertIsNotNone(caught.exception.__cause__)


class HeaderTests(ReplayCase):
    def test_token_and_user_agent_suffix(self) -> None:
        self.server.expect("GET", "/status", 200, response_example("status-responses", 200))
        client = SmklogClient(base_url=self.server.base_url + "/", token="abc123", user_agent="my-agent/2.0")

        client.status()

        sent = self.server.requests_for("GET", "/status")[0]
        self.assertEqual(sent.headers["Authorization"], "Bearer abc123")
        self.assertEqual(sent.headers["User-Agent"], "smklog-sdk-python/%s my-agent/2.0" % smklog.__version__)
        self.assertEqual(sent.headers["Accept"], "application/json")

    def test_defaults(self) -> None:
        client = SmklogClient()
        self.assertEqual(client.base_url, "https://quote-api.smklog.com")
        self.assertEqual(client.timeout, 30.0)
        self.assertIsNone(client.token)
        self.assertEqual(client.user_agent, "smklog-sdk-python/" + smklog.__version__)


class ErrorTextTests(unittest.TestCase):
    def test_str_carries_code_status_message_and_missing(self) -> None:
        exc = InvalidRequestError(400, "missing_required_fields", "Required fields are missing.", ["to_postal_code"])
        self.assertEqual(str(exc), "missing_required_fields (HTTP 400): Required fields are missing. [missing: to_postal_code]")
        self.assertEqual(exc.problem(), {"error": "missing_required_fields", "message": "Required fields are missing.",
                                         "missing": ["to_postal_code"]})

    def test_network_error_has_no_status(self) -> None:
        self.assertEqual(str(SmklogError(0, "network_error", "timed out")), "network_error: timed out")


if __name__ == "__main__":
    unittest.main()
