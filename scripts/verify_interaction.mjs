#!/usr/bin/env node
// End-to-end regression fixture in a newly owned Arc tab. No account pages are modified.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {mkdtemp,writeFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
const execute=promisify(execFile),binary=process.env.REP_TEST_BINARY||'/tmp/rep-jev-interaction';
const browser=process.env.REP_TEST_BROWSER||'arc',task=process.env.REP_TEST_TASK||'jev-actions-20260920';
const directory=await mkdtemp(join(tmpdir(),'rep-interaction-'));
const checks=[];let tab;
const html=`<!doctype html><html><head><title>Rep interaction regression fixture</title></head><body>
<style>body{font:16px sans-serif;padding:16px}section{padding:12px}input,textarea,select{margin:8px}#rich,#ace{border:1px solid;min-height:40px}</style>
<section><h2>First question</h2><label>Answer<input id="first"></label><button class="check">Check</button><p class="status">Waiting</p></section>
<section><h2>Second question</h2><label>Answer<input id="second"></label><button class="check">Check</button><p class="status">Waiting</p></section>
<label>Semantic response<input id="semantic"></label>
<label>Multiline<textarea id="multi"></textarea></label><label>Count<input id="count" type="number"></label>
<label><input id="radio-a" name="radio" type="radio">Yes</label><label><input id="radio-b" name="radio" type="radio">No</label>
<label><input id="box" type="checkbox">Remember</label>
<label>Color<select id="color"><option>Red</option><option>Green</option></select></label>
<label>Extras<select id="extras" multiple><option>A</option><option>B</option><option>C</option></select></label>
<div id="rich" role="textbox" contenteditable="true" aria-label="Rich text"></div>
<div id="ace" class="ace_editor" aria-label="Code editor">Editable code</div>
<div id="shadow"></div>
<input id="disabled" disabled value="untouched"><input id="protected" value="keep me"><input id="readonly" readonly value="read only">
<input id="controlled" aria-label="Controlled input"><button id="timeout-button">Never accepted</button>
<button id="guarded">Already accepted</button><p id="old-success">Saved</p>
<button id="save">Save form</button><p id="saved">Not saved</p>
<div id="move" role="option" tabindex="0">Move me</div><p id="moved">Waiting</p>
<button id="route">Next section</button><div id="section"></div>
<a id="navigate" href="/next">Open next page</a>
<script>
window.counts={checks:0,saves:0,guarded:0,keys:0,timeouts:0};window.observed={};window.editorValue='public class Demo { /* replace */ }';
for(const el of document.querySelectorAll('input,textarea,select,[contenteditable]')){
 const update=()=>window.observed[el.id]=el.type==='checkbox'||el.type==='radio'?el.checked:el.isContentEditable?el.innerText:el.value;
 el.addEventListener('input',update);el.addEventListener('change',update);
}
for(const section of document.querySelectorAll('section'))section.querySelector('button').onclick=()=>{window.counts.checks++;setTimeout(()=>section.querySelector('.status').textContent='Correct',75)};
document.querySelector('#ace').env={editor:{getValue:()=>window.editorValue,getReadOnly:()=>false,setValue:value=>{window.editorValue=value}}};
const shadow=document.querySelector('#shadow').attachShadow({mode:'open'});shadow.innerHTML='<label>Shadow answer<input id="shadow-input"></label>';
document.querySelector('#controlled').addEventListener('input',e=>{e.target.value=''});
document.querySelector('#timeout-button').onclick=()=>window.counts.timeouts++;
document.querySelector('#guarded').onclick=()=>window.counts.guarded++;
document.querySelector('#save').onclick=()=>{window.counts.saves++;setTimeout(()=>document.querySelector('#saved').textContent='Saved',140)};
document.querySelector('#move').onkeydown=e=>{window.counts.keys++;if(e.key==='ArrowRight')document.querySelector('#moved').textContent='Moved'};
document.querySelector('#route').onclick=()=>{setTimeout(()=>{history.pushState({},'', '/section2');document.querySelector('#section').innerHTML='<h2>Section two ready</h2><label>Next answer<input id="next-answer"></label>'},100)};
</script></body></html>`;
const server=createServer((req,res)=>{res.setHeader('Content-Type','text/html');res.end(req.url==='/next'?'<title>Next page</title><h1 id="next-page">Next page ready</h1>':html)});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const base=`http://127.0.0.1:${server.address().port}`,url=base+'/';
async function rep(args,allowFailure=false){
 try{const {stdout}=await execute(binary,['--workspace','rep-runtime','--task',task,...args,...(['browser','jev'].includes(args[0])?['--browser',browser]:[]),'--raw-json'],{timeout:120000,maxBuffer:2*1024*1024});return JSON.parse(stdout)}
 catch(error){if(allowFailure&&error.stdout)return JSON.parse(error.stdout);throw error}
}
async function evaluate(expression){const r=await rep(['browser','eval',expression,'--tab',String(tab)]);if(r.result?.exceptionDetails)throw Error('Fixture evaluation failed');return r.result.result.value}
async function run(name,steps,{apply=true,expected='verified',page=url,legacy=false}={}){
 const path=join(directory,name+'.json');await writeFile(path,JSON.stringify({version:1,url:page,steps}),{mode:0o600});
 console.error('Testing: '+name);
 const report=await rep([...(legacy?['jev','act']:['browser','interact']),'--tab',String(tab),...(legacy?['--plan',path]:[path]),...(apply?['--apply']:[])],expected==='failed'||expected==='unconfirmed');
 assert.equal(report.status,expected,JSON.stringify(report));checks.push({name,status:report.status,steps:report.steps.length,duration_ms:report.duration_ms,bridge_calls:report.bridge_calls});return report;
}
const target=selector=>({selector});const text=(selector,text)=>({target:target(selector),text});
try{
 const created=await rep(['browser','create',url]);tab=created.tab_id;assert.ok(Number.isSafeInteger(tab));
 // Wait for the owned fixture to load using the same runtime being tested.
 await run('ready',[{id:'ready',action:'wait',after:[{url},{target:target('#first'),visible:true}]}]);
 await run('preview',[{id:'first',action:'fill',target:target('#first'),value:'must not fill'},{id:'deferred',action:'fill',target:target('#future'),value:'later'}],{apply:false,expected:'preview',legacy:true});
 assert.equal(await evaluate('document.querySelector("#first").value'),'');
 const scope={selector:'section',text_selector:'h2',text:'Second question'};
 await run('scoped-fill-and-check',[
  {id:'fill',action:'fill',target:{name:'Answer',role:'textbox',within:scope},value:'second answer'},
  {id:'check',action:'click',target:{name:'Check',role:'button',within:scope},after:[{target:{selector:'.status',within:scope},text:'Correct'}]},
 ]);
 assert.deepEqual(await evaluate('({first:document.querySelector("#first").value,second:document.querySelector("#second").value,checks:window.counts.checks})'),{first:'',second:'second answer',checks:1});
 await run('input-adapters',[
  {id:'multiline',action:'fill',target:target('#multi'),value:'first line\nsecond line'},
  {id:'number',action:'fill',target:target('#count'),value:'42'},
  {id:'radio',action:'check',target:{name:'No',role:'radio'},checked:true},
  {id:'checkbox',action:'check',target:{name:'Remember',role:'checkbox'},checked:true},
  {id:'select',action:'choose',target:target('#color'),values:['Green']},
  {id:'multi-select',action:'choose',target:target('#extras'),values:['A','C']},
  {id:'rich',action:'fill',target:target('#rich'),value:'Hello\nworld'},
  {id:'code',action:'replace',target:target('#ace'),old:'/* replace */',value:'private int count;'},
  {id:'shadow',action:'fill',target:{name:'Shadow answer',role:'textbox'},value:'inside shadow'},
 ]);
 const state=await evaluate('({observed:window.observed,code:window.editorValue,selected:[...document.querySelector("#extras").selectedOptions].map(o=>o.textContent),shadow:document.querySelector("#shadow").shadowRoot.querySelector("input").value})');
 assert.equal(state.observed.multi,'first line\nsecond line');assert.equal(state.observed.count,'42');assert.equal(state.observed['radio-b'],true);assert.equal(state.observed.box,true);assert.equal(state.observed.rich,'Hello\nworld');assert.equal(state.code,'public class Demo { private int count; }');assert.deepEqual(state.selected,['A','C']);assert.equal(state.shadow,'inside shadow');
 for(const [name,step,code] of [
  ['ambiguous',{action:'fill',target:{name:'Answer'},value:'bad'},'ambiguous_target'],
  ['disabled',{action:'fill',target:target('#disabled'),value:'bad'},'target_disabled'],
  ['readonly',{action:'fill',target:target('#readonly'),value:'bad'},'target_readonly'],
  ['preserve-existing',{action:'fill',target:target('#protected'),value:'bad'},'existing_value_preserved'],
  ['missing-option',{action:'choose',target:target('#color'),values:['Missing']},'option_missing_or_disabled'],
  ['stale-success',{action:'click',target:target('#guarded'),after:[text('#old-success','Saved')]},'postcondition_already_satisfied'],
 ]){const report=await run(name,[{id:name,...step}],{expected:'failed'});assert.equal(report.steps[0].code,code);assert.equal(report.steps[0].attempted,false)}
 assert.equal(await evaluate('window.counts.guarded'),0);
 const rejected=await run('framework-rejects-input',[{id:'rejected',action:'fill',target:target('#controlled'),value:'rejected value'},{id:'must-not-run',action:'click',target:target('#guarded'),after:[text('#old-success','Changed')]}],{expected:'unconfirmed'});
 assert.equal(rejected.steps[0].code,'input_readback_mismatch');assert.equal(rejected.steps.length,1);
 const timeout=await run('submission-timeout-no-replay',[{id:'unaccepted',action:'click',target:target('#timeout-button'),timeout_ms:200,after:[text('#never','Ready')]}],{expected:'unconfirmed'});
 assert.equal(timeout.steps[0].code,'postcondition_timeout');assert.equal(await evaluate('window.counts.timeouts'),1);
 await run('delayed-save-and-resume',[
  {id:'save',action:'click',target:target('#save'),after:[text('#saved','Saved')]},
  {id:'resume',action:'click',target:target('#save'),skip_if:[text('#saved','Saved')],after:[text('#saved','Saved')]},
 ]);
 assert.equal(await evaluate('window.counts.saves'),1);
 await run('trusted-keyboard',[{id:'move',action:'press',target:target('#move'),keys:['Space','ArrowRight','Space'],after:[text('#moved','Moved')]}]);
 assert.equal(await evaluate('window.counts.keys'),3);
 // One real semantic selection. Value is excluded from the Jev selector input by construction.
 const selected=await rep(['browser','select','Find the text input named Semantic response','--tab',String(tab)]);
 assert.equal(selected.status,'selected');assert.equal(selected.selected.name,'Semantic response');
 checks.push({name:'canonical-select',status:'selected',steps:1});
 await run('semantic-target',[{id:'semantic',action:'fill',target:{goal:'Find the text input named Semantic response',name:'Semantic response',role:'textbox'},value:'semantic fixture value'}]);
 assert.equal(await evaluate('document.querySelector("#semantic").value'),'semantic fixture value');
 await run('spa-and-full-navigation',[
  {id:'section',action:'click',target:target('#route'),after:[{url:base+'/section2'},text('#section h2','Section two ready')]},
  {id:'next-input',action:'fill',target:target('#next-answer'),value:'loaded dynamically'},
  {id:'navigate',action:'click',target:target('#navigate'),after:[{url:base+'/next'},text('#next-page','Next page ready')]},
 ]);
 assert.equal(await evaluate('location.href'),base+'/next');
 const wrongPage=await run('wrong-page-guard',[{id:'wrong-page',action:'fill',target:target('#next-page'),value:'bad'}],{expected:'failed'});
 assert.equal(wrongPage.steps[0].code,'page_changed');
 const report={verified:true,time:new Date().toISOString(),checks};
 await writeFile(process.env.REP_TEST_REPORT||join(tmpdir(),'rep-interaction-verification.json'),JSON.stringify(report,null,2)+'\n',{mode:0o600});
 console.log(JSON.stringify(report,null,2));
}catch(error){console.error('Primary failure:',error.message);throw error;}finally{
 if(tab!==undefined)try{await rep(['browser','close',String(tab)])}catch(error){console.error('Cleanup failed for owned tab '+tab+': '+error.message)}
 server.closeAllConnections();await new Promise(resolve=>server.close(resolve));await rm(directory,{recursive:true,force:true});
}
