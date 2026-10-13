"""The client: four operations over urllib, nothing else."""

import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from http.client import HTTPException
from typing import Any, Dict, Mapping, Optional, Union

from ._version import __version__
from .errors import InvalidRequestError, SmklogError, error_for
from .models import CheckoutSession, CheckoutStatus, Liveness, Model, QuoteResponse

DEFAULT_BASE_URL = "https://quote-api.smklog.com"
# A quote can take 10-20 s while carriers answer, so the default is generous.
DEFAULT_TIMEOUT = 30.0
COUNTRIES = ("US", "CA", "GB", "DE", "AU")
MEASUREMENTS = ("length_cm", "width_cm", "height_cm", "weight_kg")
# One pause per retry, GETs only. POST /quote spends a live carrier rating and
# POST /agent/checkout-session opens a session, so neither is ever repeated by the SDK.
RETRY_BACKOFF_SECONDS = (0.5, 1.0)

_ZIP = re.compile(r"^[0-9]{5}$")
RequestLike = Union[Model, Mapping[str, Any], None]


class SmklogClient:
    """Talks to the SMKlog Quote API. No key needed; ``token`` is the optional OAuth
    bearer that buys a client its own rate-limit bucket."""

    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT,
        token: Optional[str] = None,
        user_agent: Optional[str] = None,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.token = token
        self.user_agent = "smklog-sdk-python/" + __version__
        if user_agent:
            self.user_agent += " " + user_agent

    def get_parcel_quote(self, request: RequestLike = None, **fields: Any) -> QuoteResponse:
        """POST /quote. Pass a QuoteRequest, a dict, or the fields as keywords.

        Costs a live carrier rating on the server: one call per shipment, never a poll.
        """
        body = _prepare_quote(_merge(request, fields))
        return QuoteResponse.from_dict(self._request("POST", "/quote", body))

    def create_payment_session(self, request: RequestLike = None, **fields: Any) -> CheckoutSession:
        """POST /agent/checkout-session. Give a ``quote_id`` (preferred: the quote is
        reused, no second carrier call) or the shipment itself; the SDK does not turn
        one into the other. A ``quote_id`` older than 15 minutes makes the API price
        the shipment fields live, so send them along when the quote may have aged."""
        body = _prepare_session(_merge(request, fields))
        return CheckoutSession.from_dict(self._request("POST", "/agent/checkout-session", body))

    def get_checkout_status(self, session_id: str) -> CheckoutStatus:
        """GET /agent/checkout-session/{session_id}."""
        if not isinstance(session_id, str) or not session_id.strip():
            raise InvalidRequestError(0, "invalid_request", "session_id must be a non-empty string.")
        path = "/agent/checkout-session/" + urllib.parse.quote(session_id, safe="")
        return CheckoutStatus.from_dict(self._request("GET", path, retry=True))

    def status(self) -> Liveness:
        """GET /status: liveness and the serving version."""
        return Liveness.from_dict(self._request("GET", "/status", retry=True))

    def _request(
        self, method: str, path: str, body: Optional[Dict[str, Any]] = None, retry: bool = False
    ) -> Dict[str, Any]:
        attempts = 1 + (len(RETRY_BACKOFF_SECONDS) if retry else 0)
        for attempt in range(attempts):
            try:
                return self._send(method, path, body)
            except SmklogError as exc:
                # Only a dropped connection or a server-side failure is worth a second
                # try; a 429 is deliberately excluded (retrying burns the bucket faster).
                transient = exc.status == 0 or exc.status >= 500
                if not transient or attempt + 1 == attempts:
                    raise
                time.sleep(RETRY_BACKOFF_SECONDS[attempt])
        raise AssertionError("unreachable")

    def _send(self, method: str, path: str, body: Optional[Dict[str, Any]]) -> Dict[str, Any]:
        url = self.base_url + path
        headers = {"Accept": "application/json", "User-Agent": self.user_agent}
        data = None
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as exc:
            status, raw = exc.code, exc.read()
        except (OSError, HTTPException) as exc:
            # URLError, socket timeouts and connection resets are all OSError.
            raise SmklogError(0, "network_error", "%s %s: %s" % (method, url, exc)) from exc
        parsed = _parse_json(raw)
        if 200 <= status < 300:
            if not isinstance(parsed, dict):
                raise SmklogError(status, "unexpected_response", "HTTP %d with a non-JSON body" % status, body=_text(raw))
            return parsed
        raise error_for(status, parsed if parsed is not None else _text(raw))


