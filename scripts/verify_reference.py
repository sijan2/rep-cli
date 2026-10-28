#!/usr/bin/env python3
"""Exercise the shipped reference/page commands in a real, authenticated browser.

Uses task-owned loopback tabs, independent fixture values and server request
counts. No form submissions or external account writes. --live also observes
the three user-supplied sites and retains timing/coverage evidence.
"""
import argparse
import hashlib
import json
import os
import shlex
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--rep', default='rep')
    ap.add_argument('--out', required=True)
    ap.add_argument('--task', default='reference-verification-20261006')
    ap.add_argument('--live', action='store_true')
    a = ap.parse_args()
    out = Path(a.out).resolve()
    out.mkdir(parents=True, exist_ok=False)
    prefix = [a.rep, '--workspace', 'rep-workspace', '--task', a.task]
    measurements, checks, tabs, requests, messages = [], [], set(), [], []

    def run(name, args, expected=0, command=None):
        started = time.perf_counter()
        cmd = command or prefix + args + ['--raw-json']
        if command:
            cmd[0] = a.rep
        p = subprocess.run(cmd, text=True, capture_output=True, timeout=35)
        elapsed = round((time.perf_counter() - started) * 1000, 2)
        (out / (name + '.json')).write_text(p.stdout)
        (out / (name + '.stderr')).write_text(p.stderr)
        d = json.loads(p.stdout)
        if d.get('created'):
            tabs.add(d['tab_id'])
        measurements.append({'name': name, 'wall_ms': elapsed, 'exit': p.returncode,
                             'status': d.get('status'), 'bytes': len(p.stdout.encode()),
                             'bridge_calls': d.get('bridge_calls'), 'model_requests': d.get('model_requests')})
        assert (p.returncode == 0) == (expected == 0), (name, p.returncode, d)
        return d, elapsed

    def check(name, ok):
        assert ok, name
        checks.append(name)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            requests.append(self.path)
            if self.path.startswith('/delayed-document'):
                head = b'<!doctype html><html><head><title>Delayed reference oracle</title></head>'
                tail = b'<body><main><h1>DELAYED_BODY_READY</h1><p>Exact delayed document text</p></main></body></html>'
                self.send_response(200)
                self.send_header('Content-Type', 'text/html; charset=utf-8')
                self.send_header('Content-Length', str(len(head) + len(tail)))
                self.end_headers()
                self.wfile.write(head)
                self.wfile.flush()
                time.sleep(2.7)
                self.wfile.write(tail)
                return
            if self.path.startswith('/long-controls'):
                long_query = '&' * 3000
                links = ''.join(
                    f'<a href="/target/{i}?q={long_query}">Long link {i}</a>'
                    for i in range(40)
                )
                body, mime = f'<!doctype html><html><body><main>{links}</main></body></html>', 'text/html; charset=utf-8'
            elif self.path.startswith('/large-dom'):
                body, mime = '<!doctype html><html><body><main>' + '<div>x</div>' * 50018 + '</main></body></html>', 'text/html; charset=utf-8'
            elif self.path.startswith('/long-fonts'):
                long_font = 'x' * 900
                rows = ''.join(
                    f'<p style="font-family: &quot;Font{i}{long_font}&quot;">Font row {i}</p>'
                    for i in range(24)
                )
                body, mime = f'<!doctype html><html><body><main><h1>Bounded font reference</h1>{rows}</main></body></html>', 'text/html; charset=utf-8'
            elif self.path.startswith('/chat'):
                body, mime = '''<!doctype html><html><body data-rep-chat="v1"><aside>SIDEBAR_NOT_CONVERSATION</aside>
<div id="history"></div><div id="log"></div><textarea aria-label="Message"></textarea><script>
for(let i=0;i<60;i++){const b=document.createElement('button');b.textContent='History '+i;document.querySelector('#history').append(b);}
const box=document.querySelector('textarea'),send=document.createElement('button');send.setAttribute('aria-label','Send');send.textContent='Send';
send.onclick=async e=>{const value=box.value;box.value='';const u=document.createElement('p');u.dataset.messageRole='user';u.dataset.messageId='u1';u.textContent=value;document.querySelector('#log').append(u);
const data=await fetch('/message',{method:'POST',body:JSON.stringify({value,trusted:e.isTrusted})}).then(r=>r.json());
const reply=document.createElement('p');reply.dataset.messageRole='assistant';reply.dataset.messageId='a1';reply.dataset.messageStatus='complete';reply.textContent=data.reply;document.querySelector('#log').append(reply);};
setTimeout(()=>document.body.append(send),600);
</script></body></html>''', 'text/html; charset=utf-8'
            elif self.path.startswith('/image.svg'):
                body = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32"><rect width="64" height="32" fill="red"/></svg>'
                mime = 'image/svg+xml'
            elif self.path.startswith('/foreign.css'):
                body, mime = 'h1 {color:rgb(11,22,33)}', 'text/css'
            elif self.path.startswith('/frame'):
                body, mime = '<p>Same origin FRAME_EVIDENCE</p>', 'text/html'
            else:
                body = '''<!doctype html><html><head><meta charset="utf-8"><title>Reference oracle</title>
<link rel="stylesheet" href="http://localhost:PORT/foreign.css">
<style>:root{font-size:16px;--space:24px}body{margin:0;font-family:Arial,sans-serif}
main{width:min(900px,calc(100vw - 48px));margin:24px auto;padding:var(--space);display:grid;gap:24px;grid-template-columns:1fr 1fr}
h1{font-size:clamp(24px,4vw,64px);line-height:1.25}p{font-size:18px}img{width:128px;height:64px}
@media(max-width:700px){main{grid-template-columns:1fr}}.art{height:80px;background-image:url('/image.svg')}
</style></head><body><nav>UNRELATED_NAV</nav><main id="fixture"><h1>Observed Heading</h1>
<p>Reference body <a href="/detail">Exact detail</a> <input type="password" value="INPUT_SECRET"></p>
<p>UNICODE</p><img src="/image.svg" alt="Measured asset"><div class="art"></div>
<pre>const exact = `preserve`;\n  indentation</pre><iframe title="Local frame" src="/frame"></iframe>
<div hidden>HIDDEN_SECRET</div><div id="shadow"></div>
</main><footer>UNRELATED_FOOTER</footer><script>document.querySelector('#shadow').attachShadow({mode:'open'}).innerHTML='<p>SHADOW_EVIDENCE</p>';</script></body></html>'''.replace('PORT', str(server.server_port)).replace('UNICODE', '界' * 450)
                mime = 'text/html; charset=utf-8'
            data = body.encode()
            self.send_response(200)
            self.send_header('Content-Type', mime)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_POST(self):
            value = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            messages.append(value)
            data = json.dumps({'reply': 'PONG ' + value['value']}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f'http://127.0.0.1:{server.server_port}/'
    try:
        first, elapsed = run('fixture-cold', ['browser', 'reference', url, '--max-text', '256', '--keep-tab'])
        check('cold full reference including screenshot below two seconds', elapsed < 2000)
        ref = first['reference']
        archive_path = Path(ref['snapshot_path'])
        original = archive_path.read_bytes()
        snapshot = json.loads(original)['snapshot']['reference']
        check('main content and shadow text retained', all(t in snapshot['content']['text'] for t in ['Observed Heading', 'SHADOW_EVIDENCE', 'const exact = `preserve`;\n  indentation']))
        check('navigation and field values excluded', all(t not in snapshot['content']['text'] for t in ['UNRELATED_NAV', 'INPUT_SECRET', 'HIDDEN_SECRET', 'UNRELATED_FOOTER']))
        check('same origin frame provenance retained', any('FRAME_EVIDENCE' in f['text'] for f in snapshot['content']['frames']))
        check('link targets retained', any(x['url'] == url + 'detail' for x in snapshot['content']['links']))
        main = next(e for e in snapshot['elements'] if e['selector'] == '#fixture')
        check('computed layout and source math agree with independent fixture', main['computed']['display'] == 'grid' and main['computed']['gap'] == '24px' and
              float(main['computed']['width'].removesuffix('px')) == min(900, snapshot['layout']['viewport']['width'] - 48) and
              any(all(part in r['declarations'] for part in ['min(', '100vw', '48px']) for r in snapshot['styles']) and
              any('clamp(' in r['declarations'] for r in snapshot['layout']['css_math']))
        check('pixel conversion reports measured DPR', main['geometry']['device_pixel_width'] == round(main['rect']['width'] * snapshot['layout']['viewport']['device_pixel_ratio'], 2))
        check('cross origin CSS limitation disclosed while computed color survives', snapshot['coverage']['inaccessible_stylesheets'] >= 1 and
              any(e['computed']['color'] == 'rgb(11, 22, 33)' for e in snapshot['elements'] if e['tag'] == 'h1'))
        check('asset dimensions and kinds retained', any(x['kind'] == 'image' and x['natural_width'] == 64 and x['rendered_width'] == 128 for x in snapshot['assets']) and
              any(x['kind'] == 'background' for x in snapshot['assets']))
        shot = ref['artifacts']['screenshot']
        shot_path = Path(shot['artifact']['path'])
        check('actual JPEG artifact and digest match', shot['status'] == 'captured' and shot_path.read_bytes().startswith(b'\xff\xd8') and hashlib.sha256(shot_path.read_bytes()).hexdigest() == shot['artifact']['sha256'])
        check('private artifact permissions', all(p.stat().st_mode & 0o077 == 0 for p in [archive_path, shot_path, archive_path.with_name('content.md')]))
        collected, current = first.get('text', ''), first
        index = 0
        requests_before = len(requests)
        while current.get('next'):
            current, _ = run(f'content-continuation-{index}', [], command=shlex.split(current['next']))
            collected += current.get('text', '')
            check(f'offline continuation {index}', current['bridge_calls'] == 0 and current['observed_at'] == first['observed_at'])
            index += 1
            assert index < 30
        check('UTF-8 continuation reconstructs exact immutable text', collected == snapshot['content']['text'] and len(requests) == requests_before and archive_path.read_bytes() == original)
        assets, _ = run('assets-slice', [], command=shlex.split(ref['read_sections']['assets']))
        check('section reads have zero model and browser calls', assets['bridge_calls'] == 0 and assets['model_requests'] == 0 and assets['reference']['items'])
        foreign = prefix.copy(); foreign[-1] += '-foreign'
        failed, _ = run('foreign-snapshot', [], expected=1, command=foreign + ['browser', 'reference', '--saved', ref['id'], '--raw-json'])
        check('foreign task snapshot unavailable', failed.get('error') is not None)
        for i in range(3):
            warm, elapsed = run(f'fixture-warm-{i}', ['browser', 'reference', '--tab', str(first['tab_id']), '--screenshot=false'])
            check(f'warm {i} under two seconds with fresh evidence', elapsed < 2000 and warm['model_requests'] == 0 and warm['observed_at'] != first['observed_at'])
        focus, _ = run('focused-reference', ['browser', 'reference', '--tab', str(first['tab_id']), '--selector', 'h1', '--screenshot=false'])
        check('selector focuses content and design', focus['content']['scope'] == 'selected_element' and 'Observed Heading' in focus['text'] and 'Reference body' not in focus['text'])
        bad, _ = run('ambiguous-selector', ['browser', 'reference', '--tab', str(first['tab_id']), '--selector', 'p'], expected=1)
        check('ambiguous selector fails explicitly', bad['code'] == 'selector_ambiguous')
        chat, elapsed = run('late-send-control', ['browser', 'chat', url + 'chat', '--message', 'hydrated-send'])
        check('late Send hydration uses remaining budget and sends exactly once', chat['status'] == 'completed' and chat['response'] == 'PONG hydrated-send' and elapsed < 2000 and messages == [{'value': 'hydrated-send', 'trusted': True}])
        chat_page, _ = run('chat-primary-controls', ['browser', 'page', '--tab', str(chat['tab_id']), '--limit', '3'])
        check('chat controls precede history and text is the conversation', 'Send' in [c['name'] for c in chat_page['controls']] and chat_page['coverage']['text_source'] == 'rendered_chat_messages' and 'SIDEBAR_NOT_CONVERSATION' not in chat_page['text'])

        bounded, elapsed = run('long-controls-0', ['browser', 'page', url + 'long-controls', '--limit', '80', '--max-text', '256'])
        bounded_controls, bounded_pages, current = [], 0, bounded
        while True:
            bounded_pages += 1
            bounded_controls.extend(current.get('controls', []))
            coverage = current['coverage']
            check(f'long-control page {bounded_pages} respects byte budget', coverage['returned_control_bytes'] <= coverage['control_output_budget_bytes'])
            if not current.get('next'):
                break
            current, _ = run(f'long-controls-{bounded_pages}', [], command=shlex.split(current['next']))
            assert bounded_pages < 20
        check('long controls paginate gaplessly without context blow-up', elapsed < 2000 and bounded['coverage']['controls_limited_by_bytes'] and
              len(bounded_controls) == 40 and len({c['ref'] for c in bounded_controls}) == 40)

        large, elapsed = run('large-dom', ['browser', 'page', url + 'large-dom', '--max-text', '256'])
        check('large DOM returns bounded partial coverage instead of a retry failure', elapsed < 2000 and large['status'] == 'partial' and
              large['coverage']['dom_scan_limited'] and large['coverage']['dom_nodes_scanned'] == large['coverage']['dom_node_scan_limit'] and
              '--cursor ' in large.get('next', '') and large.get('code') != 'dom_budget_exceeded')

        fonts, elapsed = run('long-fonts', ['browser', 'reference', url + 'long-fonts', '--screenshot=false'])
        preview_coverage = fonts['reference']['preview']['preview_coverage']
        font_coverage = preview_coverage['groups']['font_stacks']
        check('pathological font stacks keep a useful hard-bounded preview', elapsed < 2000 and preview_coverage['encoded_bytes'] <= preview_coverage['budget_bytes'] == 8192 and
              font_coverage['total'] >= 20 and font_coverage['returned'] > 0 and font_coverage['omitted'] > 0 and font_coverage['records_clipped'] > 0)
        delayed, elapsed = run('delayed-document-initial', ['browser', 'reference', url + 'delayed-document', '--screenshot=false'])
        check('slow document retains a below-two-second pending result', elapsed < 2000 and delayed['status'] == 'loading' and delayed['next'])
        resumed, resumed_elapsed = run('delayed-document-resume', [], command=shlex.split(delayed['next']))
        check('one continuation waits for the body instead of polling', resumed['status'] == 'ready' and resumed['tab_state'] == 'closed' and 'DELAYED_BODY_READY' in resumed['text'] and 300 <= resumed_elapsed < 2000)
        if a.live:
            sites = {
                'apple': 'https://www.apple.com/',
                'canvas': 'https://canvas.unm.edu/courses/72971',
                'docs': 'https://docs.google.com/document/d/1anx9nq83K9ZWbCSk4a1f5jw7Ncu_954gMcj4giLw73U/edit?tab=t.0',
            }
            for name, site in sites.items():
                # Retain every cold outcome. An unavailable source is not a passing sample.
                cold, cold_elapsed = run(name + '-cold', ['browser', 'reference', site, '--keep-tab'])
                check(name + ' initial browser/context command below two seconds', cold_elapsed < 2000)
                if cold['status'] == 'loading':
                    cold, _ = run(name + '-continuation', [], command=shlex.split(cold['next']))
                check(name + ' real source ready', cold['status'] == 'ready')
                if name == 'canvas':
                    check('real Canvas course modules and links', 'STAT-345' in cold.get('text', '') and cold['reference']['counts']['links'] >= 10)
                elif name == 'apple':
                    check('real Apple font assets and CSS evidence', any('SF Pro' in s for s in cold['reference']['preview']['font_stacks']) and cold['reference']['counts']['styles'] > 0 and cold['reference']['counts']['assets'] > 10)
                else:
                    check('real Google Docs selected tab export, not toolbar text', cold['content']['source'] == 'google_docs_text_export' and cold['content']['complete'] and 'FileEditView' not in cold.get('text', ''))
                for i in range(3):
                    warm, elapsed = run(f'{name}-warm-{i}', ['browser', 'reference', '--tab', str(cold['tab_id'])])
                    check(f'{name} warm {i} below two seconds', elapsed < 2000 and warm['status'] == 'ready')
    finally:
        for tab in tabs:
            p = subprocess.run(prefix + ['browser', 'close', '--tab', str(tab), '--raw-json'], capture_output=True, text=True)
            (out / f'close-{tab}.json').write_text(p.stdout)
        server.shutdown()
        report = {'checks': checks, 'measurements': measurements, 'fixture_requests': requests, 'fixture_messages': messages}
        (out / 'results.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
