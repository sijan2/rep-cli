async function repInteraction(spec) {
  'use strict';
  const started = performance.now();
  let attempted = false, changed = false;
  const norm = value => String(value ?? '').replace(/\s+/g, ' ').trim();
  const fail = code => { throw Object.assign(new Error(code), { code }); };
  const bound = this instanceof Element ? this : null;
  const result = (status, extra = {}) => ({status, attempted, changed, duration_ms: Math.round(performance.now() - started), ...extra});
  const roots = root => {
    const found = [root]; let nodes = 0;
    for (let i = 0; i < found.length; i++) {
      for (const el of found[i].querySelectorAll('*')) {
        if (++nodes > 50000) fail('dom_budget_exceeded');
        if (el.shadowRoot) { if (found.length >= 128) fail('shadow_root_budget_exceeded'); found.push(el.shadowRoot); }
      }
    }
    return found;
  };
  const query = (root, selector) => {
    try { return roots(root).flatMap(tree => [...tree.querySelectorAll(selector)]); }
    catch (error) { if (error.code) throw error; fail('invalid_selector'); }
  };
  const text = el => norm(el.innerText ?? el.textContent);
  const name = el => {
    const labelled = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean)
      .map(id => el.getRootNode().getElementById?.(id)?.textContent || '').join(' ');
    return norm(labelled || el.getAttribute('aria-label') || [...(el.labels || [])].map(text).join(' ')
      || (el.matches('input[type=button],input[type=submit],input[type=reset]') ? el.value : '')
      || (el.matches('button,a,[role],summary,option') ? text(el) : '') || el.getAttribute('alt') || el.getAttribute('title'));
  };
  const role = el => el.getAttribute('role') || ({BUTTON:'button',TEXTAREA:'textbox',SELECT:el.multiple?'listbox':'combobox',A:el.hasAttribute('href')?'link':'',SUMMARY:'button'}[el.tagName])
    || (el.tagName === 'INPUT' ? ({checkbox:'checkbox',radio:'radio',number:'spinbutton',range:'slider',button:'button',submit:'button',search:'searchbox'}[el.type] || 'textbox') : el.isContentEditable ? 'textbox' : '');
  const locate = (target, allowMissing = false, useBound = true) => {
    let scope = document;
    if (target.within) {
      let scopes = query(document, target.within.selector);
      if (target.within.text) scopes = scopes.filter(el => {
        const labels = target.within.text_selector ? query(el,target.within.text_selector) : [el];
        return labels.length === 1 && text(labels[0]) === norm(target.within.text);
      });
      if (scopes.length > 1) fail('ambiguous_scope');
      if (!scopes.length) { if (allowMissing) return null; fail('scope_missing'); }
      scope = scopes[0];
    }
    let matches;
    if (target.goal) {
      if (!useBound || !bound?.isConnected) fail('stale_semantic_target');
      matches = [bound];
    } else matches = query(scope, target.selector || 'input,textarea,select,button,a,summary,option,[role],[contenteditable],[aria-label],[aria-labelledby]');
    if (target.name) matches = matches.filter(el => name(el) === norm(target.name));
    if (target.role) matches = matches.filter(el => role(el) === target.role);
    if (matches.length > 1) fail('ambiguous_target');
    if (!matches.length && !allowMissing) fail('target_missing_or_changed');
    return matches[0] || null;
  };
  const visible = el => {
    if (!el?.isConnected || el.closest('[hidden],[inert],[aria-hidden="true"]')) return false;
    const style = getComputedStyle(el), rect = el.getBoundingClientRect();
    return style.visibility === 'visible' && style.display !== 'none' && rect.width > 0 && rect.height > 0;
  };
  const enabled = el => !el.matches(':disabled') && !el.closest('[aria-disabled="true"],[inert]');
  const editor = el => el.classList.contains('ace_editor') ? (el.env?.editor || null) : null;
  const read = el => editor(el) ? editor(el).getValue() : el.isContentEditable ? el.innerText : el.value;
  const test = condition => {
    if (condition.url && location.href !== condition.url) return false;
    if (!condition.target) return true;
    const el = locate(condition.target, true, false);
    if (condition.absent) return !el;
    if (!el) return false;
    if (condition.visible !== undefined && visible(el) !== condition.visible) return false;
    if (condition.enabled !== undefined && !!enabled(el) !== condition.enabled) return false;
    if (condition.text !== undefined && text(el) !== norm(condition.text)) return false;
    if (condition.value !== undefined && read(el) !== condition.value) return false;
    if (condition.checked !== undefined && el.checked !== condition.checked) return false;
    return true;
  };
  const all = conditions => !!conditions?.length && conditions.every(test);
  const usable = (el, edit = false) => {
    if (!visible(el)) fail('target_hidden_or_detached');
    if (!enabled(el)) fail('target_disabled');
    if (edit && (el.readOnly || el.getAttribute('aria-readonly') === 'true' || editor(el)?.getReadOnly())) fail('target_readonly');
  };
  const emitInput = el => { el.dispatchEvent(new InputEvent('input',{bubbles:true,composed:true,inputType:'insertText'})); el.dispatchEvent(new Event('change',{bubbles:true})); };
  const wait = conditions => new Promise(resolve => {
    let timer, interval, observer, finished = false;
    const cleanup = () => { clearTimeout(timer); clearInterval(interval); observer?.disconnect(); };
    const finish = value => { if (finished) return; finished = true; cleanup(); resolve(value); };
    const check = () => { try { if (all(conditions)) finish(result('verified',{evidence:'postcondition'})); } catch(error) { finish(result('failed',{code:error.code || 'condition_error'})); } };
    observer = new MutationObserver(check);
    for (const root of roots(document)) observer.observe(root,{subtree:true,childList:true,attributes:true,characterData:true});
    // Property changes and newly attached shadow roots need a bounded fallback.
    interval = setInterval(check,100);
    timer = setTimeout(()=>finish(result('failed',{code:'postcondition_timeout'})),spec.timeout_ms || 10000);
    check();
  });
  try {
    if (window !== window.top) fail('frame_target_unsupported');
    if (spec.operation === 'wait') return await wait(spec.step.after);
    if (location.href !== spec.url) fail('page_changed');
    const step = spec.step;
    if (all(step.skip_if)) return result('skipped',{evidence:'skip_condition'});
    if (spec.operation === 'check_skip') return result('planned');
    if (step.action === 'wait') return spec.operation === 'preview' ? result('planned') : await wait(step.after);
    const el = locate(step.target);
    usable(el,['fill','replace','choose','check'].includes(step.action));
    if (spec.operation === 'focus_guard') {
      if (el.getRootNode().activeElement !== el) fail('focus_changed');
      return result('verified',{evidence:'focus'});
    }
    let value, selected, current;
    if (step.action === 'fill' || step.action === 'replace') {
      if (!editor(el) && !el.isContentEditable && !el.matches('textarea,input:not([type]),input[type=text],input[type=search],input[type=email],input[type=url],input[type=tel],input[type=password],input[type=number]')) fail('unsupported_text_control');
      current = read(el);
      if (typeof current !== 'string') fail('unsupported_text_control');
      value = step.value;
      if (step.action === 'replace') {
        const index = current.indexOf(step.old);
        if (index < 0 || current.indexOf(step.old,index+1)>=0) fail('replacement_missing_or_ambiguous');
        value = current.slice(0,index)+step.value+current.slice(index+step.old.length);
      } else if (current !== '' && current !== value && !step.replace) fail('existing_value_preserved');
      changed = current !== value;
    } else if (step.action === 'choose') {
      if (!(el instanceof HTMLSelectElement)) fail('unsupported_select_control');
      if (!el.multiple && step.values.length!==1) fail('single_select_requires_one_option');
      const labels=[...el.options].map(text);
      if (new Set(labels).size!==labels.length) fail('ambiguous_option');
      selected=step.values.map(label=>{
        const option=[...el.options].find(option=>text(option)===norm(label));
        if (!option || option.disabled || option.parentElement?.disabled) fail('option_missing_or_disabled');
        return option;
      });
      changed=[...el.options].some(option=>option.selected!==selected.includes(option));
    } else if (step.action === 'check') {
      if (!el.matches('input[type=checkbox],input[type=radio]')) fail('unsupported_check_control');
      if (el.type==='radio'&&!step.checked) fail('radio_requires_true');
      changed=el.checked!==step.checked;
    } else if (!['click','press'].includes(step.action)) fail('unsupported_action');
    if (step.action==='click' || step.action==='press') changed=true;
    if (changed && all(step.after)) fail('postcondition_already_satisfied');
    if (spec.operation==='preview') return result('ready',{control:editor(el)?'ace':el.tagName.toLowerCase(),would_change:changed,changed:false});
    if (spec.operation!=='perform') fail('invalid_operation');
    el.scrollIntoView({block:'center',inline:'nearest'});
    if (step.action==='press') { el.focus();if(el.getRootNode().activeElement!==el)fail('focus_failed');return result('prepared'); }
    if (changed) {
      attempted=true; // Set before dispatch: an exception does not imply the side effect was absent.
      if (step.action==='click') el.click();
      else if (step.action==='check') el.click();
      else if (step.action==='choose') { for(const option of el.options)option.selected=selected.includes(option);emitInput(el); }
      else if(editor(el)) { editor(el).setValue(value,-1); }
      else if(el.isContentEditable) {
        el.focus();const selection=getSelection(),range=document.createRange();range.selectNodeContents(el);selection.removeAllRanges();selection.addRange(range);
        if(!document.execCommand('insertText',false,value))fail('editable_input_rejected');
      } else {
        el.focus();const proto=el instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
        Object.getOwnPropertyDescriptor(proto,'value').set.call(el,value);emitInput(el);
      }
    }
    if(step.action!=='click') {
      const fresh=locate(step.target);usable(fresh,true);
      if ((step.action==='fill'||step.action==='replace')&&read(fresh)!==value)fail('input_readback_mismatch');
      if(step.action==='check'&&fresh.checked!==step.checked)fail('check_readback_mismatch');
      if(step.action==='choose') {
        const expected=step.values.map(norm).sort(),actual=[...fresh.selectedOptions].map(text).sort();
        if(JSON.stringify(expected)!==JSON.stringify(actual))fail('select_readback_mismatch');
      }
    }
    return result(step.after?.length?'performed':'verified',{evidence:step.after?.length?'dispatch':'input_readback'});
  } catch(error) { return result(attempted?'unconfirmed':'failed',{code:error.code||'runtime_error'}); }
}
