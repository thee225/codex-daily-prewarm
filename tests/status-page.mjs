import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';
const source = readFileSync(new URL('../management.go', import.meta.url), 'utf8');
const script = source.split('const statusPageActionScript = `<script>')[1].split('</script>`')[0];
const now = '2026-09-26T10:00:00+08:00';
const zero = '0001-01-01T00:00:00Z';
const valid = {account:'fixture',quota_status:'confirmed',quota_checked_at:now,five_hour:{window_minutes:300,reset_at:now},weekly:{window_minutes:10080,reset_at:now},attempts_24h:1,response_received:true};
for (const [label,item,known] of [
 ['valid zero percent', valid,true],
 ['failed query',{...valid,quota_status:'query_failed'},false],
 ['zero time',{...valid,quota_checked_at:zero},false],
 ['missing window',{...valid,five_hour:{}},false],
 ['invalid reset',{...valid,weekly:{window_minutes:10080,reset_at:zero}},false],
]) {
 const elements = new Map();
 const document = {createElement(){return {style:{},textContent:''};},createTextNode(text){return {textContent:text};},getElementById(id){if (!elements.has(id)) elements.set(id,{textContent:'',addEventListener(){},appendChild(child){this.textContent+=child.textContent;}});return elements.get(id);}};
 const state={notifications:{last_digest_at:now,last_digest_result:'accepted',next_digest_at:'2026-09-26T22:30:00+08:00'},config:{timezone:'Asia/Shanghai'},last_error:'state_write_failed',last_run:{finished_at:now,quota_queried_accounts:1,discovered_accounts:1,would_warm_accounts:1,attempted_accounts:1,succeeded_accounts:1,failed_accounts:0,skipped_accounts:0,error_code:'test_failure',accounts:[item]}};
 vm.runInNewContext(script,{setInterval(){},document,localStorage:{getItem(){return JSON.stringify({state:{rememberPassword:true,managementKey:'test-only'}});}},fetch:async()=>({ok:true,status:200,json:async()=>state})});
 await new Promise(resolve=>setImmediate(resolve));
 const text=elements.get('status-details').textContent;
 assert.ok(text.includes('日报最近发送：') && text.includes('日报结果：accepted') && text.includes('下次日报：'), text);
 assert.equal(text.includes('5h 100% / 周 100%'),known,label);
 assert.equal(text.includes('额度未知'),!known,label);
 assert.ok(text.includes('符合预热 1') && text.includes('失败 0') && text.includes('错误 test_failure') && text.includes('运行错误：state_write_failed'),text);
}
console.log('status page: 5 fixture cases passed');


// A server-side preference governs all clients; switching saves through management auth.
const elements=new Map(), handlers={};
const document={getElementById(id){if(!elements.has(id))elements.set(id,{textContent:'',checked:false,addEventListener(e,f){handlers[id+e]=f},appendChild(c){this.textContent+=c.textContent}});return elements.get(id)},createElement(){return{style:{},textContent:''}},createTextNode(t){return{textContent:t}}};
const ids=['acct-111111111111','acct-222222222222'];
let enabled=true, rejectSave=false;
const state=()=>({account_display:{enabled,accounts:enabled?{[ids[0]]:{name:'same@example.com',account_type:'plus'},[ids[1]]:{name:'same@example.com',account_type:'team'}}:{}},config:{timezone:'Asia/Shanghai'},notifications:{},last_run:{accounts:ids.map(account=>({...valid,account}))}});
const writes=[];
vm.runInNewContext(script,{setInterval(){},document,localStorage:{getItem(){return JSON.stringify({state:{rememberPassword:true,managementKey:'test-only'}})}},fetch:async(path,options)=>{
 if(options.method==='PUT'){writes.push(JSON.parse(options.body));if(rejectSave)return{ok:false,status:503,json:async()=>({error:'save_failed'})};enabled=JSON.parse(options.body).enabled;return{ok:true,status:200,json:async()=>({enabled})}}
 return{ok:true,status:200,json:async()=>state()}
}});
const flush=()=>new Promise(resolve=>setImmediate(resolve));await flush();
const toggle=elements.get('show-account-details'),output=elements.get('status-details');
assert.equal(toggle.checked,true);
assert.ok(output.textContent.includes('same@example.com · Plus')&&output.textContent.includes('same@example.com · Team'));
toggle.checked=false;await handlers['show-account-detailschange']();
assert.deepEqual(writes[0],{enabled:false});assert.equal(output.textContent.includes('same@example.com'),false);
toggle.checked=true;await handlers['show-account-detailschange']();assert.equal(enabled,true);
rejectSave=true;toggle.checked=false;await handlers['show-account-detailschange']();
assert.equal(enabled,true);assert.equal(toggle.checked,true);assert.ok(output.textContent.includes('保存失败'));
console.log('global display: server preference, shared Plus/Team, persistence request and save failure passed');
