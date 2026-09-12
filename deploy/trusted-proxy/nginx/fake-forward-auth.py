#!/usr/bin/env python3
"""Deterministic forward-auth and Panel doubles for gateway smoke tests."""

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
from urllib.parse import quote, urlsplit


class QuietHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        pass


class ForwardAuthHandler(QuietHandler):
    def do_GET(self) -> None:  # noqa: N802 - stdlib handler API
        path = urlsplit(self.path).path
        if path.endswith("/oauth2/auth"):
            self.handle_auth()
            return
        if path.endswith("/oauth2/start"):
            prefix = path[: -len("/oauth2/start")]
            location = prefix + "/fake-idp"
            query = urlsplit(self.path).query
            redirect_target = self.headers.get("X-Auth-Request-Redirect")
            if redirect_target and "rd=" not in query:
                location += "?rd=" + quote(redirect_target, safe="")
            elif query:
                location += "?" + query
            self.send_response(HTTPStatus.FOUND)
            self.send_header("Location", location)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path.endswith("/oauth2/callback"):
            prefix = path[: -len("/oauth2/callback")]
            cookie_path = prefix + "/" if prefix else "/"
            self.send_response(HTTPStatus.OK)
            self.send_header("Set-Cookie", f"xform_test_auth=admit; Path={cookie_path}; HttpOnly")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path.endswith("/oauth2/sign_out"):
            prefix = path[: -len("/oauth2/sign_out")]
            cookie_path = prefix + "/" if prefix else "/"
            self.send_response(HTTPStatus.OK)
            self.send_header("Set-Cookie", f"xform_test_auth=; Path={cookie_path}; Max-Age=0")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path.endswith("/fake-idp") or "/oauth2/" in path:
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
        "Forwarded",
        "X-Xform-Authenticated",
        "X-Forwarded-For",
        "X-Forwarded-Host",
        "X-Forwarded-Port",
        "X-Forwarded-Proto",
        "X-Forwarded-Server",
        "X-Forwarded-Uri",
        "X-Forwarded-User",
        "X-Forwarded-Email",
        "X-Forwarded-Groups",
        "X-Forwarded-Preferred-Username",
        "X-Forwarded-Access-Token",
        "X-Forwarded-Authorization",
        "X-Forwarded-Id-Token",
        "X-Forwarded-Client-Cert",
        "X-Auth-Request-User",
        "X-Auth-Request-Email",
        "X-Auth-Request-Groups",
        "X-Auth-Request-Preferred-Username",
        "X-Auth-Request-Token",
        "X-Auth-Request-Access-Token",
        "X-Auth-Request-Redirect",
        "X-Auth-Request-Id-Token",
        "X-Auth-Request-IdToken",
        "X-Access-Token",
        "X-ID-Token",
        "X-Id-Token",
        "Remote-User",
        "Remote-Email",
        "Remote-Groups",
        "X-Remote-User",
        "X-Remote-Email",
        "X-Remote-Groups",
        "X-Authenticated-User",
        "X-Authenticated-Email",
        "X-Authenticated-Groups",
        "X-User",
        "X-Email",
        "X-Groups",
        "X-Group",
        "X-Original-URL",
        "X-Original-URI",
        "X-Original-Method",
        "X-SSL-Client-Cert",
        "X-Client-Cert",
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
    def __init__(self, auth: HTTPServer, panel: HTTPServer, socket_path: str | None) -> None:
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
            if self.socket_path is not None:
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
    panel = parser.add_mutually_exclusive_group(required=True)
    panel.add_argument("--socket")
    panel.add_argument("--panel-port", type=int)
    args = parser.parse_args()

    auth = ThreadingHTTPServer((args.auth_address, args.auth_port), ForwardAuthHandler)
    if args.socket is not None:
        panel_server = UnixHTTPServer(args.socket, PanelHandler)
    else:
        panel_server = ThreadingHTTPServer(("127.0.0.1", args.panel_port), PanelHandler)
    servers = Servers(auth, panel_server, args.socket)
    signal.signal(signal.SIGINT, servers.stop)
    signal.signal(signal.SIGTERM, servers.stop)
    servers.serve()


if __name__ == "__main__":
    main()
