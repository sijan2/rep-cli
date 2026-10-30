"""Synthetic fixtures for the T1–T11 rep contract; no external services.

The harness drives the browser exclusively through rep subprocesses. This
module only serves local HTTP documents and records server-side request counts.
"""
import contextlib
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import threading
import time
from urllib.parse import parse_qs, urlsplit

ARTICLE = """<article><h1>Reliable observation</h1><p>A fixture article about measuring complete evidence.</p>
<h2>Method</h2><p>Keep source text, code, tables and links intact.</p>
<pre><code>if (a &lt; b) {\n  return \"two  spaces\";\n}\n</code></pre>
<table><tr><th>Item</th><th>Value</th></tr><tr><td>Alpha</td><td>42</td></tr></table>
<h2>Sources</h2><a href='/paper'>Research paper</a><img alt='Fixture diagram' src='/image.svg'></article>"""
DOCUMENT = "Plain-text fixture document\n\n" + "Document evidence must remain complete. " * 90
FILE_BYTES = b"%PDF-1.4\n% synthetic authenticated artifact\n" + b"fixture\n" * 128


class FixtureServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), Handler)
        self.counts = {}
        self.guard = threading.Lock()
        self.active = self.peak = 0

    @property
    def url(self):
        return "http://127.0.0.1:{}".format(self.server_port)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def reply(self, data, content_type="text/html; charset=utf-8", status=200, headers=None):
        data = data.encode() if isinstance(data, str) else data
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        url = urlsplit(self.path)
        path = url.path
        with self.server.guard:
            self.server.counts[path] = self.server.counts.get(path, 0) + 1
        if path == "/api/items":
            with self.server.guard:
                self.server.active += 1
                self.server.peak = max(self.server.peak, self.server.active)
            try:
                time.sleep(.04)
                query = parse_qs(url.query).get("q", ["alpha"])[0]
                return self.reply(json.dumps({"data": [{"id": query, "price": 42}], "access_token": "fixture-access-token"}),
                                  "application/json", headers={"Set-Cookie": "fixture-session=fake-cookie; HttpOnly; SameSite=Lax"})
            finally:
                with self.server.guard:
                    self.server.active -= 1
        if path == "/file.pdf":
            if "fixture-session=fake-cookie" not in self.headers.get("Cookie", ""):
                return self.reply("sign in required", status=401)
            return self.reply(FILE_BYTES, "application/pdf")
        if path == "/image.svg":
            return self.reply("<svg xmlns='http://www.w3.org/2000/svg' width='100' height='60'><rect width='100' height='60' fill='#405ac2'/></svg>", "image/svg+xml")
        if path == "/document/d/fixture/export":
            return self.reply(DOCUMENT, "text/plain")
        pages = {
            "/article": ARTICLE,
            "/long": "<article><h1>Persistent cursor fixture</h1>" + "".join("<p>Paragraph {0}: " .format(i) + "Evidence remains readable after the renderer expires. " * 10 + "</p>" for i in range(90)) + "</article>",
            "/form": """<main><h1>Search fixture</h1><form onsubmit="event.preventDefault(); document.querySelector('[role=status]').textContent='Saved: '+this.query.value;">
                <label>Query <input name='query'></label><button>Submit</button></form><p role='status'></p></main>""",
            "/flow": """<main><h1>Five-step flow</h1><p role='status'>Step 1</p><button onclick="let n=Number(this.dataset.n||1); document.querySelector('[role=status]').textContent=n===5?'Flow complete':'Step '+(n+1); this.dataset.n=n+1; this.textContent=n===5?'Done':'Continue '+(n+1); this.disabled=n===5;">Continue 1</button></main>""",
            "/rows": "<main><h1>Products</h1><table>" + "".join("<tr><td>Product {0}</td><td>Price ${1}</td></tr>".format(i, i * 10) for i in range(1, 11)) + "</table></main>",
            "/api": """<main><h1>API fixture</h1><p aria-busy='true'>Loading results</p></main><script>fetch('/api/items?q=alpha').then(r=>r.json()).then(x=>{let p=document.querySelector('p');p.textContent='Result '+x.data[0].id+' price '+x.data[0].price;p.removeAttribute('aria-busy')})</script>""",
            "/download": """<main><h1>Download fixture</h1><a href='/file.pdf'>Download file</a></main><script>fetch('/file.pdf').then(r=>r.arrayBuffer()).then(()=>document.body.dataset.ready='true')</script>""",
            "/document/d/fixture/edit": """<main class='kix-appview-editor'><h1>Document fixture</h1><p>The editor draws on a canvas.</p><canvas></canvas></main><link rel='alternate' type='text/plain' href='/document/d/fixture/export?format=txt'>""",
            "/paper": "<article><h1>Synthetic paper</h1><p>The study cites two sources.</p><a href='/article'>Source one</a><a href='/rows'>Source two</a></article>",
            "/reference": "<main><h1>Design reference</h1><p>Fluid typography and a measured grid.</p><img src='/image.svg' alt='Reference asset'></main>",
            "/loading": "<main aria-busy='true'><div role='progressbar'>Loading</div></main>",
            "/empty": "<main></main>",
        }
        if path.startswith("/sweep/"):
            pages[path] = "<main><h1>Responsive fixture {}</h1><p>Viewport evidence</p></main>".format(path.rsplit("/", 1)[-1])
        if path not in pages:
            return self.reply("not found", status=404)
        headers = {"Set-Cookie": "fixture-session=fake-cookie; HttpOnly; SameSite=Lax"} if path == "/download" else None
        self.reply("<!doctype html><html lang='en'><meta charset='utf-8'><title>Rep synthetic fixture</title><style>body{font:18px system-ui;margin:32px}h1{font-size:clamp(24px,4vw,52px)}main,article{max-width:900px}td,th{padding:8px}input,button{font:inherit}img{max-width:100%}</style>" + pages[path] + "</html>", headers=headers)

    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(min(size, 65536))
        path = urlsplit(self.path).path
        with self.server.guard:
            self.server.counts[path] = self.server.counts.get(path, 0) + 1
        self.reply('{"saved":true}', "application/json", status=403 if path == "/reject-write" else 200)


@contextlib.contextmanager
def serve():
    server = FixtureServer()
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
