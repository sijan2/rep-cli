package browserflow

import (
	"os/exec"
	"testing"
)

func TestRendererTrustsValidatedAXBindingAndGuardsOcclusion(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	// The browser AX name for <button><img alt="Checkout"></button> is Checkout,
	// while the intentionally smaller exact-DOM name helper sees empty text.
	// A previously validated AX binding must survive that representational gap.
	script := `const assert=require('node:assert/strict');
global.Element=class Element{};
global.window=globalThis;window.top=window;
global.location={href:'https://example.test/'};
let clicks=0,covered=false;
const button=new Element();const blocker=new Element();
Object.assign(button,{isConnected:true,tagName:'BUTTON',innerText:'',textContent:'',labels:[],
classList:{contains:()=>false},getAttribute:()=>null,closest:()=>null,
matches:selector=>selector.split(',').includes('button'),
getBoundingClientRect:()=>({left:10,top:10,width:20,height:20}),
scrollIntoView:()=>{},click:()=>{clicks++},contains:other=>other===button,
getRootNode:()=>document});
global.document={querySelectorAll:()=>[button],elementFromPoint:()=>covered?blocker:button};
global.getComputedStyle=()=>({visibility:'visible',display:'block'});
global.innerWidth=100;global.innerHeight=100;
const run=(` + renderer + `);
(async()=>{
 const semantic=await run.call(button,{url:location.href,operation:'preview',step:{action:'click',target:{goal:'Checkout',name:'Checkout',role:'button'}}});
 assert.equal(semantic.status,'ready');assert.equal(clicks,0);
 const exact=await run({url:location.href,operation:'preview',step:{action:'click',target:{name:'Checkout',role:'button'}}});
 assert.equal(exact.status,'failed');assert.equal(exact.code,'target_missing_or_changed');
 covered=true;
 const blocked=await run.call(button,{url:location.href,operation:'perform',step:{action:'click',target:{goal:'Checkout'}}});
 assert.equal(blocked.status,'failed');assert.equal(blocked.code,'target_occluded');assert.equal(blocked.changed,false);assert.equal(blocked.attempted,false);assert.equal(clicks,0);
 covered=false;window.top={};location.href='https://child.test/';
 const child=await run.call(button,{url:'https://parent.test/',frame_id:'child',frame_url:location.href,operation:'prepare_geometry',step:{action:'click',target:{goal:'Checkout'}}});
 assert.equal(child.status,'ready');assert.deepEqual(child.point,{x:20,y:20});assert.equal(clicks,0);
 const moved=await run.call(button,{url:'https://parent.test/',frame_id:'child',frame_url:location.href,expected_point:{x:25,y:20},operation:'perform',step:{action:'click',target:{goal:'Checkout'}}});
 assert.equal(moved.status,'failed');assert.equal(moved.code,'target_geometry_changed');assert.equal(clicks,0);
})().catch(error=>{console.error(error);process.exitCode=1;});`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("renderer regression: %v\n%s", err, output)
	}
}
