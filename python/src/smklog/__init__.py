"""Official Python client for the SMKlog Quote API: https://smklog.com/developers/

    from smklog import SmklogClient
    quote = SmklogClient().get_parcel_quote(product="paperback books",
                                             from_postal_code="07030", to_postal_code="33101")
"""

from ._version import __version__
from .client import DEFAULT_BASE_URL, DEFAULT_TIMEOUT, SmklogClient
from .errors import (
    InvalidRequestError,
    NotFoundError,
    RateLimitedError,
    SessionFailedError,
    SmklogError,
)
from .models import (
    Checkout,
    CheckoutSession,
    CheckoutSessionRequest,
    CheckoutStatus,
    Liveness,
    Package,
    Problem,
    QuoteRequest,
    QuoteResponse,
    Rate,
)

__all__ = [
    "__version__",
    "DEFAULT_BASE_URL",
    "DEFAULT_TIMEOUT",
    "SmklogClient",
    "SmklogError",
    "RateLimitedError",
    "InvalidRequestError",
    "NotFoundError",
    "SessionFailedError",
    "QuoteRequest",
    "QuoteResponse",
    "Rate",
    "Package",
    "CheckoutSessionRequest",
    "CheckoutSession",
    "Checkout",
    "CheckoutStatus",
    "Liveness",
    "Problem",
]
