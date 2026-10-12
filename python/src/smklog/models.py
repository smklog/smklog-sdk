"""Typed views of the API's JSON shapes, one dataclass per schema in testdata/schemas.

Field names are the wire names (snake_case) so a model reads like the JSON it came
from. The API adds fields without a version bump, so ``from_dict`` never drops
anything: names this SDK does not know land in ``extra``, and ``raw`` keeps the body
exactly as received.
"""

from dataclasses import dataclass, field, fields
from typing import Any, Callable, ClassVar, Dict, List, Optional, Type, TypeVar

M = TypeVar("M", bound="Model")


@dataclass
class Model:
    """Shared behavior: build from a dict, serialize back, keep unknown fields."""

    # init=False keeps these off the constructor so subclasses may still declare
    # ordinary fields after them (dataclasses only order the init parameters).
    extra: Dict[str, Any] = field(default_factory=dict, init=False, compare=False)
    raw: Dict[str, Any] = field(default_factory=dict, init=False, repr=False, compare=False)

    # Field name -> converter for nested shapes; subclasses override.
    _nested: ClassVar[Dict[str, Callable[[Any], Any]]] = {}

    @classmethod
    def from_dict(cls: Type[M], data: Dict[str, Any]) -> M:
        if not isinstance(data, dict):
            raise TypeError("%s.from_dict expects a dict, got %s" % (cls.__name__, type(data).__name__))
        known = {f.name for f in fields(cls) if f.init}
        values = {}
        for name, value in data.items():
            if name not in known:
                continue
            convert = cls._nested.get(name)
            values[name] = convert(value) if convert and value is not None else value
        model = cls(**values)
        model.extra = {name: value for name, value in data.items() if name not in known}
        model.raw = dict(data)
        return model

    def to_dict(self) -> Dict[str, Any]:
        """JSON-ready dict: the fields that are set, nested models flattened, extra merged."""
        out: Dict[str, Any] = {}
        for f in fields(self):
            if not f.init:
                continue
            value = getattr(self, f.name)
            if value is not None:
                out[f.name] = _plain(value)
        out.update(self.extra)
        return out


def _plain(value: Any) -> Any:
    if isinstance(value, Model):
        return value.to_dict()
    if isinstance(value, list):
        return [_plain(item) for item in value]
    return value


def _list_of(model: Type[M]) -> Callable[[Any], Any]:
    # A list item that is not an object is passed through untouched rather than
    # failing the whole response: the typed view is a convenience, not a validator.
    def convert(items: Any) -> Any:
        if not isinstance(items, list):
            return items
        return [model.from_dict(item) if isinstance(item, dict) else item for item in items]

    return convert


def _one(model: Type[M]) -> Callable[[Any], Any]:
    def convert(item: Any) -> Any:
        return model.from_dict(item) if isinstance(item, dict) else item

    return convert


@dataclass
class QuoteRequest(Model):
    """One shipment to price (quote-request.schema.json)."""

    product: Optional[str] = None
    from_postal_code: Optional[str] = None
    to_postal_code: Optional[str] = None
    quantity: int = 1
    to_country_code: Optional[str] = None
    product_url: Optional[str] = None
    length_cm: Optional[float] = None
    width_cm: Optional[float] = None
    height_cm: Optional[float] = None
    weight_kg: Optional[float] = None
    package_source: Optional[str] = None


@dataclass
class Package(Model):
    """The packed box used for pricing (package.schema.json)."""

    quantity: Optional[int] = None
    length_cm: Optional[float] = None
    width_cm: Optional[float] = None
    height_cm: Optional[float] = None
    weight_kg: Optional[float] = None
    description: Optional[str] = None
    confidence: Optional[str] = None
    assumptions: Optional[List[str]] = None


