"""The ``smklog`` command line (docs/DESIGN.md, "CLI (Python)").

    smklog quote "Yamaha FG800 acoustic guitar in a hard case" 07102 30303 [--country US] [--qty 1]
                 [--dims 112x48x20 --kg 6.4] [--json]
    smklog session --quote-id q_... [--service "USPS Ground Advantage"] [--json]
    smklog session-status as_... [--json]
    smklog status [--json]

Exit codes: 0 for a priced answer or a successful read, 2 when the API refuses (the
Problem is printed) or answers a quote without a price, 3 on a rate limit, 1 when the
host could not be reached.
"""

import argparse
import json
import os
import sys
from typing import Any, Dict, List, Optional, Sequence, Tuple, Union

from ._version import __version__
from .client import DEFAULT_BASE_URL, SmklogClient
from .errors import RateLimitedError, SmklogError
from .models import CheckoutSession, CheckoutStatus, Liveness, QuoteResponse

Number = Union[int, float]


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    client = SmklogClient(base_url=args.base_url, token=args.token or None)
    try:
        return args.run(client, args)
    except RateLimitedError as exc:
        _print_problem(exc, args.json)
        return 3
    except SmklogError as exc:
        _print_problem(exc, args.json)
        return 1 if exc.error == "network_error" else 2


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="smklog",
        description="Live parcel rates from the SMKlog Quote API. No key needed; every quote spends a carrier rating, so quote once per shipment.",
    )
    parser.add_argument("--version", action="version", version="smklog-sdk " + __version__)
    # Shared flags live on each subcommand so they can follow it, as the usage lines show.
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--json", action="store_true", help="print the raw JSON answer instead of a table")
    common.add_argument("--base-url", default=os.environ.get("SMKLOG_BASE_URL", DEFAULT_BASE_URL),
                        help="API host (default %(default)s; env SMKLOG_BASE_URL)")
    common.add_argument("--token", default=os.environ.get("SMKLOG_TOKEN", ""),
                        help="optional OAuth bearer for a dedicated rate-limit bucket (env SMKLOG_TOKEN)")

    commands = parser.add_subparsers(dest="command", metavar="command", required=True)

    quote = commands.add_parser("quote", parents=[common], help="price one parcel")
    quote.add_argument("product", help='the item in plain words, e.g. "paperback books"')
    quote.add_argument("from_zip", help="5-digit US origin ZIP")
    quote.add_argument("to_zip", help="destination ZIP or postal code")
    quote.add_argument("--country", default="US", help="destination country: US, CA, GB, DE or AU (default US)")
    quote.add_argument("--qty", type=int, default=1, help="units shipping together (default 1)")
    quote.add_argument("--dims", type=_dims, metavar="LxWxH", help="packed box in cm, e.g. 30x22x12 (needs --kg)")
    quote.add_argument("--kg", type=_number, help="packed weight in kg (needs --dims)")
    quote.set_defaults(run=_cmd_quote)

    session = commands.add_parser("session", parents=[common], help="open a checkout for a human from a quote")
    session.add_argument("--quote-id", required=True, help="quote_id from a quote, valid 15 minutes")
    session.add_argument("--service", help='anchor the amount to a service, e.g. "USPS Ground Advantage"')
    session.set_defaults(run=_cmd_session)

    session_status = commands.add_parser("session-status", parents=[common], help="where a checkout stands")
    session_status.add_argument("session_id", help="session_id from `smklog session`")
    session_status.set_defaults(run=_cmd_session_status)

    status = commands.add_parser("status", parents=[common], help="liveness and the serving version")
    status.set_defaults(run=_cmd_status)
    return parser


def _cmd_quote(client: SmklogClient, args: argparse.Namespace) -> int:
    if (args.dims is None) != (args.kg is None):
        raise SmklogError(0, "invalid_request", "--dims and --kg go together.")
    fields: Dict[str, Any] = {
        "product": args.product,
        "from_postal_code": args.from_zip,
        "to_postal_code": args.to_zip,
        "quantity": args.qty,
    }
    # US is the API's default, so it is only sent when the caller asked for elsewhere.
    if args.country.upper() != "US":
        fields["to_country_code"] = args.country.upper()
    if args.dims is not None:
        fields.update(zip(("length_cm", "width_cm", "height_cm"), args.dims))
        fields["weight_kg"] = args.kg
    quote = client.get_parcel_quote(**fields)
    if args.json:
        _print_json(quote.raw)
    else:
        _print_quote(quote)
    return 0 if quote.is_priced() else 2


def _cmd_session(client: SmklogClient, args: argparse.Namespace) -> int:
    fields: Dict[str, Any] = {"quote_id": args.quote_id}
    if args.service:
        fields["service"] = args.service
    session = client.create_payment_session(**fields)
    if args.json:
        _print_json(session.raw)
    else:
        _print_session(session)
    return 0


def _cmd_session_status(client: SmklogClient, args: argparse.Namespace) -> int:
    status = client.get_checkout_status(args.session_id)
    if args.json:
        _print_json(status.raw)
    else:
        _print_session_status(status)
    return 0


