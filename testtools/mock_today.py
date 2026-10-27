#!/usr/bin/env python3
"""today 上游 mock：复刻 /api/token + /v1/messages + /v1/conversations/messages 协议。

只用于验证插件成功路径，不代表真实上游行为。
    python mock_today.py 18999
"""
import json
import sys
import threading
import time

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

STATE = {"threads": {}, "lock": threading.Lock()}
REPLY = "你好！我是 Today 的 mock 上游，这条回复用于验证插件链路。"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        sys.stderr.write("[mock] %s\n" % (fmt % args))

    def _send(self, code, obj=None, headers=None):
        body = b"" if obj is None else json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if body:
            self.wfile.write(body)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}") if n else {}

    def do_POST(self):
        path = urlparse(self.path).path
        if path == "/api/token":
            self._body()
            self._send(200, {"token": "mock-jwt", "expiresIn": 3600})
            return
        if path == "/v1/messages":
            body = self._body()
            tid = "thread_" + str(int(time.time() * 1000) % 10**8)
            with STATE["lock"]:
                STATE["threads"][tid] = {
                    "prompt": body.get("content") or "",
                    "started": time.time(),
                }
            self._send(202, {"threadId": tid, "messageId": "msg_1"})
            return
        if path == "/v1/pricing/model-option":
            self._send(200, {"ok": True})
            return
        self._send(404, {"error": "not found"})

    def do_PUT(self):
        # 必须读完请求体：残留字节会污染同一条 keep-alive 连接上的下一个请求。
        self._body()
        self._send(200, {"ok": True})

    def do_GET(self):
        u = urlparse(self.path)
        if u.path == "/v1/pricing/status":
            self._send(200, {"data": {"status": {
                "plan": "pro",
                "modelOptions": {
                    "selectedOptionId": "model.deepseek-v4.1-flash",
                    "options": [
                        {"id": "model.deepseek-v4.1-flash", "disabled": False,
                         "display": {"title": "DeepSeek V4.1 Flash"}},
                        {"id": "model.glm-5.3", "disabled": False,
                         "display": {"title": "GLM 5.3"}},
                    ],
                },
                "usage": {"percentUsed": 23, "used": 23, "limit": 100},
            }}})
            return
        if u.path == "/v1/users/me":
            self._send(200, {"id": "user_mock", "email": "mock@example.com", "plan": "pro"})
            return
        if u.path == "/v1/conversations/messages":
            items = []
            with STATE["lock"]:
                for tid, info in STATE["threads"].items():
                    elapsed = time.time() - info["started"]
                    done = elapsed >= 1.0
                    items.append({
                        "threadId": tid,
                        "role": "assistant",
                        "state": "completed" if done else "in_progress",
                        "isFinal": done,
                        "body": {"parts": [{"messageType": "text",
                                            "content": {"text": REPLY if done else ""}}]},
                    })
            self._send(200, {"data": {"items": items}})
            return
        self._send(404, {"error": "not found"})


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18999
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    srv.daemon_threads = True
    sys.stderr.write("mock today upstream on http://127.0.0.1:%d\n" % port)
    srv.serve_forever()
