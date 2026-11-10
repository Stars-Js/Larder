"""极简 mock embedding 服务：对文本做确定性小向量，供本地冒烟测试用。"""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
import hashlib


def vec(text: str, dim: int = 16):
    h = hashlib.sha256(text.encode("utf-8")).digest()
    return [((h[i % len(h)] / 255.0) - 0.5) for i in range(dim)]


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path.rstrip("/") != "/v1/embeddings":
            self.send_error(404)
            return
        n = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(n) or b"{}")
        inputs = body.get("input", [])
        if isinstance(inputs, str):
            inputs = [inputs]
        data = [{"index": i, "embedding": vec(t)} for i, t in enumerate(inputs)]
        out = json.dumps({"data": data, "model": body.get("model", "mock")}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 8798), H).serve_forever()
