async function repFast(spec) {
  'use strict';
  const norm = value => String(value ?? '').replace(/\s+/g, ' ').trim();
  const clip = (value,limit) => {
    const bytes=new TextEncoder().encode(value);
    if(bytes.length<=limit) return value;
    let end=limit;
    while(end>0 && (bytes[end]&0xc0)===0x80) end--;
    return new TextDecoder().decode(bytes.slice(0,end));
  };
  let attempted=false;
  const fail = code => { throw Object.assign(new Error(code), {code}); };
  const visible = el => {
    if (!el?.isConnected || el.closest('[hidden],[inert],[aria-hidden="true"]')) return false;
    const style = getComputedStyle(el), r = el.getBoundingClientRect();
    return style.display !== 'none' && style.visibility === 'visible' && r.width > 0 && r.height > 0 &&
      el.checkVisibility?.({checkOpacity:true,checkVisibilityCSS:true}) !== false;
  };
  const enabled = el => !el.matches(':disabled') && !el.closest('[aria-disabled="true"],[inert]');
  const text = el => norm(el.innerText ?? el.textContent);
  const name = el => norm(el.getAttribute('aria-label') ||
    (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean).map(id => el.getRootNode().getElementById?.(id)?.textContent || '').join(' ') ||
    [...(el.labels || [])].map(text).join(' ') || el.getAttribute('placeholder') ||
    (el.matches('button,a,summary,[role]') ? text(el) : '') || el.getAttribute('title') || el.getAttribute('alt'));
  const role = el => el.getAttribute('role') || ({BUTTON:'button',TEXTAREA:'textbox',SELECT:'combobox',A:'link',SUMMARY:'button'}[el.tagName]) ||
    (el.tagName === 'INPUT' ? ({checkbox:'checkbox',radio:'radio',search:'searchbox',submit:'button',button:'button'}[el.type] || 'textbox') : el.isContentEditable ? 'textbox' : '');
  const domScan = {nodes:0,nodeLimit:50000,limited:false,shadowRoots:0,treeLimit:64,shadowLimited:false};
  let rootCache;
  const roots = () => {
    if (rootCache) return rootCache;
    const trees = [document];
    scan: for (let i=0;i<trees.length;i++) for (const el of trees[i].querySelectorAll('*')) {
      if (domScan.nodes >= domScan.nodeLimit) { domScan.limited=true; break scan; }
      domScan.nodes++;
      if (el.shadowRoot) {
        if (trees.length >= domScan.treeLimit) domScan.shadowLimited=true;
        else { trees.push(el.shadowRoot); domScan.shadowRoots++; }
      }
    }
    rootCache=trees;
    return trees;
  };
  const query = selector => roots().flatMap(root => [...root.querySelectorAll(selector)]);
  // The CLI disables HTML escaping for these JSON-only results. Go still
  // escapes the two JavaScript line separators, so include those rare bytes.
  const wireBytes = value => new TextEncoder().encode(JSON.stringify(value).replace(/[\u2028\u2029]/g, c => ({'\u2028':'\\u2028','\u2029':'\\u2029'}[c]))).length;
  const controlOutputBudget = 16384;
  // ---- Page physics shared by every site: which stack rendered it, which
  // requests fed it, and whether it is content, loading, a wall or an error.
  const hasGlobal = key => { try { return globalThis[key] !== undefined; } catch (_) { return false; } };
  const fingerprint = () => {
    const frameworks=[], layers=[];
    const some = (selector) => { try { return !!document.querySelector?.(selector); } catch(_) { return false; } };
    const pick = (selector) => { try { return document.querySelector?.(selector) || null; } catch(_) { return null; } };
    const everything = () => { try { return [...document.querySelectorAll('*')]; } catch(_) { return []; } };
    if (hasGlobal('__NEXT_DATA__') || hasGlobal('__next_f') || some('#__next')) frameworks.push('nextjs');
    if (hasGlobal('__NUXT__') || some('#__nuxt')) frameworks.push('nuxt');
    if (hasGlobal('__remixContext') || hasGlobal('__reactRouterContext')) frameworks.push('remix');
    if (some('[ng-version]')) frameworks.push('angular');
    if (hasGlobal('__VUE__') || some('[data-v-app]')) frameworks.push('vue');
    if (some('[class*="svelte-"]')) frameworks.push('svelte');
    if (hasGlobal('Turbo') || some('[data-turbo],[data-turbo-track],meta[name="turbo-cache-control"]')) frameworks.push('turbo');
    if (hasGlobal('htmx') || some('[hx-get],[hx-post],[hx-trigger]')) frameworks.push('htmx');
    if (hasGlobal('Shopify')) frameworks.push('shopify');
    if (some('link[href*="wp-content"],script[src*="wp-content"],script[src*="wp-includes"]')) frameworks.push('wordpress');
    if (some('shreddit-app')) frameworks.push('shreddit');
    if (some('[data-wf-page],[data-wf-site]')) frameworks.push('webflow');
    if (hasGlobal('wixBiSession') || some('meta[name="generator"][content*="Wix"]')) frameworks.push('wix');
    if (some('meta[name="generator"][content*="Squarespace"]')) frameworks.push('squarespace');
    if (hasGlobal('jQuery')) frameworks.push('jquery');
    if (!frameworks.includes('nextjs') && !frameworks.includes('remix')) {
      let react=false, seen=0;
      for (const el of everything()) { if (++seen>300) break; for (const key in el) { if (key.startsWith('__reactFiber$') || key.startsWith('__reactContainer$')) { react=true; break; } } if (react) break; }
      if (react || some('[data-reactroot]')) frameworks.push('react');
    }
    if (hasGlobal('__APOLLO_STATE__')) layers.push('apollo');
    if (hasGlobal('__RELAY_PAYLOADS__') || some('script[data-content-len][data-sjs]')) layers.push('relay');
    if (hasGlobal('__NEXT_DATA__')) layers.push('next_data');
    if (hasGlobal('__next_f')) layers.push('rsc_flight');
    if (hasGlobal('__INITIAL_STATE__') || hasGlobal('__PRELOADED_STATE__') || hasGlobal('__INITIAL_DATA__') || hasGlobal('__NUXT__') || hasGlobal('__remixContext')) layers.push('embedded_state');
    if (some('script[type="application/ld+json"]')) layers.push('json_ld');
    const embedded={};
    for (const key of ['__NEXT_DATA__','__APOLLO_STATE__','__INITIAL_STATE__','__PRELOADED_STATE__','__NUXT__','__remixContext']) {
      if (!hasGlobal(key)) continue;
      try { embedded[key]=JSON.stringify(globalThis[key]).length; } catch(_) { embedded[key]=null; }
    }
    const generator=pick('meta[name="generator"]')?.getAttribute?.('content') || '';
    return {frameworks, data_layers:layers, generator:clip(generator,80), embedded_state_bytes:embedded, custom_elements:[...new Set(everything().slice(0,2000).map(el=>String(el.tagName || '').toLowerCase()).filter(t=>t.includes('-')))].slice(0,12)};
  };
  // Resource Timing records every fetch/XHR the page issued without attaching a
  // network capture. Keys drop query strings and hash-like segments so an agent
  // sees endpoints, not one entry per id; bodies come from capture or fanout.
  const entries = type => { try { return performance.getEntriesByType(type) || []; } catch(_) { return []; } };
  const dataRequests = () => {
    const all=entries('resource');
    const nav=entries('navigation')[0];
    const dcl=nav ? nav.domContentLoadedEventEnd : 0;
    const groups=new Map();
    let counted=0;
    for (const entry of all) {
      if (entry.initiatorType!=='fetch' && entry.initiatorType!=='xmlhttprequest') continue;
      let key; try { const u=new URL(entry.name); key=u.origin+u.pathname.replace(/\/(?:\d+|[0-9a-f]{8,}|[A-Za-z0-9_-]{22,})(?=\/|$)/g,'/:id'); } catch(_) { continue; }
      counted++;
      const group=groups.get(key) || {endpoint:key, initiator:entry.initiatorType, count:0, bytes:0, first_ms:Math.round(entry.startTime), last_ms:0, after_dom_content_loaded:0};
      group.count++; group.bytes+=entry.encodedBodySize || entry.transferSize || 0;
      group.first_ms=Math.min(group.first_ms,Math.round(entry.startTime)); group.last_ms=Math.max(group.last_ms,Math.round(entry.responseEnd));
      if (entry.startTime>dcl) group.after_dom_content_loaded++;
      groups.set(key,group);
    }
    const endpoints=[...groups.values()].sort((a,b)=>b.bytes-a.bytes).slice(0,24);
    return {fetch_xhr_requests:counted, endpoints, resource_entries:all.length, buffer_may_be_full:all.length>=250, since:'navigation_start'};
  };
  // Cheap readiness and interruption signals. The decisive judgment for an
  // ambiguous page belongs to browser state, which can ask Jev about pixels.
  const stateSignals = async windowMs => {
    let mutations=0;
    const observer=typeof MutationObserver==='function' && document.documentElement ? new MutationObserver(list=>{ mutations+=list.length; }) : null;
    const before=entries('resource').length;
    if (windowMs>0) { observer?.observe(document.documentElement,{childList:true,subtree:true,characterData:true}); await new Promise(resolve=>setTimeout(resolve,windowMs)); }
    observer?.disconnect();
    const after=entries('resource').length;
    const main=query('main,[role="main"]').filter(visible)[0] || document.body;
    const textChars=(main?.innerText || '').trim().length;
    const controls=query('input,textarea,select,button,a[href],[role="button"],[role="tab"],[role="menuitem"]').filter(visible).length;
    const dialogs=query('[role="dialog"],[role="alertdialog"],dialog[open]').filter(visible).length;
    const password=query('input[type="password"]').filter(visible).length;
    const title=(document.title || '').toLowerCase();
    const body=((document.body?.innerText) || '').slice(0,4000).toLowerCase();
    const hints=[];
    if (password) hints.push('password_field');
    if (/\b(sign in|log in|login|create an account|sign up to continue)\b/.test(body) && textChars<1500) hints.push('login_text');
    if (dialogs && /\b(cookie|consent|accept all|privacy choices)\b/.test(body)) hints.push('consent_text');
    if (/just a moment|attention required|verify you are human|are you a robot|checking your browser|captcha|access denied/.test(title+' '+body.slice(0,800))) hints.push('challenge_text');
    if (query('iframe[src*="captcha"],iframe[src*="challenges.cloudflare"],iframe[src*="recaptcha"],iframe[src*="hcaptcha"],iframe[src*="arkoselabs"]').some(visible)) hints.push('captcha_frame');
    if (/\b(too many requests|rate limit|rate-limited)\b/.test(body)) hints.push('rate_limit_text');
    if (/^(404|403|500|error)\b|page not found|something went wrong|this page isn.?t working/.test(title) || /page not found|something went wrong|this page isn.?t working/.test(body.slice(0,600))) hints.push('error_text');
    const busy=document.readyState==='loading' || (after>before && mutations>0 && textChars<200);
    const kind=hints.includes('captcha_frame') || hints.includes('challenge_text') ? 'challenge'
      : hints.includes('password_field') || (hints.includes('login_text') && controls<=12) ? 'login'
      : dialogs && hints.includes('consent_text') ? 'consent'
      : hints.includes('rate_limit_text') ? 'rate_limited'
      : hints.includes('error_text') ? 'error'
      // Zero readable text is never a settled empty state: real empty states say
      // so ("No results"). A shell with navigation but no text is still loading.
      : busy || textChars===0 ? 'loading'
      : 'content';
    return {kind, hints, ready_state:document.readyState, text_chars:textChars, visible_controls:controls, dialogs, password_fields:password,
      new_resources_in_window:after-before, mutations_in_window:mutations, window_ms:windowMs, basis:'in_page_signals'};
  };
  const stateKey = Symbol.for('rep.fast.v1');
  const state = globalThis[stateKey] ||= {owners:new Map()};
  if (!state.owners.has(spec.owner)) {
    if (state.owners.size >= 32) state.owners.delete(state.owners.keys().next().value);
    state.owners.set(spec.owner,{refs:new Map(),turns:new Map()});
  }
  const owner = state.owners.get(spec.owner);
  owner.pages ||= new Map();
  const binding = el => ({el,parent:el.parentNode,url:location.href,name:name(el),role:role(el),type:el.getAttribute('type') || '',href:el.getAttribute('href') || '',at:Date.now()});
  // Applications rewrite the query and fragment (tabs, filters, scroll state);
  // the same origin and path is the same document for a reference.
  const sameDocument = (a,b) => { try { const x=new URL(a), y=new URL(b); return x.origin===y.origin && x.pathname===y.pathname; } catch(_) { return a===b; } };
  const unchanged = bound => sameDocument(bound.url,location.href) && bound.el.isConnected && bound.parent===bound.el.parentNode &&
    bound.name===name(bound.el) && bound.role===role(bound.el) && bound.type===(bound.el.getAttribute('type') || '') && bound.href===(bound.el.getAttribute('href') || '');
  const remember = el => {
    const ref = 'e_' + crypto.randomUUID().replaceAll('-','');
    owner.refs.set(ref,binding(el));
    while (owner.refs.size > 256) owner.refs.delete(owner.refs.keys().next().value);
    return ref;
  };
  const resolve = ref => {
    const bound = owner.refs.get(ref);
    if (!bound || Date.now()-bound.at > 120000) fail('reference_expired');
    const el = bound.el;
    if (!unchanged(bound)) fail('reference_stale');
    if (!visible(el)) fail('target_hidden');
    if (!enabled(el)) fail('target_disabled');
    return el;
  };
  const guard = el => {
    el.scrollIntoView({block:'center',inline:'nearest'});
    if (!visible(el) || !enabled(el)) fail('target_changed');
    const r=el.getBoundingClientRect(), x=r.left+r.width/2, y=r.top+r.height/2;
    if(x<0||y<0||x>=innerWidth||y>=innerHeight) fail('target_outside_viewport');
    let target=el, root=el.getRootNode();
    while(root) {
      const hit=root.elementFromPoint?.(x,y);
      if(!hit || !(hit===target || target.contains(hit))) fail('target_occluded');
      if(!root.host) break;
      target=root.host;root=target.getRootNode();
    }
    return {x,y};
  };
  const fill = (el, value, replace) => {
    if(el.readOnly || el.getAttribute('aria-readonly')==='true') fail('target_readonly');
    const current=el.isContentEditable ? el.innerText : el.value;
    if(typeof current!=='string') fail('unsupported_text_control');
    if(current!=='' && current!==value && !replace) fail('draft_preserved');
    if(current===value) return false;
    if(!el.isContentEditable && !el.matches('textarea,input:not([type]),input[type=text],input[type=search],input[type=email],input[type=url],input[type=tel],input[type=password],input[type=number]')) fail('unsupported_text_control');
    guard(el);el.focus();
    attempted=true;
    if(el.isContentEditable) {
      const selection=getSelection(),range=document.createRange();range.selectNodeContents(el);selection.removeAllRanges();selection.addRange(range);
      if(!document.execCommand('insertText',false,value)) fail('input_rejected');
    } else {
      const proto=el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(proto,'value').set.call(el,value);
      el.dispatchEvent(new InputEvent('input',{bubbles:true,composed:true,inputType:'insertText'}));
      el.dispatchEvent(new Event('change',{bubbles:true}));
    }
    if((el.isContentEditable ? el.innerText : el.value)!==value) fail('input_readback_mismatch');
    return true;
  };
  const messages = () => query('[data-message-role][data-message-id]').filter(visible);
  const messageText = el => text(el.querySelector('[data-message-text]') || el).replace(/^You:\s*/, '');
  const chatCandidates = () => query('textarea,[contenteditable="true"][role="textbox"]').filter(el=>visible(el) && enabled(el) && /^(message|send a message|ask|ask anything|message .+)$/i.test(name(el)));
  const chatComposer = () => {
    const candidates=chatCandidates();
    return candidates.length===1 ? candidates[0] : null;
  };
  const chatSupported = () => location.hostname==='muse.ai' || query('[data-rep-chat="v1"]').length===1;
  const busy = () => query('button').some(el=>visible(el) && /^stop( generating| response)?$/i.test(name(el)));
  const wait = async check => {
    const deadline=performance.now()+Math.max(0,spec.wait_ms || 0);
    let value;
    do {
      value=check();
      if(value.status!=='pending' || performance.now()>=deadline) return value;
      await new Promise(resolve=>setTimeout(resolve,Math.min(40,Math.max(1,deadline-performance.now()))));
    } while(true);
  };
  try {
    if(spec.url && !sameDocument(location.href,spec.url)) fail('page_changed');
    if(spec.operation==='page') {
      if(!spec.cursor) {
        // An application shell before hydration has no readable text and only a
        // few scaffold controls. Spend the readiness budget waiting for content
        // instead of reporting an empty page as read; the caller decides whether
        // an empty result after the budget is final or needs a longer read.
        const shell=()=>{
          const controls=query('input,textarea,select,button,a[href],[role="button"],[role="menuitem"],[role="tab"]').filter(visible);
          if(controls.length>3) return false;
          const main=query('main,[role="main"]').filter(visible)[0] || document.body;
          return !(main?.innerText || '').trim();
        };
        const ready=await wait(()=>{
          const interactive=query('input,textarea,select,button,a[href],[role="button"],[role="menuitem"],[role="tab"]').some(visible);
          return {status:document.readyState==='loading' || chatSupported() && !interactive || shell() ? 'pending' : 'ready'};
        });
        if(ready.status==='pending' && (document.readyState==='loading' || chatSupported())) return {status:'loading',url:location.href,title:document.title,
          coverage:{scope:'top_document_and_open_shadow_roots',controls:0,text_source:'waiting_for_visible_ui',iframes:query('iframe').length}};
      }
      let page, controlOffset=0, textOffset=0;
      if(spec.cursor) {
        const slice=owner.pages.get(spec.cursor);
        if(!slice || Date.now()-slice.page.at>120000) fail('page_cursor_expired');
        ({page,controlOffset,textOffset}=slice);
        if(!sameDocument(page.url,location.href)) fail('page_snapshot_stale');
      } else {
        const all=query('input:not([type=hidden]):not([type=file]),textarea,select,button,a[href],summary,[role="button"],[role="menuitem"],[role="menuitemcheckbox"],[role="menuitemradio"],[role="option"],[role="checkbox"],[role="radio"],[role="tab"],[role="combobox"],[contenteditable="true"]').filter(visible);
        const overlays=query('[role="dialog"],[role="alertdialog"],[role="menu"]').filter(visible), overlay=overlays.at(-1);
        const composer=chatComposer();
        const inViewport=el=>{const r=el.getBoundingClientRect();return r.bottom>0 && r.right>0 && r.top<innerHeight && r.left<innerWidth;};
        const priority=el=>(overlay?.contains(el) ? -100 : el===composer ? -50 : chatSupported() && /^(send( message)?|attach file|dictate a message|stop( generating| response)?)$/i.test(name(el)) ? -49 : inViewport(el) ? 0 : 10)+
          (el.matches('input,textarea,select,[contenteditable="true"]') ? 0 : el.tagName==='A' ? 2 : 1);
        all.sort((a,b)=>priority(a)-priority(b));
        const transcript=chatSupported() && composer ? messages() : [];
        const mains=query('main,[role="main"]').filter(visible);
        const isDocument=location.hostname==='docs.google.com' && /^\/document\/(?:u\/\d+\/)?d\/[^/]+\/(?:edit|view|preview)\/?$/.test(location.pathname);
        const documentContent=isDocument && !overlay ? await repReadContent({wait_ms:spec.read_ms || spec.wait_ms}) : null;
        const body=(documentContent ? documentContent.text : overlay ? overlay.innerText : chatSupported() && composer ? transcript.slice(-12).map(messageText).join('\n\n') : (mains.length===1 ? mains[0].innerText : document.body?.innerText)) || '';
        const contentMeta=documentContent ? {...documentContent} : null;
        if(contentMeta) {delete contentMeta.text; delete contentMeta.headings; delete contentMeta.links;}
        page={url:location.href,title:document.title,at:Date.now(),status:document.readyState==='loading'?'loading':'ready',body:new TextEncoder().encode(body),
          content:contentMeta,
          bindings:all.map(el=>({...binding(el),enabled:enabled(el),...(el.matches('input,textarea,select') && el.type!=='password' ? {has_value:!!el.value} : {})})),
          coverage:{scope:'top_document_and_open_shadow_roots',controls:all.length,offscreen_controls:all.filter(el=>!inViewport(el)).length,iframes:query('iframe').length,
            text_source:documentContent?documentContent.source:overlay?'dialog_or_menu':chatSupported() && composer?'rendered_chat_messages':mains.length===1?'main':'body',omitted_messages:Math.max(0,transcript.length-12),
            dom_nodes_scanned:domScan.nodes,dom_node_scan_limit:domScan.nodeLimit,dom_scan_limited:domScan.limited,
            open_shadow_roots_discovered:domScan.shadowRoots,root_tree_limit:domScan.treeLimit,shadow_roots_limited:domScan.shadowLimited},
          chat:{available:chatSupported() && !!composer,busy:chatSupported() ? busy() : undefined,composer_ref:composer ? remember(composer) : undefined}};
        if(documentContent?.status==='pending') page.status='loading';
        if(documentContent?.status==='unavailable' || documentContent?.status==='partial') page.status='partial';
        if((domScan.limited || domScan.shadowLimited) && page.status!=='loading') page.status='partial';
      }
      const selected=page.bindings.slice(controlOffset,controlOffset+spec.limit);
      if(selected.some(bound=>!unchanged(bound) || !visible(bound.el))) fail('page_snapshot_stale');
      const controls=[];let controlBytes=2, controlsLimitedByBytes=false;
      for(const bound of selected) {
        const projected={ref:'e_00000000000000000000000000000000',role:clip(bound.role,80),name:clip(bound.name,200),enabled:bound.enabled,
          ...('has_value' in bound ? {has_value:bound.has_value} : {}),
          ...(bound.el.tagName==='A' && /^https?:/.test(bound.el.href) ? {url:clip(bound.el.href,2048)} : {})};
        const bytes=wireBytes(projected), separator=controls.length ? 1 : 0;
        if(controlBytes+separator+bytes>controlOutputBudget) {controlsLimitedByBytes=true;break;}
        projected.ref=remember(bound.el);controls.push(projected);controlBytes+=separator+bytes;
      }
      let end=Math.min(page.body.length,textOffset+spec.max_text);
      while(end>textOffset && (page.body[end]&0xc0)===0x80) end--;
      const remainingControls=Math.max(0,page.bindings.length-controlOffset-controls.length), remainingText=page.body.length-end;
      let nextCursor;
      if((remainingControls || remainingText) && page.status!=='loading') {
        nextCursor='p_'+crypto.randomUUID().replaceAll('-','');
        owner.pages.set(nextCursor,{page,controlOffset:controlOffset+controls.length,textOffset:end});
        while(owner.pages.size>8) owner.pages.delete(owner.pages.keys().next().value);
      }
      let physics={};
      if(!spec.cursor) {
        // Measure quiet only when the page looks unsettled; a settled page costs nothing extra.
        const settledLooking=document.readyState==='complete' && page.body.length>0 && page.bindings.length>3;
        physics={stack:fingerprint(),data_requests:dataRequests(),page_state:await stateSignals(settledLooking ? 0 : 150)};
      }
      return {status:page.status,url:page.url,title:page.title,text:new TextDecoder().decode(page.body.slice(textOffset,end)),controls,
        observed_at:new Date(page.at).toISOString(),next_cursor:nextCursor,content:page.content || undefined,...physics,
        coverage:{...page.coverage,read:spec.cursor?'snapshot_continuation':'live',control_offset:controlOffset,text_offset:textOffset,
          omitted_controls:Math.max(0,page.bindings.length-controls.length),remaining_controls:remainingControls,
          control_output_budget_bytes:controlOutputBudget,returned_control_bytes:controlBytes,controls_limited_by_bytes:controlsLimitedByBytes,
          text_truncated:remainingText>0,remaining_text_bytes:remainingText},chat:page.chat};
    }
    if(spec.operation==='state') {
      const signals=await stateSignals(Math.max(100,Math.min(2000,spec.wait_ms || 300)));
      return {status:'observed',url:location.href,title:document.title,page_state:signals,stack:fingerprint(),data_requests:dataRequests(),observed_at:new Date().toISOString()};
    }
    if(spec.operation==='focus_guard') {
      const el=resolve(spec.ref);guard(el);
      if(el.getRootNode().activeElement!==el) fail('focus_changed');
      owner.refs.delete(spec.ref);
      return {status:'verified',url:location.href};
    }
    if(spec.operation==='do') {
      const el=resolve(spec.ref);
      if(spec.action==='fill') return {status:'verified',evidence:'input_readback',changed:fill(el,spec.text,spec.replace),url:location.href};
      if(spec.action==='click') {const point=guard(el);owner.refs.delete(spec.ref);return {status:'prepared_pointer',point,url:location.href};}
      if(spec.action==='press') {guard(el);el.focus();if(el.getRootNode().activeElement!==el) fail('focus_changed');return {status:'prepared',url:location.href};}
      fail('unsupported_action');
    }
    if(spec.operation==='chat_prepare') {
      if(!chatSupported()) fail('chat_contract_unavailable');
      const ready=await wait(()=>{
        const candidates=chatCandidates();
        if(candidates.length>1) fail('chat_composer_missing_or_ambiguous');
        return {status:candidates.length===1?'ready':'pending',composer:candidates[0]};
      });
      const el=ready.composer;if(!el) fail('chat_composer_missing_or_ambiguous');
      if(busy()) fail('chat_busy');
      const turn='t_'+crypto.randomUUID().replaceAll('-','');
      owner.turns.set(turn,{origin:location.origin,text:norm(spec.text),before:new Set(messages().map(el=>el.getAttribute('data-message-id'))),dispatched:false});
      while(owner.turns.size>64) owner.turns.delete(owner.turns.keys().next().value);
      return {status:'prepared',turn,composer_ref:remember(el),url:location.href};
    }
    if(spec.operation==='chat_fill') {
      const turn=owner.turns.get(spec.turn);if(!turn) fail('turn_missing');
      if(turn.dispatched) fail('turn_already_dispatched');
      const el=resolve(spec.ref);fill(el,spec.text,false);guard(el);el.focus();
      if(el.getRootNode().activeElement!==el) fail('focus_changed');
      return {status:'prepared',url:location.href};
    }
    if(spec.operation==='chat_send') {
      const turn=owner.turns.get(spec.turn);if(!turn) fail('turn_missing');
      if(turn.dispatched) fail('turn_already_dispatched');
      const ready=await wait(()=>{
        const candidates=query('button').filter(el=>visible(el)&&enabled(el)&&/^send( message)?$/i.test(name(el)));
        if(candidates.length>1) fail('chat_send_ambiguous');
        return {status:candidates.length===1?'ready':'pending',button:candidates[0]};
      });
      if(ready.status!=='ready') fail('chat_send_unavailable');
      const point=guard(ready.button);turn.dispatched=true;
      return {status:'prepared_pointer',point,url:location.href};
    }
    if(spec.operation==='chat_wait') {
      const turn=owner.turns.get(spec.turn);if(!turn) fail('turn_missing');
      if(location.origin!==turn.origin) fail('page_changed');
      return await wait(()=>{
        const generating=busy();
        if(generating) {turn.sawBusy=true;turn.idleSince=0;}
        else if(turn.sawBusy && !turn.idleSince) turn.idleSince=performance.now();
        const items=messages();
        const index=items.findIndex(el=>!turn.before.has(el.getAttribute('data-message-id')) && el.getAttribute('data-message-role')==='user' && messageText(el)===turn.text);
        if(index<0) return {status:'pending',submitted:!!turn.messageId,url:location.href,turn:spec.turn,
          ...(turn.messageId ? {message_id:turn.messageId,submission_evidence:'observed_user_message'} : {})};
        const user=items[index];
        turn.messageId=user.getAttribute('data-message-id');
        const submission={submitted:true,message_id:turn.messageId,submission_evidence:'observed_user_message'};
        const replies=[];
        for(const el of items.slice(index+1)) {
          if(el.getAttribute('data-message-role')==='user') break;
          if(el.getAttribute('data-message-role')==='assistant' && !turn.before.has(el.getAttribute('data-message-id')) && messageText(el)) replies.push(el);
        }
        if(!replies.length) return {status:'pending',...submission,url:location.href,turn:spec.turn};
        const response=replies.map(messageText).join('\n\n');
        const markedComplete=replies.every(el=>el.getAttribute('data-message-status')==='complete');
        const stopped=location.hostname==='muse.ai' && turn.sawBusy && !generating && turn.idleSince && performance.now()-turn.idleSince>=200;
        const complete=markedComplete || !!stopped;
        return {status:complete?'completed':generating || turn.sawBusy?'pending':'reply_received',...submission,url:location.href,turn:spec.turn,
          response:clip(response,spec.max_text),response_truncated:new TextEncoder().encode(response).length>spec.max_text,reply_complete:complete ? true : generating ? false : null,
          evidence:markedComplete?'message_completion_marker':stopped?'observed_generation_control_stopped':'rendered_assistant_message'};
      });
    }
    fail('unsupported_operation');
  } catch(error) {return {status:attempted?'unconfirmed':'failed',code:error.code||'renderer_error',url:location.href};}
}
