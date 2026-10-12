"""The smklog command line against the replay server, by calling main() with argv."""

import contextlib
import io
import json
import unittest
from typing import Tuple
from unittest import mock

from support import ReplayServer, problem, request_example, response_example

from smklog import client as client_module
from smklog.cli import main

SESSION_ID = "as_3f9c1b2e-7d64-4a8e-9c0f-5b2d1e8a7c33"
GUITAR = "Yamaha FG800 acoustic guitar in a hard case"


class CliCase(unittest.TestCase):
    def setUp(self) -> None:
        self.server = ReplayServer()
        self.addCleanup(self.server.close)
        patcher = mock.patch.object(client_module, "RETRY_BACKOFF_SECONDS", (0, 0))
        patcher.start()
        self.addCleanup(patcher.stop)

    def run_cli(self, *argv: str) -> Tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = main(list(argv) + ["--base-url", self.server.base_url])
        return code, out.getvalue(), err.getvalue()


class QuoteCommandTests(CliCase):
    def test_table_output_and_exit_0(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        code, out, err = self.run_cli("quote", GUITAR, "07102", "30303")

        self.assertEqual(code, 0, err)
        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body,
                         request_example("get-parcel-quote-request", "described"))
        lines = out.splitlines()
        self.assertEqual(lines[0], GUITAR)
        self.assertEqual(lines[1], "Box: 112 x 48 x 20 cm, 6.4 kg (estimate, medium confidence)")
        self.assertRegex(lines[2], r"^Carrier\s+Service\s+Checkout\s+Counter\s+Delivery$")
        self.assertRegex(lines[3], r"^USPS\s+Ground Advantage\s+\$38\.42\s+\$46\.95\s+2-5 business days$")
        self.assertRegex(lines[4], r"^UPS\s+Ground\s+\$41\.10\s+\$67\.35\s+3 days, by 2026-10-16$")
        self.assertEqual(err, "")

    def test_json_output_is_the_raw_answer(self) -> None:
        answer = response_example("get-parcel-quote-responses", 200, "priced")
        self.server.expect("POST", "/quote", 200, answer)

        code, out, _ = self.run_cli("quote", GUITAR, "07102", "30303", "--json")

        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out), answer)

    def test_dims_and_kg_send_the_measured_example(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        code, _, _ = self.run_cli("quote", "paperback books", "07030", "33101", "--dims", "30x22x12", "--kg", "3.2")

        self.assertEqual(code, 0)
        self.assertEqual(self.server.requests_for("POST", "/quote")[0].body,
                         request_example("get-parcel-quote-request", "measured"))

    def test_country_and_qty_reach_the_body(self) -> None:
        self.server.expect("POST", "/quote", 200, response_example("get-parcel-quote-responses", 200, "priced"))

        self.run_cli("quote", "paperback books", "07030", "M5V 3L9", "--country", "ca", "--qty", "2")

        body = self.server.requests_for("POST", "/quote")[0].body
        self.assertEqual(body["to_country_code"], "CA")
        self.assertEqual(body["quantity"], 2)

    def test_dims_without_kg_is_refused_before_any_request(self) -> None:
        code, _, err = self.run_cli("quote", "paperback books", "07030", "33101", "--dims", "30x22x12")
        self.assertEqual(code, 2)
        self.assertIn("--dims and --kg go together", err)
        self.assertEqual(self.server.exchanges, [])

    def test_unpriced_answer_prints_the_reason_and_exits_2(self) -> None:
        answer = response_example("get-parcel-quote-responses", 200, "notAParcel")
        self.server.expect("POST", "/quote", 200, answer)

        code, out, _ = self.run_cli("quote", "Honda CB500F motorcycle", "07102", "30303")

        self.assertEqual(code, 2)
        self.assertIn("No price: not_a_parcel.", out)
        self.assertIn(answer["notice"], out)

    def test_api_refusal_prints_the_problem_and_exits_2(self) -> None:
        self.server.expect("POST", "/quote", 400, response_example("get-parcel-quote-responses", 400))

        code, out, err = self.run_cli("quote", GUITAR, "07102", "30303")

        self.assertEqual(code, 2)
        self.assertEqual(out, "")
        self.assertIn("error: missing_required_fields (HTTP 400)", err)
        self.assertIn("missing: to_postal_code", err)

    def test_api_refusal_with_json_prints_the_problem_as_json(self) -> None:
        answer = response_example("get-parcel-quote-responses", 422)
        self.server.expect("POST", "/quote", 422, answer)

        code, out, _ = self.run_cli("quote", GUITAR, "07102", "30303", "--json")

        self.assertEqual(code, 2)
        self.assertEqual(json.loads(out), answer)

    def test_rate_limit_exits_3(self) -> None:
        self.server.expect("POST", "/quote", 429, response_example("get-parcel-quote-responses", 429))

        code, _, err = self.run_cli("quote", GUITAR, "07102", "30303")

        self.assertEqual(code, 3)
        self.assertIn("rate_limited", err)
        self.assertEqual(len(self.server.exchanges), 1)

    def test_four_digit_zip_is_refused_locally_with_exit_2(self) -> None:
        code, _, err = self.run_cli("quote", GUITAR, "0710", "30303")
        self.assertEqual(code, 2)
        self.assertIn("5-digit", err)
        self.assertEqual(self.server.exchanges, [])

    def test_unreachable_host_exits_1(self) -> None:
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = main(["status", "--base-url", "http://127.0.0.1:1"])
        self.assertEqual(code, 1)
        self.assertIn("network_error", err.getvalue())


class SessionCommandTests(CliCase):
    def test_session_from_quote_id(self) -> None:
        answer = response_example("create-payment-session-responses", 200)
        self.server.expect("POST", "/agent/checkout-session", 200, answer)

        code, out, _ = self.run_cli("session", "--quote-id", "q_8f3c2e1a4b7d", "--service", "USPS Ground Advantage")

        self.assertEqual(code, 0)
        self.assertEqual(self.server.requests_for("POST", "/agent/checkout-session")[0].body,
                         request_example("create-payment-session-request", "fromQuote"))
        self.assertIn("session_id    " + SESSION_ID, out)
        self.assertIn("amount        $38.42", out)
        self.assertIn("quote_reused  yes", out)
        self.assertIn(answer["handoff_url"], out)

    def test_session_json(self) -> None:
        answer = response_example("create-payment-session-responses", 200)
        self.server.expect("POST", "/agent/checkout-session", 200, answer)
        code, out, _ = self.run_cli("session", "--quote-id", "q_8f3c2e1a4b7d", "--json")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out), answer)

    def test_session_failed_exits_2(self) -> None:
        self.server.expect("POST", "/agent/checkout-session", 422, response_example("create-payment-session-responses", 422))
        code, _, err = self.run_cli("session", "--quote-id", "q_8f3c2e1a4b7d")
        self.assertEqual(code, 2)
        self.assertIn("session_failed (HTTP 422)", err)

    def test_quote_id_is_required(self) -> None:
        with self.assertRaises(SystemExit) as caught:
            with contextlib.redirect_stderr(io.StringIO()):
                main(["session"])
        self.assertEqual(caught.exception.code, 2)