@dataclass
class Rate(Model):
    """One purchasable service (rate.schema.json).

    ``amount`` is the checkout total the customer pays, SMKlog fee inside;
    ``retail_amount`` is the carrier's own counter price (0 when not published).
    """

    carrier: Optional[str] = None
    service: Optional[str] = None
    service_code: Optional[str] = None
    amount: Optional[float] = None
    retail_amount: Optional[float] = None
    delivery_days: Optional[int] = None
    delivery_window: Optional[str] = None
    delivery_estimated_date: Optional[str] = None
    currency: Optional[str] = None


@dataclass
class QuoteResponse(Model):
    """A priced parcel, or the reason it was not priced (quote-response.schema.json)."""

    mode: Optional[str] = None
    identified_product_title: Optional[str] = None
    package: Optional[Package] = None
    packages: Optional[List[Package]] = None
    rates: List[Rate] = field(default_factory=list)
    dangerous_goods: Optional[Dict[str, Any]] = None
    sources: Optional[List[Dict[str, Any]]] = None
    notice: Optional[str] = None
    source: Optional[Dict[str, Any]] = None

    _nested: ClassVar[Dict[str, Callable[[Any], Any]]] = {
        "package": _one(Package),
        "packages": _list_of(Package),
        "rates": _list_of(Rate),
    }

    def is_priced(self) -> bool:
        """True only when the shipment can be bought: a label-ready mode with a rate."""
        return self.mode == "parcel_label_ready" and bool(self.rates)

    def cheapest(self) -> Optional[Rate]:
        """The rate with the lowest checkout total; None when nothing was priced."""
        priced = [rate for rate in self.rates if isinstance(rate, Rate) and rate.amount is not None]
        return min(priced, key=lambda rate: rate.amount) if priced else None


@dataclass
class CheckoutSessionRequest(Model):
    """What to open a checkout for: a quote_id, or the shipment itself
    (checkout-session-request.schema.json)."""

    quote_id: Optional[str] = None
    service: Optional[str] = None
    product: Optional[str] = None
    from_zip: Optional[str] = None
    to_zip: Optional[str] = None
    to_country: Optional[str] = None
    quantity: Optional[int] = None


@dataclass
class CheckoutSession(Model):
    """A payment session, not a charge (checkout-session.schema.json)."""

    intent: Optional[str] = None
    session_id: Optional[str] = None
    method: Optional[List[str]] = None
    amount: Optional[float] = None
    currency: Optional[str] = None
    service: Optional[str] = None
    handoff_url: Optional[str] = None
    identified_product_title: Optional[str] = None
    quote_reused: Optional[bool] = None
    note: Optional[str] = None


@dataclass
class Checkout(Model):
    """The checkout a human started, nested in CheckoutStatus."""

    order_id: Optional[str] = None
    started_at: Optional[str] = None
    paid: Optional[bool] = None
    label_ready: Optional[bool] = None
    refunded: Optional[bool] = None
    carrier: Optional[str] = None
    service: Optional[str] = None
    tracking_number: Optional[str] = None
    tracking_status: Optional[str] = None
    estimated_delivery: Optional[str] = None
    delivered_at: Optional[str] = None


@dataclass
class CheckoutStatus(Model):
    """Where a checkout stands (checkout-status.schema.json)."""

    session_id: Optional[str] = None
    status: Optional[str] = None
    created_at: Optional[str] = None
    expires_at: Optional[str] = None
    amount: Optional[float] = None
    currency: Optional[str] = None
    service: Optional[str] = None
    quote_reused: Optional[bool] = None
    checkout: Optional[Checkout] = None
    next: Optional[str] = None

    _nested: ClassVar[Dict[str, Callable[[Any], Any]]] = {"checkout": _one(Checkout)}


@dataclass
class Liveness(Model):
    """The liveness answer (liveness.schema.json)."""

    ok: Optional[bool] = None
    service: Optional[str] = None
    version: Optional[str] = None
    time: Optional[str] = None
    last_estimate_race: Optional[Dict[str, Any]] = None


@dataclass
class Problem(Model):
    """Every error body on the host (problem.schema.json)."""

    error: Optional[str] = None
    message: Optional[str] = None
    missing: List[str] = field(default_factory=list)
