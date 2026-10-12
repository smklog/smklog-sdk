"""The client against the vendored OpenAPI document (DESIGN.md, "Tests", item 2)."""

import os
import re
import unittest

import support

import smklog
from smklog import SmklogClient
from smklog.errors import ERROR_TYPES

HTTP_METHODS = {"get", "put", "post", "delete", "options", "head", "patch", "trace"}


def snake_case(operation_id: str) -> str:
    # getParcelQuote -> get_parcel_quote: the one rule that maps every operationId to
    # its Python method, so a new operation in the document fails here, not in a user's stack trace.
    return re.sub(r"(?<!^)(?=[A-Z])", "_", operation_id).lower()


class ContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.document = support.load_json("openapi.json")

    def operations(self):
        for path, item in self.document["paths"].items():
            for method, operation in item.items():
                if method in HTTP_METHODS:
                    yield method.upper(), path, operation

    def test_every_documented_operation_has_a_client_method(self) -> None:
        seen = []
        for method, path, operation in self.operations():
            name = snake_case(operation["operationId"])
            self.assertTrue(callable(getattr(SmklogClient, name, None)),
                            "%s %s (%s) has no SmklogClient.%s" % (method, path, operation["operationId"], name))
            seen.append(name)
        self.assertEqual(sorted(seen), ["create_payment_session", "get_checkout_status", "get_parcel_quote", "status"])

    def test_default_base_url_is_the_documented_server(self) -> None:
        self.assertEqual(SmklogClient().base_url, self.document["servers"][0]["url"].rstrip("/"))
        self.assertEqual(smklog.DEFAULT_BASE_URL, "https://quote-api.smklog.com")

    def test_every_problem_code_in_the_document_has_a_home(self) -> None:
        description = self.document["components"]["schemas"]["Problem"]["properties"]["error"]["description"]
        codes = re.findall(r"[a-z]+(?:_[a-z]+)+", description)
        self.assertIn("rate_limited", codes)
        for code in codes:
            # invalid_token has no specialization by design: it is the base error.
            self.assertIn(ERROR_TYPES.get(code, smklog.SmklogError).__name__,
                          {"RateLimitedError", "InvalidRequestError", "NotFoundError", "SessionFailedError", "SmklogError"})

    def test_sdk_version_is_pinned_everywhere(self) -> None:
        pyproject = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "pyproject.toml")
        with open(pyproject, encoding="utf-8") as handle:
            text = handle.read()
        match = re.search(r'^version = "([^"]+)"$', text, re.MULTILINE)
        self.assertIsNotNone(match, "pyproject.toml must pin a static version")
        self.assertEqual(match.group(1), smklog.__version__)
        self.assertEqual(smklog.__version__, "0.1.0")

    def test_request_schema_rules_are_the_ones_the_client_enforces(self) -> None:
        schemas = self.document["components"]["schemas"]
        quote = schemas["QuoteRequest"]["properties"]
        self.assertEqual(quote["from_postal_code"]["pattern"], "^[0-9]{5}$")
        self.assertEqual(tuple(quote["to_country_code"]["enum"]), smklog.client.COUNTRIES)
        self.assertEqual(quote["quantity"]["minimum"], 1)
        for name in smklog.client.MEASUREMENTS:
            self.assertIn(name, quote)
        session = schemas["CheckoutSessionRequest"]["properties"]
        self.assertEqual(session["from_zip"]["pattern"], "^[0-9]{5}$")
        self.assertEqual(tuple(session["to_country"]["enum"]), smklog.client.COUNTRIES)


if __name__ == "__main__":
    unittest.main()
