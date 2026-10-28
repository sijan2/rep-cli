async function repReadContent(spec) {
  'use strict';
  spec ||= {};
  const startURL = location.href, enc = new TextEncoder(), maxBytes = 512 * 1024;
  const clip = (s, n) => {
    const b = enc.encode(String(s ?? '')); let end = Math.min(n, b.length);
    while (end && (b[end] & 192) === 128) end--;
    return new TextDecoder().decode(b.slice(0, end));
  };
  const norm = s => String(s ?? '').replace(/\s+/g, ' ').trim();
  const urlOf = (s, base = location.href) => {
    if (!s) return '';
    try { const u = new URL(s, base); return /^https?:$/.test(u.protocol) && !u.username && !u.password ? u.href : ''; } catch { return ''; }
  };
  const doc = location.hostname === 'docs.google.com' && location.pathname.match(/^\/document\/(?:u\/\d+\/)?d\/([a-zA-Z0-9_-]+)\/(?:edit|view|preview)\/?$/);
  if (doc) {
    const tab = new URL(location.href).searchParams.get('tab');
    const source = new URL('/document/d/' + doc[1] + '/export', location.origin);
    source.searchParams.set('format', 'txt');
    if (tab) source.searchParams.set('tab', tab);
    const base = {source: 'google_docs_text_export', source_url: source.href, scope: tab ? 'selected_document_tab' : 'default_document_tab', tab, format: 'text', headings: [], links: [], text: '', complete: false};
    if (!(spec.wait_ms > 0)) return {...base, status: 'pending', reason: 'read_budget_exhausted'};
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), spec.wait_ms);
    try {
      const response = await fetch(source.href, {credentials: 'same-origin', signal: controller.signal});
      const mime = (response.headers.get('content-type') || '').split(';')[0].trim().toLowerCase();
      // Signed redirect URLs stay inside the browser; only the stable export URL is retained.
      const host = new URL(response.url).hostname;
      if (!response.ok || mime !== 'text/plain' || !(host === 'docs.google.com' || host.endsWith('.googleusercontent.com'))) {
        await response.body?.cancel();
        return {...base, status: 'unavailable', reason: response.status === 401 || response.status === 403 || host === 'accounts.google.com' ? 'authentication_or_access_required' : 'document_export_rejected', http_status: response.status, content_type: mime};
      }
      const reader = response.body?.getReader();
      if (!reader) return {...base, status: 'unavailable', reason: 'document_export_body_unavailable'};
      const chunks = []; let size = 0, complete = false;
      try {
        while (true) {
          const {value, done} = await reader.read();
          if (done) { complete = true; break; }
          const room = maxBytes - size;
          if (value.length > room) { chunks.push(value.slice(0, room)); size += room; break; }
          chunks.push(value); size += value.length;
        }
      } finally { if (!complete) await reader.cancel().catch(() => {}); reader.releaseLock(); }
      const bytes = new Uint8Array(size); let offset = 0;
      for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
      // Streaming decode without flush drops an incomplete final UTF-8 code point.
      const text = new TextDecoder().decode(bytes, {stream: !complete});
      if (location.href !== startURL) return {...base, status: 'unavailable', reason: 'document_changed'};
      return {...base, status: complete ? (text.length ? 'ready' : 'empty') : 'partial', complete, text, bytes: enc.encode(text).length,
        http_status: response.status, content_type: mime, limit_bytes: maxBytes, omitted_bytes: complete ? 0 : null};
    } catch (error) {
      return {...base, status: controller.signal.aborted ? 'pending' : 'unavailable', reason: controller.signal.aborted ? 'document_export_timeout' : 'document_export_failed'};
    } finally { clearTimeout(timer); }
  }
  let root = spec.selector ? document.querySelector(spec.selector) : null;
  if (spec.selector && !root) return {status: 'unavailable', reason: 'selector_not_found', text: '', complete: false};
  if (!root) root = document.querySelector('main,[role="main"],#content') || document.body;
  if (!root) return {status: 'pending', reason: 'document_loading', text: '', complete: false};
  const headings = [], links = [], frames = [], seenLinks = new Set();
  let visited = 0, omitted = false, bytes = 0, hidden = 0;
  const deadline = performance.now() + 250, output = [];
  const put = s => {
    const b = enc.encode(s).length;
    if (bytes + b > maxBytes) { output.push(clip(s, maxBytes - bytes)); bytes = maxBytes; omitted = true; return; }
    output.push(s); bytes += b;
  };
  const visible = el => {
    if (el.closest('[hidden],[inert],[aria-hidden="true"]')) return false;
    const style = getComputedStyle(el);
    return style.display !== 'none' && style.visibility !== 'hidden';
  };
  const skip = /^(SCRIPT|STYLE|NOSCRIPT|TEMPLATE|NAV|ASIDE|FOOTER|INPUT|TEXTAREA|SELECT|BUTTON|IFRAME)$/;
  const walk = (node, depth = 0) => {
    if (omitted) return;
    if (++visited > 20000 || depth > 128 || performance.now() > deadline) { omitted = true; return; }
    if (node.nodeType === Node.TEXT_NODE) { put(node.textContent.replace(/\s+/g, ' ')); return; }
    if (node.nodeType !== Node.ELEMENT_NODE && node.nodeType !== Node.DOCUMENT_FRAGMENT_NODE) return;
    const tag = node.tagName || '';
    if (skip.test(tag)) return;
    if (tag && !visible(node)) { hidden++; return; }
    const block = /^(DIV|SECTION|ARTICLE|P|UL|OL|BLOCKQUOTE|FIGURE|TR|H[1-6])$/.test(tag);
    if (block) put('\n\n');
    if (/^H[1-6]$/.test(tag)) {
      const title = norm(node.innerText);
      if (headings.length < 256) headings.push({level: +tag[1], text: clip(title, 500)});
      put('#'.repeat(+tag[1]) + ' ');
    }
    if (tag === 'BR') { put('\n'); return; }
    if (tag === 'LI') put('\n- ');
    if (tag === 'TD' || tag === 'TH') put(' | ');
    if (tag === 'PRE') { const value = node.innerText || ''; const fence = '`'.repeat(Math.max(3, ...Array.from(value.matchAll(/`+/g), m => m[0].length + 1))); put('\n' + fence + '\n' + value + '\n' + fence + '\n'); return; }
    if (tag === 'A') {
      const url = urlOf(node.getAttribute('href'));
      const label = clip(norm(node.innerText || node.getAttribute('aria-label')), 500);
      if (url && label) {
        if (!seenLinks.has(url) && links.length < 512) { links.push({text: label, url}); seenLinks.add(url); }
        put('[' + label.replace(/([\[\]\\])/g, '\\$1') + '](' + url.replace(/\)/g, '%29') + ')'); return;
      }
    }
    if (tag === 'IMG') {
      const src = urlOf(node.currentSrc || node.getAttribute('src'));
      if (src) put('![' + clip(norm(node.alt), 200).replace(/\]/g, '\\]') + '](' + src.replace(/\)/g, '%29') + ')');
      return;
    }
    for (const child of (node.shadowRoot || node).childNodes) walk(child, depth + 1);
    if (block) put('\n\n');
  };
  walk(root);
  // Frame contents have independent provenance and never masquerade as the parent.
  for (const frame of document.querySelectorAll('iframe')) {
    if (frames.length >= 32) break;
    let text = '', status = 'cross_origin_or_unavailable', truncated = false;
    try {
      if (frame.contentDocument?.body && visible(frame)) {
        const full = frame.contentDocument.body.innerText || '';
        text = clip(full, 16384); truncated = enc.encode(full).length > enc.encode(text).length; status = 'same_origin_text';
      }
    } catch {}
    frames.push({url: urlOf(frame.getAttribute('src')), title: clip(frame.title, 200), status, text, truncated});
  }
  const text = output.join('').replace(/[ \t]+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
  const totalFrames = document.querySelectorAll('iframe').length;
  return {status: document.readyState === 'loading' ? 'pending' : omitted ? 'partial' : text ? 'ready' : 'empty',
    source: 'rendered_dom', scope: spec.selector ? 'selected_element' : root === document.body ? 'document_body' : 'main_landmark', format: 'markdown',
    text, bytes: enc.encode(text).length, complete: !omitted && document.readyState !== 'loading', headings, links, frames,
    coverage: {nodes_visited: visited, hidden_subtrees_skipped: hidden, extraction_limited: omitted,
      iframes: totalFrames, omitted_frames: Math.max(0, totalFrames - frames.length), canvas_elements: document.querySelectorAll('canvas').length, canvas_pixels_read: false,
      scope: 'current_rendered_content; virtualized, unopened and cross-origin content may be absent'}};
}
