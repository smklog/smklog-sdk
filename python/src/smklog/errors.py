"""The error taxonomy: one base exception and four subclasses picked by the Problem code."""

from typing import Any, Dict, Optional, Sequence, Type


class SmklogError(Exception):
    """Any failure the client raises.

    ``status`` is the HTTP status, or 0 when no HTTP exchange happened: a network
    failure or timeout (``error == "network_error"``) or a request the client refused
    locally before spending a call. ``error`` is the stable code from the Problem body,
    ``message`` its sentence, ``missing`` the field names it asked for (often empty)
    and ``body`` the response body as received (a dict when it was JSON).
    """

    def __init__(
        self,
        status: int,
        error: str,
        message: str = "",
        missing: Optional[Sequence[str]] = None,
        body: Any = None,
    ) -> None:
        self.status = status
        self.error = error
        self.message = message or ""
        self.missing = [str(name) for name in (missing or [])]
        self.body = body
        super().__init__(str(self))

    def __reduce__(self):
        # Exception's default pickling replays ``args`` (the rendered text) into
        # __init__, which wants the five fields; rebuild from them instead so an
        # error crosses a multiprocessing boundary intact.
        return (type(self), (self.status, self.error, self.message, self.missing, self.body))

    def __str__(self) -> str:
        text = self.error
        if self.status:
            text += " (HTTP %d)" % self.status
        if self.message:
            text += ": " + self.message
        if self.missing:
            text += " [missing: " + ", ".join(self.missing) + "]"
        return text

    def problem(self) -> Dict[str, Any]:
        """The Problem shape for this error, rebuilt when the server sent none."""
        if isinstance(self.body, dict) and "error" in self.body:
            return dict(self.body)
        out: Dict[str, Any] = {"error": self.error, "message": self.message}
        if self.missing:
            out["missing"] = list(self.missing)
        return out


class RateLimitedError(SmklogError):
    """429: the hourly bucket is spent. The SDK never retries this; wait, then decide."""


class InvalidRequestError(SmklogError):
    """The request cannot be served as written: missing fields, bad JSON, an item or
    URL the estimator could not recognize, or a value this client rejected locally."""


class NotFoundError(SmklogError):
    """404: no payment session with that id (sessions answer for 30 days)."""


class SessionFailedError(SmklogError):
    """422: the shipment cannot become a checkout (past parcel limits, restricted,
    more than one box, or unpriceable)."""


ERROR_TYPES: Dict[str, Type[SmklogError]] = {
    "rate_limited": RateLimitedError,
    "slow_down": RateLimitedError,
    "product_not_recognized": InvalidRequestError,
    "product_url_not_detected": InvalidRequestError,
    "missing_required_fields": InvalidRequestError,
    "invalid_request": InvalidRequestError,
    "invalid_json": InvalidRequestError,
    "session_not_found": NotFoundError,
    "session_failed": SessionFailedError,
}


def error_for(status: int, body: Any) -> SmklogError:
    """Builds the error for a non-2xx answer; the Problem's code picks the type.

    A 429 without a Problem body (an edge rate limit answers in HTML) still becomes a
    RateLimitedError, because the caller's reaction is the same either way. A Problem
    may carry no sentence (missing_required_fields lists fields instead), so the
    message is then the code plus the missing fields and a log line still says what
    went wrong.
    """
    problem = body if isinstance(body, dict) else {}
    code = problem.get("error")
    if not isinstance(code, str) or not code:
        code = "rate_limited" if status == 429 else "unexpected_response"
    missing = problem.get("missing")
    if not isinstance(missing, list):
        missing = None
    message = problem.get("message")
    if not isinstance(message, str) or not message:
        if not problem:
            message = "HTTP %d with a non-JSON body" % status
        elif missing:
            message = "%s: missing %s" % (code, ", ".join(str(name) for name in missing))
        else:
            message = code
    return ERROR_TYPES.get(code, SmklogError)(status, code, message, missing, body)