class SessionStatusCommandTests(CliCase):
    def test_label_ready_table(self) -> None:
        answer = response_example("get-checkout-status-responses", 200)
        self.server.expect("GET", "/agent/checkout-session/" + SESSION_ID, 200, answer)

        code, out, _ = self.run_cli("session-status", SESSION_ID)

        self.assertEqual(code, 0)
        self.assertIn("status              label_ready", out)
        self.assertIn("tracking_number     9400100000000000000000", out)
        self.assertIn("carrier             USPS Ground Advantage", out)
        self.assertIn("paid                yes", out)
        self.assertNotIn("delivered_at", out, "null fields stay off the table")

    def test_not_found_exits_2(self) -> None:
        self.server.expect("GET", "/agent/checkout-session/as_missing", 404, response_example("get-checkout-status-responses", 404))
        code, _, err = self.run_cli("session-status", "as_missing")
        self.assertEqual(code, 2)
        self.assertIn("session_not_found (HTTP 404)", err)

    def test_rate_limit_exits_3(self) -> None:
        self.server.expect("GET", "/agent/checkout-session/" + SESSION_ID, 429, response_example("get-checkout-status-responses", 429))
        code, _, _ = self.run_cli("session-status", SESSION_ID)
        self.assertEqual(code, 3)


class StatusCommandTests(CliCase):
    def test_status_table(self) -> None:
        self.server.expect("GET", "/status", 200, response_example("status-responses", 200))
        code, out, _ = self.run_cli("status")
        self.assertEqual(code, 0)
        self.assertIn("ok       yes", out)
        self.assertIn("version  1.4.0", out)

    def test_status_json_after_a_retried_503(self) -> None:
        answer = response_example("status-responses", 200)
        self.server.expect("GET", "/status", 503, problem("unavailable", "Warming up."))
        self.server.expect("GET", "/status", 200, answer)
        code, out, _ = self.run_cli("status", "--json")
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out), answer)
        self.assertEqual(len(self.server.exchanges), 2)


class ParserTests(unittest.TestCase):
    def test_no_command_is_a_usage_error(self) -> None:
        with self.assertRaises(SystemExit) as caught:
            with contextlib.redirect_stderr(io.StringIO()):
                main([])
        self.assertEqual(caught.exception.code, 2)

    def test_version_flag(self) -> None:
        out = io.StringIO()
        with self.assertRaises(SystemExit) as caught:
            with contextlib.redirect_stdout(out):
                main(["--version"])
        self.assertEqual(caught.exception.code, 0)
        self.assertEqual(out.getvalue().strip(), "smklog-sdk 0.1.0")


if __name__ == "__main__":
    unittest.main()
