#!/usr/bin/env python3
"""假上游：只实现「列模型」这一条，给 `mock/e2e_modellist.sh` 用。

为什么不复用 `core/mock/fake_upstream.py`：那一位是**逐字节复刻真实上游行为**
的产物（转发、推理、尾部形状），它的 `/v1/models` 只是一个空列表桩。这里要验的
是模型清单的解析与分组，需要一份**有内容**的回答；把测试夹具塞进核心那份假上游
会改变别的端到端的输入。两份东西的目的不同，所以分开。

路由：
  GET /v1/models   有清单（OpenAI 形状，含 owned_by 分组）
  GET /models      同上（DeepSeek 那种 base 不带 /v1 的写法）
  其它              404（用来演「这家没有列表接口」）
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
