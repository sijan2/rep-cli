package browserfast

import (
	"os/exec"
	"testing"
)

// A populated sidebar must not make an empty application main ready. Exercise
// hydration and a bounded continuation without launching a browser.
func TestPageRendererWaitsForMainContentDespiteNavigation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to exercise the renderer")
	}
	script := `const assert=require('node:assert/strict');
const fast=(` + renderer + `);
global.location=new URL('https://example.test/thread');
global.innerWidth=1200;global.innerHeight=800;
global.getComputedStyle=()=>({display:'block',visibility:'visible'});
global.crypto=require('node:crypto').webcrypto;
function element(tag, label='') {
  return {tagName:tag,innerText:label,labels:[],isConnected:true,href:'https://example.test/nav',
    getAttribute(n){return n==='aria-label'?label:null;},closest(){return null;},
    matches(s){return s==='button,a,summary,[role]' && tag==='A';},
    getBoundingClientRect(){return {width:20,height:20,left:0,right:20,top:0,bottom:20};},
    getRootNode(){return document;},contains(n){return n===this;}};
}
const main=element('MAIN'), nav=Array.from({length:17},(_,i)=>element('A','Navigation '+i));
const spinner=element('DIV');let loading=true;
main.contains=n=>n===main || n===spinner;
global.document={readyState:'complete',title:'Thread',body:element('BODY','Navigation'),
  querySelectorAll(s){
    if(s==='*') return [main,...nav];
    if(s==='main,[role="main"]') return [main];
    if(s==='[aria-busy="true"],[role="progressbar"]') return loading?[spinner]:[];
    if(s.startsWith('input')) return s.includes('a[href]')?nav:[];
    return [];
  }};
(async()=>{
  setTimeout(()=>{main.innerText='See new posts';},20);
  setTimeout(()=>{main.innerText='The actual thread and its replies';loading=false;},120);
  const hydrated=await fast({owner:'hydration',operation:'page',wait_ms:400,max_text:4096,limit:40});
  assert.equal(hydrated.status,'ready');
  assert.match(hydrated.text,/actual thread/,'sidebar controls must not bypass hydration');
  assert.equal(hydrated.page_state.kind,'content');

  main.innerText='';
  const pending=await fast({owner:'hydration',operation:'page',wait_ms:10,max_text:4096,limit:1});
  assert.equal(pending.status,'loading');
  assert.equal(pending.page_state.kind,'loading');
  assert.equal(pending.next_cursor,undefined,'an unfinished shell must not paginate stale controls');
  assert.equal(pending.controls.length,1,'keep available controls for diagnosis');

  main.innerText='No results';
  const empty=await fast({owner:'hydration',operation:'page',wait_ms:400,max_text:4096,limit:40});
  assert.equal(empty.status,'ready');
  assert.equal(empty.text,'No results','an explicit empty state is content');

  main.innerText='A populated conversation. '.repeat(20);loading=true;
  const pagination=await fast({owner:'hydration',operation:'page',wait_ms:400,max_text:4096,limit:40});
  assert.equal(pagination.status,'ready','loading more records does not erase existing content');
  assert.equal(pagination.page_state.loading_indicators,1);
})().catch(e=>{console.error(e);process.exitCode=1;});`
	if out, err := exec.Command("node", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("page hydration contract: %v\n%s", err, out)
	}
}

// Exercise the shipped page renderer in one process so cursor state and exact
// references are covered together with the aggregate wire-byte budget.
func TestPageRendererBoundsControlsAndDegradesLargeDOMToPartial(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to exercise the renderer")
	}
	script := `const assert=require('node:assert/strict');
const fast=(` + renderer + `);
const read=async()=>({status:'ready',source:'fixture',scope:'fixture',text:'fixture',complete:true});
global.repReadContent=read;
global.location=new URL('https://example.test/');
global.innerWidth=1200;global.innerHeight=800;
global.getComputedStyle=()=>({display:'block',visibility:'visible'});
global.crypto=require('node:crypto').webcrypto;
let controls=[],allNodes=[];
const body={innerText:'fixture body'};
global.document={readyState:'complete',title:'Fixture',body,
  querySelectorAll(selector){
    if(selector==='*') return allNodes;
    if(selector.startsWith('input:not(')) return controls;
    return [];
  }};
function anchor(index) {
  const href='https://example.test/'+index+'?q='+'&'.repeat(4000);
  const el={tagName:'A',type:'',href,innerText:'Link '+index+' '+'&'.repeat(200),textContent:'',labels:[],parentNode:body,isConnected:true,shadowRoot:null,
    getAttribute(name){return name==='href'?href:name==='aria-label'?this.innerText:null;},
    matches(selector){return selector==='button,a,summary,[role]';},closest(){return null;},checkVisibility(){return true;},
    getBoundingClientRect(){return {width:20,height:20,left:0,right:20,top:0,bottom:20};},getRootNode(){return document;},contains(node){return node===this;}};
  return el;
}
const wireJSON=value=>JSON.stringify(value).replace(/[\u2028\u2029]/g,c=>({'\u2028':'\\u2028','\u2029':'\\u2029'}[c]));
(async()=>{
  controls=Array.from({length:40},(_,i)=>anchor(i));allNodes=controls;
  let cursor='',seen=new Set(),offset=0,pages=0,first;
  do {
    const result=await fast({owner:'budget-test',operation:'page',max_text:4096,limit:40,...(cursor?{cursor}:{})});
    first ||= result;pages++;
    assert.equal(result.status,'ready');
    assert.equal(result.coverage.control_offset,offset);
    assert.ok(result.controls.length>0,'each continuation must make progress');
    assert.ok(result.coverage.returned_control_bytes<=result.coverage.control_output_budget_bytes);
    assert.equal(result.coverage.returned_control_bytes,Buffer.byteLength(wireJSON(result.controls)));
    for(const control of result.controls) {assert.match(control.ref,/^e_[0-9a-f]{32}$/);assert.ok(!seen.has(control.ref));seen.add(control.ref);}
    offset+=result.controls.length;cursor=result.next_cursor||'';
    assert.ok(pages<50,'pagination must terminate');
  } while(cursor);
  assert.equal(offset,40);assert.equal(seen.size,40);assert.equal(first.coverage.controls_limited_by_bytes,true);

  const draft=Object.assign(anchor(41),{tagName:'INPUT',type:'text',value:'unsubmitted draft'});
  controls=[anchor(40),draft];allNodes=controls;
  const sliced=await fast({owner:'protected-input',operation:'page',max_text:4096,limit:1});
  assert.equal(sliced.controls.length,1);
  assert.equal(sliced.page_state.has_input,true,'a draft outside the returned control slice must protect the tab');
  draft.value='';
  const cleared=await fast({owner:'protected-input',operation:'page',max_text:4096,limit:1});
  assert.equal(cleared.page_state.has_input,false,'cleared drafts release protection');

  controls=[];allNodes=Array(50001).fill({shadowRoot:null});body.innerText='bounded result';
  const large=await fast({owner:'large-dom-test',operation:'page',max_text:4096,limit:40});
  assert.equal(large.status,'partial');assert.equal(large.next_cursor,undefined);
  assert.equal(large.coverage.dom_scan_limited,true);assert.equal(large.coverage.dom_nodes_scanned,50000);
  assert.equal(large.coverage.dom_node_scan_limit,50000);
})().catch(e=>{console.error(e);process.exitCode=1;});`
	cmd := exec.Command("node", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("page renderer contract: %v\n%s", err, out)
	}
}
