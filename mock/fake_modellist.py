#!/usr/bin/env python3
"""Fake upstream: implements only "list models", for `mock/e2e_modellist.sh`.

Why not reuse `core/mock/fake_upstream.py`: that one is the product of a **byte-for-byte replica of real upstream behavior**
(forwarding, reasoning, tail shape), and its `/v1/models` is just an empty-list stub. What is verified here
is model list parsing and grouping, which needs an answer **with content**; stuffing test fixtures into the core fake upstream
would change the inputs of other end-to-end runs. The two things have different purposes, so they are kept apart.

Routing:
  GET /v1/models   a list (OpenAI shape, with owned_by grouping)
  GET /models      the same (DeepSeek's style of base without /v1)
  everything else  404 (used to play "this vendor has no list endpoint")
"""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODELS = {
    "object": "list",
    "data": [
        {"id": "alpha-chat", "owned_by": "alpha"},
        {"id": "alpha-reasoner", "owned_by": "alpha"},
        {"id": "beta-vision", "owned_by": "beta"},
    ],
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.split("?")[0] in ("/v1/models", "/models"):
            body = json.dumps(MODELS).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        body = b'{"error":"no model list here"}'
        self.send_response(404)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print("[fake-modellist] " + fmt % args, flush=True)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, default=18098)
    a = p.parse_args()
    srv = ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    print(f"[fake-modellist] listening on 127.0.0.1:{a.port}", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