def _cmd_status(client: SmklogClient, args: argparse.Namespace) -> int:
    liveness = client.status()
    if args.json:
        _print_json(liveness.raw)
    else:
        _print_rows([
            ("ok", _yes_no(liveness.ok)),
            ("service", liveness.service),
            ("version", liveness.version),
            ("time", liveness.time),
        ])
    return 0


def _print_quote(quote: QuoteResponse) -> None:
    if quote.identified_product_title:
        print(quote.identified_product_title)
    box = quote.package
    if box is not None and box.length_cm is not None:
        origin = "exact" if box.confidence == "manual" else "estimate, %s confidence" % (box.confidence or "unknown")
        print("Box: %s x %s x %s cm, %s kg (%s)" % (
            _num(box.length_cm), _num(box.width_cm), _num(box.height_cm), _num(box.weight_kg), origin))
    if not quote.is_priced():
        print("No price: %s. %s" % (quote.mode, quote.notice or "The service did not price this shipment."))
        return
    rows = [("Carrier", "Service", "Checkout", "Counter", "Delivery")]
    for rate in quote.rates:
        rows.append((rate.carrier or "", rate.service or "", _money(rate.amount), _money(rate.retail_amount), _delivery(rate)))
    _print_table(rows)


def _print_session(session: CheckoutSession) -> None:
    _print_rows([
        ("session_id", session.session_id),
        ("amount", _money(session.amount, session.currency)),
        ("service", session.service),
        ("quote_reused", _yes_no(session.quote_reused)),
        ("handoff_url", session.handoff_url),
        ("note", session.note),
    ])


def _print_session_status(status: CheckoutStatus) -> None:
    checkout = status.checkout
    rows: List[Tuple[str, Any]] = [
        ("session_id", status.session_id),
        ("status", status.status),
        ("amount", _money(status.amount, status.currency)),
        ("service", status.service),
        ("expires_at", status.expires_at),
    ]
    if checkout is not None:
        carrier = " ".join(part for part in (checkout.carrier, checkout.service) if part)
        rows += [
            ("order_id", checkout.order_id),
            ("paid", _yes_no(checkout.paid)),
            ("label_ready", _yes_no(checkout.label_ready)),
            ("refunded", _yes_no(checkout.refunded)),
            ("carrier", carrier or None),
            ("tracking_number", checkout.tracking_number),
            ("tracking_status", checkout.tracking_status),
            ("estimated_delivery", checkout.estimated_delivery),
            ("delivered_at", checkout.delivered_at),
        ]
    rows.append(("next", status.next))
    _print_rows(rows)


def _print_problem(exc: SmklogError, as_json: bool) -> None:
    if as_json:
        _print_json(exc.problem())
        return
    line = "error: %s" % exc.error
    if exc.status:
        line += " (HTTP %d)" % exc.status
    if exc.message:
        line += ": " + exc.message
    print(line, file=sys.stderr)
    if exc.missing:
        print("missing: " + ", ".join(exc.missing), file=sys.stderr)


def _print_json(data: Any) -> None:
    print(json.dumps(data, indent=2))


def _print_rows(rows: Sequence[Tuple[str, Any]]) -> None:
    present = [(key, value) for key, value in rows if value is not None and value != ""]
    width = max(len(key) for key, _ in present) if present else 0
    for key, value in present:
        print("%-*s  %s" % (width, key, value))


def _print_table(rows: Sequence[Sequence[str]]) -> None:
    widths = [max(len(row[i]) for row in rows) for i in range(len(rows[0]))]
    for row in rows:
        print("  ".join(cell.ljust(widths[i]) for i, cell in enumerate(row)).rstrip())


def _delivery(rate: Any) -> str:
    parts = []
    if rate.delivery_window:
        parts.append(str(rate.delivery_window))
    elif rate.delivery_days is not None:
        parts.append("%s day%s" % (rate.delivery_days, "" if rate.delivery_days == 1 else "s"))
    if rate.delivery_estimated_date:
        parts.append("by %s" % rate.delivery_estimated_date)
    return ", ".join(parts)


def _money(amount: Optional[Number], currency: Optional[str] = None) -> str:
    if amount is None:
        return ""
    text = "$%.2f" % amount
    return text + " " + currency if currency and currency != "USD" else text


def _num(value: Optional[Number]) -> str:
    if value is None:
        return "?"
    return str(int(value)) if float(value).is_integer() else str(value)


def _yes_no(value: Optional[bool]) -> Optional[str]:
    return None if value is None else ("yes" if value else "no")


def _number(text: str) -> Number:
    value: Number
    try:
        # Whole numbers stay integers so the wire shows 30, not 30.0.
        value = int(text)
    except ValueError:
        try:
            value = float(text)
        except ValueError:
            raise argparse.ArgumentTypeError("%r is not a number" % text)
    if value <= 0:
        raise argparse.ArgumentTypeError("%r must be above zero" % text)
    return value


def _dims(text: str) -> Tuple[Number, Number, Number]:
    parts = text.lower().split("x")
    if len(parts) != 3:
        raise argparse.ArgumentTypeError("--dims wants LxWxH in cm, e.g. 30x22x12")
    length, width, height = (_number(part) for part in parts)
    return length, width, height


if __name__ == "__main__":
    sys.exit(main())
