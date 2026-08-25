import json
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

POLLUTED = "</think>\nSure! Here is your data:\n```json\n{\"live\": True}\n```\nCheers!"

class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); self.rfile.read(n)
        body = json.dumps({
            "id": "msg_live", "type": "message", "role": "assistant",
            "content": [
                {"type": "text", "text": POLLUTED},
                {"type": "tool_use", "id": "t1", "name": "get_weather", "input": {"city": "Jakarta"}}
            ],
            "stop_reason": "tool_use"
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass

ThreadingHTTPServer(("127.0.0.1", 9997), H).serve_forever()
