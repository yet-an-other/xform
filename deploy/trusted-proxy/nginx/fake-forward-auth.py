#!/usr/bin/env python3
"""Deterministic forward-auth and Unix-socket Panel doubles for the nginx smoke test."""

from __future__ import annotations

import argparse
import json
import os
import signal
import socket
import threading
from http import HTTPStatus
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import ThreadingMixIn
from urllib.parse import urlsplit


class QuietHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        pass


class ForwardAuthHandler(QuietHandler):
    def do_GET(self) -> None:  # noqa: N802 - stdlib handler API
        path = urlsplit(self.path).path
        if path == "/oauth2/auth":
            self.handle_auth()
            return
        if path == "/oauth2/start":
            self.send_response(HTTPStatus.FOUND)
            self.send_header("Location", "/fake-idp" + ("?" + self.path.split("?", 1)[1] if "?" in self.path else ""))
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path in {"/oauth2/callback", "/oauth2/sign_out", "/fake-idp"} or path.startswith("/oauth2/"):
            self.send_text(HTTPStatus.OK, "fake gateway endpoint")
            return
        self.send_text(HTTPStatus.NOT_FOUND, "not found")

    def handle_auth(self) -> None:
        cookies = SimpleCookie()
        cookies.load(self.headers.get("Cookie", ""))
        mode = cookies.get("xform_test_auth")
        mode = mode.value if mode is not None else "deny"
        if mode == "admit":
            self.send_response(HTTPStatus.NO_CONTENT)
            # These must never reach the xform Unix upstream. They model the
            # identity and token headers a real auth gateway might emit.
            self.send_header("X-Auth-Request-User", "spoofed@example.test")
            self.send_header("X-Auth-Request-Email", "spoofed@example.test")
            self.send_header("X-Auth-Request-Access-Token", "spoofed-access-token")
            self.send_header("X-Xform-Authenticated", "spoofed-admission")
            self.end_headers()
            return
        if mode == "error":
            self.send_text(HTTPStatus.SERVICE_UNAVAILABLE, "forward-auth unavailable")
            return
        self.send_text(HTTPStatus.UNAUTHORIZED, "fake sign-in response")

    def send_text(self, status: HTTPStatus, text: str) -> None:
        body = text.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class PanelHandler(QuietHandler):
    observed_headers = (
        "Host",
        "Cookie",
        "Authorization",
        "Proxy-Authorization",
        "X-Xform-Authenticated",
        "X-Forwarded-User",
        "X-Forwarded-Email",
        "X-Forwarded-Access-Token",
        "X-Auth-Request-Email",
        "X-Auth-Request-Access-Token",
        "X-Access-Token",
        "X-ID-Token",
        "X-Forwarded-For",
        "X-Real-IP",
    )

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler API
        self.send_panel_response()

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        self.send_panel_response()

    def do_HEAD(self) -> None:  # noqa: N802 - stdlib handler API
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def send_panel_response(self) -> None:
        payload = {
            "method": self.command,
            "request_target": self.path,
            "headers": {name: self.headers.get(name) for name in self.observed_headers},
        }
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class UnixHTTPServer(ThreadingMixIn, HTTPServer):
    address_family = socket.AF_UNIX
    daemon_threads = True
    allow_reuse_address = True

    def server_bind(self) -> None:
        socket_path = self.server_address
        if os.path.exists(socket_path):
            os.unlink(socket_path)
        super().server_bind()
        # The smoke container has no shared xform gateway group. The real
        # xform listener is 0660; this fake uses 0666 only for transport proof.
        os.chmod(socket_path, 0o666)


class Servers:
    def __init__(self, auth: HTTPServer, panel: HTTPServer, socket_path: str) -> None:
        self.auth = auth
        self.panel = panel
        self.socket_path = socket_path
        self.stopping = threading.Event()

    def serve(self) -> None:
        auth_thread = threading.Thread(target=self.auth.serve_forever, daemon=True)
        panel_thread = threading.Thread(target=self.panel.serve_forever, daemon=True)
        auth_thread.start()
        panel_thread.start()
        try:
            self.stopping.wait()
        finally:
            self.auth.shutdown()
            self.panel.shutdown()
            self.auth.server_close()
            self.panel.server_close()
            try:
                os.unlink(self.socket_path)
            except FileNotFoundError:
                pass

    def stop(self, _signum: int, _frame: object) -> None:
        self.stopping.set()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--auth-address", default="127.0.0.1")
    parser.add_argument("--auth-port", type=int, required=True)
    parser.add_argument("--socket", required=True)
    args = parser.parse_args()

    auth = ThreadingHTTPServer((args.auth_address, args.auth_port), ForwardAuthHandler)
    panel = UnixHTTPServer(args.socket, PanelHandler)
    servers = Servers(auth, panel, args.socket)
    signal.signal(signal.SIGINT, servers.stop)
    signal.signal(signal.SIGTERM, servers.stop)
    servers.serve()


if __name__ == "__main__":
    main()
