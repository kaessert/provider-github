#!/usr/bin/env python3
"""A local stand-in for the GitHub REST API, used by selftest.sh.

Serves GET <path> from <root><path>.json (the query string is ignored; a second
page is empty). Anything else is refused with 405, so a test that passes proves
the snapshot tool only reads. Every request is appended to <log> as
"<METHOD> <path>". Prints the port it listens on, then serves until killed.
"""
import http.server
import json
import os
import sys
import urllib.parse

ROOT, LOG = sys.argv[1], sys.argv[2]


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def _record(self):
        with open(LOG, "a") as f:
            f.write("%s %s\n" % (self.command, self.path))

    def _send(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self._record()
        parsed = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        path = os.path.join(ROOT, parsed.path.lstrip("/") + ".json")
        if not os.path.isfile(path):
            self._send(404, {"message": "Not Found"})
            return
        with open(path) as f:
            body = json.load(f)
        if query.get("page", ["1"])[0] != "1":
            body = [] if isinstance(body, list) else {}
        self._send(200, body)

    def _refuse(self):
        self._record()
        self._send(405, {"message": "the stand-in API is read-only"})

    do_POST = do_PUT = do_PATCH = do_DELETE = _refuse


server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
print(server.server_address[1], flush=True)
server.serve_forever()
