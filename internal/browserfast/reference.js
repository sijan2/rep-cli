async function repReference(spec) {
  'use strict';
  const started = performance.now(), pageURL = location.href;
  const isDocument = location.hostname === 'docs.google.com' && /^\/document\/(?:u\/\d+\/)?d\/[^/]+\/(?:edit|view|preview)\/?$/.test(location.pathname);
  const norm = s => String(s ?? '').replace(/\s+/g, ' ').trim();
  const clip = (s, n = 500) => { const bytes = new TextEncoder().encode(String(s ?? '')); let end = Math.min(bytes.length, n); while (end && (bytes[end] & 192) === 128) end--; return new TextDecoder().decode(bytes.slice(0, end)); };
  const round = n => Math.round(n * 100) / 100;
  const urlOf = (s, base = pageURL) => { if (!s) return ''; try { const u = new URL(s, base); return /^https?:$/.test(u.protocol) && !u.username && !u.password ? u.href : ''; } catch { return ''; } };
  const selectorOf = el => {
    if (el.id) return '#' + CSS.escape(el.id);
    const parts = [];
    for (let n = el; n && parts.length < 5; n = n.parentElement) {
      let part = n.localName;
      if (!part) break;
      if (n.id) { parts.unshift('#' + CSS.escape(n.id)); break; }
      const siblings = n.parentElement ? [...n.parentElement.children].filter(e => e.localName === n.localName) : [];
      if (siblings.length > 1) part += ':nth-of-type(' + (siblings.indexOf(n) + 1) + ')';
      parts.unshift(part);
    }
    return parts.join(' > ');
  };
  try {
	const readyDeadline = started + Math.min(60000, Math.max(0, Number(spec.wait_ms) || 0));
	while (document.readyState === 'loading' || !document.body) {
	  const remaining = readyDeadline - performance.now();
	  if (remaining <= 0) return {status: 'loading', url: pageURL, title: document.title, code: 'document_loading'};
	  await new Promise(resolve => setTimeout(resolve, Math.min(50, remaining)));
	}
    let focus = null;
    if (spec.selector) {
      let matches;
      try { matches = document.querySelectorAll(spec.selector); } catch { return {status: 'failed', code: 'invalid_selector', url: pageURL}; }
      if (matches.length !== 1) return {status: 'failed', code: matches.length ? 'selector_ambiguous' : 'selector_not_found', url: pageURL};
      focus = matches[0];
    }
    const contentPromise = repReadContent({...spec, wait_ms: Math.max(0, readyDeadline - performance.now())});
    const main = focus || document.querySelector('main,[role="main"],#content') || document.body;
    if (!main) return {status: 'loading', url: pageURL, title: document.title, code: 'document_loading'};
    const samples = [], sampleSet = new Set();
    const addSample = el => {
      if (!el || sampleSet.has(el) || samples.length >= 24) return;
      const r = el.getBoundingClientRect(), s = getComputedStyle(el);
      if (!r.width || !r.height || s.display === 'none' || s.visibility === 'hidden') return;
      samples.push(el); sampleSet.add(el);
    };
    addSample(focus || main);
    if (!focus) { addSample(document.documentElement); addSample(document.body); }
    for (const el of [...main.children].slice(0, 4)) addSample(el);
    for (const el of [...main.querySelectorAll('h1,h2,h3')].slice(0, 6)) { addSample(el); addSample(el.parentElement); }
    for (const el of main.querySelectorAll('section,article,figure,p,button,a[href],img')) addSample(el);
    addSample(document.body); addSample(document.documentElement);
    const properties = ['display','position','box-sizing','width','height','min-width','max-width','min-height','max-height','margin','padding','gap','row-gap','column-gap',
      'grid-template-columns','grid-template-rows','grid-auto-flow','flex-direction','flex-wrap','align-items','justify-content','aspect-ratio',
      'font-family','font-size','font-weight','line-height','letter-spacing','text-align','color','background-color','background-image','background-size','background-position',
      'border','border-radius','box-shadow','object-fit','object-position','transform','transform-origin','opacity','overflow','z-index','animation','transition'];
    const rootRem = parseFloat(getComputedStyle(document.documentElement).fontSize);
    const elements = samples.map((el, index) => {
      const s = getComputedStyle(el), r = el.getBoundingClientRect(), parent = el.parentElement?.getBoundingClientRect();
      const computed = Object.fromEntries(properties.map(p => [p, clip(s.getPropertyValue(p), 1000)]));
      const rect = {x: round(r.x), y: round(r.y), width: round(r.width), height: round(r.height), document_x: round(r.x + scrollX), document_y: round(r.y + scrollY)};
      const selector = selectorOf(el);
      return {id: 'n' + index, selector, selector_unique: document.querySelectorAll(selector).length === 1, tag: el.localName, text: clip(norm(el.innerText || el.getAttribute('aria-label') || el.getAttribute('alt')), 120),
        viewport_visible: r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth, rect, computed,
        inline_style: clip(el.getAttribute('style') || '', 3000),
        geometry: {units: 'CSS pixels; bounding rect includes transforms', width_rem: rootRem ? round(r.width / rootRem) : null,
          width_viewport_percent: innerWidth ? round(100 * r.width / innerWidth) : null, width_parent_percent: parent?.width ? round(100 * r.width / parent.width) : null,
          device_pixel_width: round(r.width * devicePixelRatio), formula: 'rem=CSS_px/root_font_px; viewport_percent=100*CSS_px/viewport_width; device_px=CSS_px*DPR'}};
    });
    const assets = [], seenAssets = new Set(); let assetCandidates = 0;
    const asset = (kind, src, details = {}) => {
      const url = urlOf(src); if (!url) return;
      const key = kind + ':' + url; if (seenAssets.has(key)) return;
      seenAssets.add(key); assetCandidates++;
      if (assets.length < 512) assets.push({kind, url, ...details});
    };
    for (const img of document.images) {
      const r = img.getBoundingClientRect();
      asset('image', img.currentSrc || img.src, {selector: selectorOf(img), alt: clip(img.alt, 200), src: urlOf(img.src),
        srcset: clip(img.getAttribute('srcset') || '', 2048), sizes: clip(img.sizes, 400), natural_width: img.naturalWidth,
        natural_height: img.naturalHeight, rendered_width: round(r.width), rendered_height: round(r.height), loading: img.loading, decoded: img.complete && img.naturalWidth > 0,
        picture_sources: [...(img.closest('picture')?.querySelectorAll('source') || [])].slice(0, 12).map(s => ({media: clip(s.media, 300), type: s.type, srcset: clip(s.srcset, 2048), sizes: clip(s.sizes, 300)}))});
    }
    for (const el of document.querySelectorAll('video,audio,source')) {
      asset(el.localName, el.currentSrc || el.src, {type: el.type || '', srcset: clip(el.srcset || '', 2048)});
      if (el.poster) asset('poster', el.poster);
    }
    for (const script of document.scripts) if (script.src) asset('script', script.src, {type: script.type || 'classic', async: script.async, defer: script.defer});
    const bgDeadline = performance.now() + 80; let backgroundScanned = 0, backgroundLimited = false;
    const walk = document.createTreeWalker(main, NodeFilter.SHOW_ELEMENT); let node = main;
    do {
      if (++backgroundScanned > 5000 || performance.now() > bgDeadline) { backgroundLimited = true; break; }
      const bg = getComputedStyle(node).backgroundImage;
      for (const m of bg.matchAll(/url\(["']?([^"')]+)["']?\)/g)) asset('background', m[1], {selector: selectorOf(node), background_image: clip(bg, 1500)});
    } while ((node = walk.nextNode()));
    const styles = [], sheets = [], fontFaces = [], maths = [], media = [];
    let ruleCount = 0, rulesLimited = false, ruleBytes = 0, matchedCount = 0, fontFaceCount = 0, mathCount = 0;
    // Document text has priority over sampling the editor's large CSS bundles.
    const cssDeadline = performance.now() + (isDocument ? 50 : 200);
    const ruleLimit = isDocument ? 3000 : 12000;
    const knownSheets = new Set();
    const readSheet = (sheet, inherited = [], depth = 0) => {
      if (!sheet || knownSheets.has(sheet) || depth > 16) return;
      knownSheets.add(sheet);
      const source = sheet.href || 'inline:' + knownSheets.size;
      if (sheet.href) asset('stylesheet', sheet.href);
      const conditions = [...inherited];
      if (sheet.media?.mediaText) conditions.push({kind: 'media', text: sheet.media.mediaText, active: matchMedia(sheet.media.mediaText).matches});
      if (sheet.disabled) conditions.push({kind: 'disabled_stylesheet', text: '', active: false});
      try { const list = sheet.cssRules; sheets.push({url: source, accessible: true, rules: list.length}); readRules(list, source, conditions, depth); }
      catch { sheets.push({url: source, accessible: false, reason: 'cssom_access_denied'}); }
    };
    const readRules = (list, source, conditions, depth) => {
      if (depth > 16) { rulesLimited = true; return; }
      for (const rule of list) {
        if (++ruleCount > ruleLimit || performance.now() > cssDeadline || ruleBytes > 400000) { rulesLimited = true; return; }
        if (rule.type === 3) { readSheet(rule.styleSheet, conditions, depth + 1); continue; }
        let next = conditions;
        if (rule.conditionText) {
          const kind = rule.constructor.name === 'CSSMediaRule' ? 'media' : rule.constructor.name === 'CSSSupportsRule' ? 'supports' : rule.constructor.name;
          let active = null;
          try { if (kind === 'media') active = matchMedia(rule.conditionText).matches; if (kind === 'supports') active = CSS.supports(rule.conditionText); } catch {}
          next = [...conditions, {kind, text: clip(rule.conditionText, 500), active}];
          if (kind === 'media' && media.length < 128 && !media.some(m => m.text === rule.conditionText)) media.push({text: rule.conditionText, active});
        }
        if (rule.type === 5) {
          fontFaceCount++;
          if (fontFaces.length < 128) fontFaces.push({source, family: rule.style.getPropertyValue('font-family'), src: clip(rule.style.getPropertyValue('src'), 2000),
            weight: rule.style.getPropertyValue('font-weight'), style: rule.style.getPropertyValue('font-style'), display: rule.style.getPropertyValue('font-display'), conditions: next});
          for (const m of rule.style.getPropertyValue('src').matchAll(/url\(["']?([^"')]+)["']?\)/g)) asset('font', urlOf(m[1], source.startsWith('inline:') ? pageURL : source));
        }
        if (rule.selectorText && rule.style) {
          const matched = [];
          samples.forEach((el, i) => { try { if (el.matches(rule.selectorText)) matched.push('n' + i); } catch {} });
          const css = rule.style.cssText;
          const math = /(?:calc|clamp|min|max|var)\(|\d(?:\.\d+)?(?:vw|vh|rem|%)/.test(css);
          if (matched.length) {
            matchedCount++;
            if (styles.length < 256) { const declarations = clip(css, 3000); ruleBytes += declarations.length; styles.push({source, order: ruleCount, selector: clip(rule.selectorText, 1000), elements: matched,
              declarations, declarations_truncated: declarations !== css, conditions: next, active: next.some(c => c.active === false) ? false : next.some(c => c.active === null) ? null : true}); }
          }
          if (math) {
            mathCount++;
            const score = (/(?:calc|clamp|min|max)\(/.test(css) ? 200 : 0) + (matched.length ? 20 : 0) + (/(?:width|height|gap|padding|margin|font-size):/.test(css) ? 10 : 0);
            if (maths.length < 64 || score > maths.at(-1).score) {
              maths.push({score, source, selector: clip(rule.selectorText, 500), elements: matched, conditions: next,
                declarations: clip([...rule.style].filter(p => /(?:calc|clamp|min|max|var)\(|\d(?:\.\d+)?(?:vw|vh|rem|%)/.test(rule.style.getPropertyValue(p))).map(p => p + ': ' + rule.style.getPropertyValue(p)).join('; '), 2000)});
              maths.sort((a,b) => b.score - a.score);
              if (maths.length > 64) maths.pop();
            }
          }
        }
        if (rule.cssRules) readRules(rule.cssRules, source, next, depth + 1);
      }
    };
    for (const sheet of [...document.styleSheets, ...(document.adoptedStyleSheets || [])]) readSheet(sheet);
    const custom = getComputedStyle(focus || document.documentElement);
    const variables = [...custom].filter(p => p.startsWith('--')).slice(0, 128).map(name => ({name, value: clip(custom.getPropertyValue(name), 1000)}));
    const fontStacks = [...new Set(elements.map(e => e.computed['font-family']))];
    const fontSet = [...document.fonts].slice(0, 128).map(f => ({family: f.family, weight: f.weight, style: f.style, status: f.status, display: f.display}));
    const resources = performance.getEntriesByType('resource');
    const known = new Set(['img', 'css', 'script', 'link', 'video', 'audio']);
    for (const r of resources) {
      if (/\.(?:woff2?|ttf|otf)(?:[?#]|$)/i.test(r.name)) asset('font', r.name);
      else if (known.has(r.initiatorType)) asset('resource:' + r.initiatorType, r.name);
    }
    const nav = performance.getEntriesByType('navigation')[0];
    const paints = Object.fromEntries(performance.getEntriesByType('paint').map(p => [p.name, round(p.startTime)]));
    const performanceInfo = {units: 'milliseconds since this document navigation', observed_at_ms: round(performance.now()),
      navigation: nav ? {type: nav.type, ttfb_ms: round(nav.responseStart - nav.requestStart), dom_content_loaded_ms: nav.domContentLoadedEventEnd || null,
        load_ms: nav.loadEventEnd || null, response_end_ms: round(nav.responseEnd), transfer_bytes: nav.transferSize, decoded_bytes: nav.decodedBodySize} : null,
      paint: paints, resources: {observed: resources.length, transfer_bytes_visible: resources.reduce((n, r) => n + r.transferSize, 0),
        zero_transfer_entries: resources.filter(r => !r.transferSize).length, slowest: [...resources].sort((a,b) => b.duration - a.duration).slice(0, 12).map(r => ({url: urlOf(r.name), type: r.initiatorType, duration_ms: round(r.duration), transfer_bytes: r.transferSize}))},
      limits: ['Single navigation observation; not a benchmark or FPS measurement.', 'Resource timing buffer can omit earlier entries; zero sizes can mean cache or cross-origin timing restrictions.', 'Background tabs may defer paints; missing paint entries are unknown.']};
    const animations = document.getAnimations().slice(0, 32).map(a => { const t = a.effect?.getTiming?.() || {}; return {name: a.animationName || '', target: a.effect?.target ? selectorOf(a.effect.target) : '', play_state: a.playState, duration_ms: t.duration === Infinity ? 'infinite' : t.duration, delay_ms: t.delay, easing: t.easing, iterations: t.iterations === Infinity ? 'infinite' : t.iterations}; });
    for (const math of maths) delete math.score;
    const content = await contentPromise;
    if (location.href !== pageURL) return {status: 'failed', code: 'page_changed', url: location.href};
    const reference = {content, elements, styles, assets, fonts: {computed_stacks: fontStacks, faces: fontFaces, loaded_faces: fontSet, status: document.fonts.status,
        scope: 'Computed CSS stacks and FontFaceSet loading state; actual per-glyph fallback is not inferred.'},
      layout: {viewport: {width: innerWidth, height: innerHeight, device_pixel_ratio: devicePixelRatio, root_font_px: rootRem, scroll_x: scrollX, scroll_y: scrollY},
        media_queries: media, variables, css_math: maths, animations, stylesheets: sheets}, performance: performanceInfo,
      coverage: {scope: 'Top document; representative visible boxes at the current viewport, including offscreen layout. Rules are matching candidates, not cascade winners.',
        design_scope: isDocument ? 'Google Docs editor UI; document glyph fonts, rich formatting and canvas pixels are not extracted.' : 'Rendered top-document elements; unopened and virtualized content may be absent.',
        selector: spec.selector || null, sampled_elements: elements.length, background_nodes_scanned: backgroundScanned, background_scan_limited: backgroundLimited,
        rules_scanned: ruleCount, rules_scan_limited: rulesLimited, matched_rules: matchedCount, omitted_matched_rules: Math.max(0, matchedCount - styles.length),
        inaccessible_stylesheets: sheets.filter(s => !s.accessible).length, assets_observed: assetCandidates, omitted_assets: Math.max(0, assetCandidates - assets.length),
        font_faces_observed: fontFaceCount, omitted_font_faces: Math.max(0, fontFaceCount - fontFaces.length), font_set_faces: document.fonts.size,
        omitted_loaded_faces: Math.max(0, document.fonts.size - fontSet.length), math_rules_observed: mathCount, omitted_math_rules: Math.max(0, mathCount - maths.length),
        extraction_ms: round(performance.now() - started), iframe_contents: 'See content.frames; styles inside frames and shadow roots are outside this design sample.'}};
    return {status: content.status === 'pending' ? 'loading' : ['unavailable','partial'].includes(content.status) ? 'partial' : 'ready', url: pageURL, title: document.title,
      observed_at: new Date().toISOString(), reference};
  } catch (error) { return {status: 'failed', code: 'reference_renderer_failed', url: location.href}; }
}
