"""Larder 本地联调用的 mock 服务。

一个进程同时模拟：
  GET  /v1/models         OpenAI 兼容模型列表
  POST /v1/embeddings     OpenAI 兼容向量接口（8 维，按关键词分配维度）
  POST /v1/rerank         OpenAI 兼容重排接口
  POST /parse             MinerU 自建 /file_parse 风格的解析接口（返回 OCR 文本）

仅用于开发与自动化测试，不连接任何外部网络。

    python scripts/mock_services.py --port 8811
"""
import argparse
import hashlib
import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DIM = 8
KEYWORDS = ["windows", "macos", "pdf", "larder", "安装", "检索", "word", "ocr"]


def embed(text: str):
    vec = [0.01] * DIM
    low = text.lower()
    for i, kw in enumerate(KEYWORDS):
        if kw in low:
            vec[i] += 1.0
    # 加一点确定性噪声，避免所有向量完全同向
    h = hashlib.sha256(text.encode("utf-8")).digest()
    for i in range(DIM):
        vec[i] += (h[i] / 255.0) * 0.05
    return vec


def score(query: str, doc: str) -> float:
    q = query.lower()
    d = doc.lower()
    total = 0.0
    for kw in KEYWORDS:
        if kw in q and kw in d:
            total += 1.0
    # 常见问题词也参与匹配
    for tok in set(re.findall(r"[\u4e00-\u9fff]{2,}", q)):
        if tok in d:
            total += 0.5
    return total + 0.05


class Handler(BaseHTTPRequestHandler):
    server_version = "LarderMock/1.0"

    def _json(self, code: int, obj):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.rstrip("/") in ("/v1/models", "/models"):
            self._json(200, {"data": [
                {"id": "BAAI/bge-m3", "object": "model"},
                {"id": "text-embedding-3-small", "object": "model"},
                {"id": "BAAI/bge-reranker-v2-m3", "object": "model"},
                {"id": "gpt-4o-mini", "object": "model"},
            ]})
            return
        self._json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(n) if n else b""
        path = self.path.rstrip("/")

        if path == "/v1/embeddings":
            body = json.loads(raw or b"{}")
            inputs = body.get("input", [])
            if isinstance(inputs, str):
                inputs = [inputs]
            data = [{"index": i, "embedding": embed(t)} for i, t in enumerate(inputs)]
            self._json(200, {"data": data, "model": body.get("model", "mock-embed")})
            return

        if path == "/v1/rerank":
            body = json.loads(raw or b"{}")
            q = body.get("query", "")
            docs = body.get("documents", [])
            results = [{"index": i, "relevance_score": score(q, d)} for i, d in enumerate(docs)]
            self._json(200, {"results": results})
            return

        if path in ("/parse", "/file_parse", "/v1/convert/file"):
            # 不去解析 multipart，直接返回一段“OCR 识别结果”。
            md = (
                "# 英语六级核心词\n\n"
                "## Unit 1\n\n"
                "ambition n. 野心；雄心，抱负\n\n"
                "aggressive adj. 好斗的，侵略的；有进取心的\n\n"
                "congress n. 国会，议会；代表大会\n\n"
                "> 该内容由 mock 解析服务的 OCR 生成，用于验证扫描件 PDF 的兜底流程。\n"
            )
            if path == "/v1/convert/file":
                self._json(200, {"status": "success", "document": {"md_content": md}})
            else:
                self._json(200, {"code": 0, "md_content": md, "title": "英语六级核心词"})
            return

        self._json(404, {"error": {"message": "not found"}})

    def log_message(self, fmt, *args):
        pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8811)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
