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
 vm.runInNewContext(script,{document,localStorage:{getItem(){return JSON.stringify({state:{rememberPassword:true,managementKey:'test-only'}});}},fetch:async()=>({ok:true,status:200,json:async()=>state})});
 await new Promise(resolve=>setImmediate(resolve));
 const text=elements.get('status-details').textContent;
 assert.ok(text.includes('日报最近发送：') && text.includes('日报结果：accepted') && text.includes('下次日报：'), text);
 assert.equal(text.includes('5h 100% / 周 100%'),known,label);
 assert.equal(text.includes('额度未知'),!known,label);
 assert.ok(text.includes('符合预热 1') && text.includes('失败 0') && text.includes('错误 test_failure') && text.includes('运行错误：state_write_failed'),text);
}
console.log('status page: 5 fixture cases passed');

// Opt-in identity loading, same-email plans, fallback and immediate redaction.
for (const failure of [false,true]) {
 const handlers = {};
 const elements = new Map();
 const document = {
  getElementById(id){if (!elements.has(id)) elements.set(id,{textContent:'',checked:false,addEventListener(event,fn){handlers[id+event]=fn;},appendChild(child){this.textContent+=child.textContent;}});return elements.get(id);},
  createElement(){return {style:{},textContent:''};},createTextNode(text){return {textContent:text};}
 };
 const ids=['acct-111111111111','acct-222222222222','acct-333333333333'];
 const state={config:{timezone:'Asia/Shanghai'},notifications:{},accounts:{[ids[0]]:{five_hour_followup_at:now}},last_run:{finished_at:now,accounts:ids.map((account,i)=>({...valid,account,account_type:i===1?'team':'plus'}))}};
 const calls=[];
 const storage=new Map([['cli-proxy-auth',JSON.stringify({state:{rememberPassword:true,managementKey:'test-only'}})]]);
 const details={accounts:{[ids[0]]:{name:'same@example.com',account_type:'plus'},[ids[1]]:{name:'same@example.com',account_type:'team'},[ids[2]]:{name:'<img src=x onerror=alert(1)>'}}};
 let pendingResolve;
 let delayed=false;
 vm.runInNewContext(script,{document,localStorage:{getItem(k){return storage.get(k)||null;},setItem(k,v){storage.set(k,v);}},fetch:async(path)=>{
  calls.push(path);
  if(path.endsWith('/account-details')) {
   if(delayed) await new Promise(resolve=>{pendingResolve=resolve;});
   return {ok:!failure,status:failure?503:200,json:async()=>details};
  }
  return {ok:true,status:200,json:async()=>state};
 }});
 const flush=()=>new Promise(resolve=>setImmediate(resolve));
 await flush();
 assert.equal(calls.some(p=>p.endsWith('/account-details')),false);
 const toggle=elements.get('show-account-details');
 const output=elements.get('status-details');
 toggle.checked=true;handlers['show-account-detailschange']();await flush();
 if(!failure) {
  assert.ok(output.textContent.includes('same@example.com · Plus（'+ids[0]+'）'));
  assert.ok(output.textContent.includes('same@example.com · Team（'+ids[1]+'）'));
  assert.ok(output.textContent.includes('<img src=x onerror=alert(1)> · Plus')); // plain text only
 } else assert.ok(output.textContent.includes('账户详情暂不可用'));
 toggle.checked=false;handlers['show-account-detailschange']();
 assert.equal(output.textContent.includes('same@example.com'),false);
 await flush();
 assert.equal(output.textContent.includes('same@example.com'),false);
 if(!failure) {
  delayed=true;toggle.checked=true;handlers['show-account-detailschange']();await flush();
  toggle.checked=false;handlers['show-account-detailschange']();await flush();
  pendingResolve();await flush();
  assert.equal(output.textContent.includes('same@example.com'),false,'late response restored hidden identities');
 }
 assert.equal(storage.get('cpa-prewarm-show-account-details'),'false');
}
console.log('account details: opt-in, Plus/Team, safe text, failure and toggle race passed');
