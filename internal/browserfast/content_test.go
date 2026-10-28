package browserfast

import (
	"os/exec"
	"testing"
)

// Run the shipped renderer, not a second implementation. The browser fixture
// separately checks DOM/CSS extraction; these cases isolate the export contract.
func TestDocumentExportContentContract(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to exercise the renderer")
	}
	script := `const assert=require('node:assert/strict');
const read=(` + contentRenderer + `);
global.location=new URL('https://docs.google.com/document/d/test-document/edit?tab=t.selected');
let calls=0;
function response(body,type='text/plain; charset=utf-8',status=200,url='https://doc-00-docstext.googleusercontent.com/export/signed-secret') {
  const r=new Response(body,{status,headers:{'content-type':type}});Object.defineProperty(r,'url',{value:url});return r;
}
global.fetch=async (url,options)=>{calls++;assert.equal(new URL(url).searchParams.get('tab'),'t.selected');assert.equal(options.credentials,'same-origin');return response('Actual document body 界');};
(async()=>{
 let r=await read({wait_ms:1000});assert.equal(r.text,'Actual document body 界');assert.equal(r.complete,true);assert.equal(r.scope,'selected_document_tab');assert.ok(!JSON.stringify(r).includes('signed-secret'));
 global.fetch=async()=>response('');r=await read({wait_ms:1000});assert.equal(r.status,'empty');assert.equal(r.bytes,0);assert.equal(r.complete,true);
 global.fetch=async()=>response('<html>Sign in</html>','text/html');r=await read({wait_ms:1000});assert.equal(r.status,'unavailable');assert.equal(r.complete,false);assert.equal(r.text,'');
 global.fetch=async()=>response('permission denied','text/plain',403);r=await read({wait_ms:1000});assert.equal(r.reason,'authentication_or_access_required');assert.equal(r.complete,false);
 global.fetch=async()=>response('forged','text/plain',200,'https://example.test/export');r=await read({wait_ms:1000});assert.equal(r.status,'unavailable');
 global.fetch=async()=>response('界'.repeat(200000));r=await read({wait_ms:1000});assert.equal(r.status,'partial');assert.equal(r.complete,false);assert.ok(Buffer.byteLength(r.text)<=524288);assert.ok(!r.text.includes('\uFFFD'));
 global.fetch=async(_url,{signal})=>new Promise((resolve,reject)=>signal.addEventListener('abort',()=>reject(new Error('timeout'))));r=await read({wait_ms:5});assert.equal(r.status,'pending');assert.equal(r.complete,false);
 global.fetch=()=>{throw new Error('must not dispatch')};r=await read({wait_ms:0});assert.equal(r.reason,'read_budget_exhausted');
})().catch(e=>{console.error(e);process.exitCode=1;});`
	// Compile the other two functions without interpolating their template strings.
	syntax := exec.Command("node", "-e", "new Function('return ('+process.argv[1]+')');new Function('return ('+process.argv[2]+')')", renderer, referenceRenderer)
	if out, err := syntax.CombinedOutput(); err != nil {
		t.Fatalf("renderer syntax: %v\n%s", err, out)
	}
	cmd := exec.Command("node", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("document export contract: %v\n%s", err, out)
	}
}
