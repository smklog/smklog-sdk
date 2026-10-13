"""The client against the vendored contract: every operation the OpenAPI document
declares has a client method, every documented problem code maps to its exception,
every schema property is a typed model field, and the local checks are the schema's."""

import dataclasses
import glob
import os
import re
import unittest

import support

import smklog
from smklog import SmklogClient
from smklog import models
from smklog.errors import ERROR_TYPES, error_for

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

    def test_every_problem_code_maps_to_its_exception(self) -> None:
        expected = {
            "rate_limited": smklog.RateLimitedError,
            "slow_down": smklog.RateLimitedError,
            "product_not_recognized": smklog.InvalidRequestError,
            "product_url_not_detected": smklog.InvalidRequestError,
            "missing_required_fields": smklog.InvalidRequestError,
            "invalid_request": smklog.InvalidRequestError,
            "invalid_json": smklog.InvalidRequestError,
            "session_not_found": smklog.NotFoundError,
            "session_failed": smklog.SessionFailedError,
            # invalid_token has no specialization of its own: it is the base error.
            "invalid_token": smklog.SmklogError,
        }
        description = self.document["components"]["schemas"]["Problem"]["properties"]["error"]["description"]
        documented = set(re.findall(r"[a-z]+(?:_[a-z]+)+", description))
        self.assertEqual(documented, set(expected) - {"slow_down"},
                         "a code appeared in or left the document; decide its exception here")
        self.assertEqual(ERROR_TYPES, {code: cls for code, cls in expected.items() if cls is not smklog.SmklogError})
        for code, cls in expected.items():
            self.assertIs(type(error_for(400, {"error": code})), cls, code)
        self.assertIs(type(error_for(500, {"error": "something_new"})), smklog.SmklogError)

    def test_every_schema_property_is_a_model_field(self) -> None:
        # The model for each schema is its title; the nested checkout object has no
        # schema file of its own and is covered through CheckoutStatus.
        for path in sorted(glob.glob(os.path.join(support.TESTDATA, "schemas", "*.schema.json"))):
            schema = support.load_json("schemas", os.path.basename(path))
            model = getattr(models, schema["title"])
            declared = {f.name for f in dataclasses.fields(model) if f.init}
            self.assertEqual(set(schema["properties"]) - declared, set(),
                             "%s names properties %s has no field for" % (os.path.basename(path), schema["title"]))
        quote_id = self.document["components"]["schemas"]["QuoteResponse"]["properties"]["quote_id"]
        self.assertEqual(quote_id["type"], ["string", "null"])
        self.assertIsNone(models.QuoteResponse().quote_id)

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