def _parse_json(raw: bytes) -> Any:
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        return None


def _text(raw: bytes) -> str:
    return raw.decode("utf-8", "replace")[:500]


def _merge(request: RequestLike, fields: Dict[str, Any]) -> Dict[str, Any]:
    if request is None:
        body: Dict[str, Any] = {}
    elif isinstance(request, Model):
        body = request.to_dict()
    elif isinstance(request, Mapping):
        body = dict(request)
    else:
        raise TypeError("request must be a model, a dict or None, got %s" % type(request).__name__)
    body.update(fields)
    # A None means "not given", so it never reaches the wire as null.
    return {name: value for name, value in body.items() if value is not None}


def _reject(message: str, code: str = "invalid_request", missing: Optional[list] = None) -> InvalidRequestError:
    return InvalidRequestError(0, code, message, missing)


def _prepare_quote(body: Dict[str, Any]) -> Dict[str, Any]:
    """Local checks that save a request the server would refuse anyway."""
    missing = [name for name in ("from_postal_code", "to_postal_code") if not body.get(name)]
    if not body.get("product") and not body.get("product_url"):
        missing.append("product")
    if missing:
        raise _reject("Required: " + ", ".join(missing) + ".", "missing_required_fields", missing)
    if not _is_zip(body["from_postal_code"]):
        raise _reject("from_postal_code must be a 5-digit US ZIP, got %r." % (body["from_postal_code"],))
    country = body.get("to_country_code", "US")
    if country not in COUNTRIES:
        raise _reject("to_country_code must be one of %s, got %r." % (", ".join(COUNTRIES), country))
    if country == "US" and not _is_zip(body["to_postal_code"]):
        raise _reject("to_postal_code must be a 5-digit US ZIP for a US destination, got %r." % (body["to_postal_code"],))
    body.setdefault("quantity", 1)
    _check_quantity(body["quantity"])
    given = [name for name in MEASUREMENTS if name in body]
    if given and len(given) != len(MEASUREMENTS):
        absent = [name for name in MEASUREMENTS if name not in body]
        raise _reject("length_cm, width_cm, height_cm and weight_kg go together; missing " + ", ".join(absent) + ".")
    if given:
        for name in MEASUREMENTS:
            if not _is_positive_number(body[name]):
                raise _reject("%s must be a positive number, got %r." % (name, body[name]))
        # Exact measurements are only honored when the request says so.
        body.setdefault("package_source", "manual")
    return body


def _prepare_session(body: Dict[str, Any]) -> Dict[str, Any]:
    if body.get("quote_id"):
        if not isinstance(body["quote_id"], str):
            raise _reject("quote_id must be a string, got %r." % (body["quote_id"],))
    else:
        missing = [name for name in ("product", "from_zip", "to_zip") if not body.get(name)]
        if missing:
            raise _reject("Without quote_id the shipment itself is required: " + ", ".join(missing) + ".",
                          "missing_required_fields", missing)
        if not _is_zip(body["from_zip"]):
            raise _reject("from_zip must be a 5-digit US ZIP, got %r." % (body["from_zip"],))
        country = body.get("to_country", "US")
        if country not in COUNTRIES:
            raise _reject("to_country must be one of %s, got %r." % (", ".join(COUNTRIES), country))
        if country == "US" and not _is_zip(body["to_zip"]):
            raise _reject("to_zip must be a 5-digit US ZIP for a US destination, got %r." % (body["to_zip"],))
    if "quantity" in body:
        _check_quantity(body["quantity"])
    return body


def _check_quantity(value: Any) -> None:
    if isinstance(value, bool) or not isinstance(value, int) or value < 1:
        raise _reject("quantity must be a whole number of 1 or more, got %r." % (value,))


def _is_zip(value: Any) -> bool:
    return isinstance(value, str) and bool(_ZIP.match(value))


def _is_positive_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and value > 0
