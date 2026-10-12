"""Test helpers: the src path, the vendored samples, and a replay server.

The replay server is a real HTTP server on a loopback port (http.server in a thread),
so the client's urllib path is exercised end to end: headers, JSON bodies, status
codes and retries all go over a socket exactly as they would against production.
"""

import json
import os
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional

_HERE = os.path.dirname(os.path.abspath(__file__))
_SRC = os.path.join(_HERE, "..", "src")
if _SRC not in sys.path:
    sys.path.insert(0, _SRC)

TESTDATA = os.path.normpath(os.path.join(_HERE, "..", "..", "testdata"))


def load_json(*parts: str) -> Any:
    with open(os.path.join(TESTDATA, *parts), encoding="utf-8") as handle:
        return json.load(handle)


def sample(name: str) -> Dict[str, Any]:
    return load_json("samples", name + ".json")


def request_example(name: str, example: str) -> Dict[str, Any]:
    """A named request example from testdata/samples/<name>.json, verbatim."""
    for item in sample(name)["examples"]:
        if item["name"] == example:
            return item["value"]
    raise KeyError("%s has no request example %r" % (name, example))


def response_example(name: str, status: int, example: str = "default") -> Dict[str, Any]:
    """A per-status response example from testdata/samples/<name>.json, verbatim."""
    for item in sample(name)["responses"][str(status)]:
        if item["name"] == example:
            return item["value"]
    raise KeyError("%s has no %d example %r" % (name, status, example))


class Exchange:
    """One request the replay server received."""

    def __init__(self, method: str, path: str, headers: Dict[str, str], body: Any) -> None:
        self.method = method
        self.path = path
        self.headers = headers
        self.body = body


class ReplayServer:
    """Serves scripted answers in the order they were queued, per method and path.

    Each queued answer is consumed once, so a test can script a 503 followed by a
    200 and read back exactly how many requests the client made.
    """

    def __init__(self) -> None:
        self.scripted: List[Dict[str, Any]] = []
        self.exchanges: List[Exchange] = []
        self._lock = threading.Lock()
        server = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                server._handle(self)

            def do_POST(self) -> None:
                server._handle(self)

            def log_message(self, *args: Any) -> None:
                pass  # the test output stays the test output

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        # A short poll keeps shutdown() quick: each test starts and stops its own server.
        self.thread = threading.Thread(target=self.httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()

    @property
    def base_url(self) -> str:
        return "http://127.0.0.1:%d" % self.httpd.server_address[1]

    def expect(self, method: str, path: str, status: int, body: Any = None,
               delay: float = 0.0, content_type: str = "application/json") -> None:
        with self._lock:
            self.scripted.append({"method": method, "path": path, "status": status,
                                  "body": body, "delay": delay, "content_type": content_type})

    def close(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()

    def requests_for(self, method: str, path: str) -> List[Exchange]:
        return [x for x in self.exchanges if x.method == method and x.path == path]

    def _handle(self, handler: BaseHTTPRequestHandler) -> None:
        length = int(handler.headers.get("Content-Length") or 0)
        raw = handler.rfile.read(length) if length else b""
        body = json.loads(raw.decode("utf-8")) if raw else None
        with self._lock:
            self.exchanges.append(Exchange(handler.command, handler.path, dict(handler.headers), body))
            match = next((s for s in self.scripted
                          if s["method"] == handler.command and s["path"] == handler.path), None)
            if match is not None:
                self.scripted.remove(match)
        if match is None:
            match = {"status": 500, "content_type": "application/json", "delay": 0.0,
                     "body": {"error": "unscripted_request",
                              "message": "no scripted answer for %s %s" % (handler.command, handler.path)}}
        if match["delay"]:
            time.sleep(match["delay"])
        payload = match["body"]
        data = (payload if isinstance(payload, str) else json.dumps(payload)).encode("utf-8")
        handler.send_response(match["status"])
        handler.send_header("Content-Type", match["content_type"])
        handler.send_header("Content-Length", str(len(data)))
        handler.end_headers()
        try:
            handler.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass  # the client gave up first (the timeout test), which is the point


def unused_port() -> int:
    """A loopback port nothing listens on, for connection-refused tests."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def problem(error: str, message: str = "", missing: Optional[List[str]] = None) -> Dict[str, Any]:
    body: Dict[str, Any] = {"error": error, "message": message}
    if missing is not None:
        body["missing"] = missing
    return body
