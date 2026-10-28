(() => {
  const DROP = new Set(['SCRIPT','STYLE','NOSCRIPT','NAV','FOOTER','HEADER','FORM','IFRAME','ASIDE','BUTTON','SELECT','TEXTAREA']);
  const BLOCK = /^(P|DIV|LI|H[1-6]|PRE|BLOCKQUOTE|SECTION|ARTICLE|UL|OL|TABLE|TR|FIGURE|HR|BR)$/;

  // --- pick main content: prefer semantic containers, penalize link-farms (archives/sidebars)
  const density = (el) => {
    let txt = 0, linkTxt = 0;
    for (const p of el.querySelectorAll('p,li,pre,code,h1,h2,h3,h4,blockquote,td,figcaption')) txt += (p.innerText||'').length;
    for (const a of el.querySelectorAll('a')) linkTxt += (a.innerText||'').length;
    // archive sidebars are ~all link text; real articles are mostly prose
    return txt - 1.6 * linkTxt;
  };
  let best = null, bestScore = -1e9;
  const prefer = ['article','main','[role=main]','.post-body','.entry-content','.markdown-body','.prose','.post','#content','.content'];
  let pool = [];
  for (const sel of prefer) pool.push(...document.querySelectorAll(sel));
  if (pool.length === 0) pool = [...document.querySelectorAll('section,div')];
  for (const el of pool) {
    if (el.closest('nav,footer,header,aside')) continue;
    const s = density(el);
    if (s > bestScore) { bestScore = s; best = el; }
  }
  if (!best || bestScore < 200) best = document.body;

  const esc = (s) => s.replace(/\s+/g,' ').trim();
  const out = [];
  const headings = [];
  const emit = (s) => { if (s!=null) out.push(s); };

  const inlineOf = (el) => {
    // inline rendering that preserves links and inline code
    let s = '';
    for (const n of el.childNodes) {
      if (n.nodeType === 3) s += n.textContent.replace(/\s+/g,' ');
      else if (n.nodeType === 1) {
        if (DROP.has(n.tagName)) continue;
        if (n.tagName === 'A') {
          const t = esc(n.innerText||''); const h = n.getAttribute('href')||'';
          s += (t && h && !h.startsWith('#')) ? `[${t}](${(()=>{try{return new URL(h,location.href).href}catch{return h}})()})` : t;
        } else if (n.tagName === 'CODE') {
          s += '`' + (n.textContent||'').replace(/`/g,'⁣`') + '`';
        } else if (n.tagName === 'IMG') {
          const alt = esc(n.getAttribute('alt')||''); let src=n.getAttribute('src')||'';
          try { src = new URL(src, location.href).href; } catch {}
          if (!src.startsWith('data:')) s += `![${alt||'image'}](${src})`;
          else if (alt) s += `![${alt}](data-uri-omitted)`;
        } else s += inlineOf(n);
      }
    }
    return s;
  };

  const walk = (el) => {
    for (const n of el.childNodes) {
      if (n.nodeType === 3) { const t = esc(n.textContent); if (t) emit(t); continue; }
      if (n.nodeType !== 1) continue;
      const tag = n.tagName;
      if (DROP.has(tag)) continue;

      if (tag === 'PRE') {                      // VERBATIM — exploit payloads must survive
        const code = n.innerText.replace(/ /g,' ');
        const langAttr = (n.querySelector('code')?.className || n.className || '');
        const lang = (langAttr.match(/(?:language|lang|highlight)[-_]([a-z0-9+#]+)/i)||[])[1] || '';
        emit('\n```' + lang + '\n' + code.replace(/\n+$/,'') + '\n```\n');
        continue;
      }
      if (/^H[1-6]$/.test(tag)) {
        const t = esc(n.innerText); if (!t) continue;
        const lvl = +tag[1];
        headings.push({ level: lvl, text: t });
        emit('\n' + '#'.repeat(lvl) + ' ' + t + '\n');
        continue;
      }
      if (tag === 'TABLE') {                    // real markdown tables
        const rows = [...n.querySelectorAll('tr')];
        if (!rows.length) continue;
        const cells = (tr) => [...tr.querySelectorAll('th,td')].map(c => esc(inlineOf(c)).replace(/\|/g,'\\|') || ' ');
        const head = cells(rows[0]);
        if (head.length) {
          emit('\n| ' + head.join(' | ') + ' |');
          emit('| ' + head.map(()=>'---').join(' | ') + ' |');
          for (const tr of rows.slice(1)) { const c = cells(tr); if (c.length) emit('| ' + c.join(' | ') + ' |'); }
          emit('');
        }
        continue;
      }
      if (tag === 'IMG') { const s = inlineOf(n.parentElement === el ? n : n); const alt=esc(n.getAttribute('alt')||''); let src=n.getAttribute('src')||'';
        try{src=new URL(src,location.href).href}catch{}
        if (!src.startsWith('data:')) emit(`![${alt||'image'}](${src})`); else if (alt) emit(`![${alt}](data-uri-omitted)`);
        continue; }
      if (tag === 'LI') { const s = esc(inlineOf(n)); if (s) emit('- ' + s); continue; }
      if (tag === 'BLOCKQUOTE') { const s = esc(inlineOf(n)); if (s) emit('> ' + s); continue; }
      if (tag === 'A' || tag === 'CODE' || tag === 'SPAN' || tag === 'EM' || tag === 'STRONG' || tag === 'B' || tag === 'I') {
        const s = esc(inlineOf(n)); if (s) emit(s); continue;
      }
      if (tag === 'SVG' || tag === 'svg') {     // keep SVG text (chart labels/values)
        const t = esc(n.textContent||''); if (t && t.length > 2) emit('[svg-text] ' + t);
        continue;
      }
      walk(n);
      if (BLOCK.test(tag)) emit('');
    }
  };
  walk(best);

  let text = out.join('\n')
    .replace(/[ \t]+\n/g,'\n')
    .replace(/\n{3,}/g,'\n\n')
    .replace(/⁣/g,'')
    .trim();

  const links = []; const seen = new Set();
  for (const a of document.querySelectorAll('a[href]')) {
    let h; try { h = new URL(a.getAttribute('href'), location.href).href; } catch { continue; }
    if (!/^https?:/.test(h) || seen.has(h)) continue; seen.add(h);
    const t = esc(a.innerText||''); if (t) links.push(t.slice(0,90) + ' :: ' + h);
  }
  return JSON.stringify({
    url: location.href, title: document.title, chars: text.length,
    fetched_at: new Date().toISOString(),
    headings, text, links,
    stats: { code_blocks: (text.match(/```/g)||[]).length/2, tables: (text.match(/^\| --- /gm)||[]).length, images: (text.match(/!\[/g)||[]).length }
  });
})()
