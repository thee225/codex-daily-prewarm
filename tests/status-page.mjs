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
 const document = {getElementById(id){if (!elements.has(id)) elements.set(id,{textContent:'',addEventListener(){}});return elements.get(id);}};
 const state={config:{timezone:'Asia/Shanghai'},last_error:'state_write_failed',last_run:{finished_at:now,quota_queried_accounts:1,discovered_accounts:1,would_warm_accounts:1,attempted_accounts:1,succeeded_accounts:1,failed_accounts:0,skipped_accounts:0,error_code:'test_failure',accounts:[item]}};
 vm.runInNewContext(script,{document,localStorage:{getItem(){return JSON.stringify({state:{rememberPassword:true,managementKey:'test-only'}});}},fetch:async()=>({ok:true,status:200,json:async()=>state})});
 await new Promise(resolve=>setImmediate(resolve));
 const text=elements.get('status-details').textContent;
 assert.equal(text.includes('5h 100% / 周 100%'),known,label);
 assert.equal(text.includes('额度未知'),!known,label);
 assert.ok(text.includes('符合预热 1') && text.includes('失败 0') && text.includes('错误 test_failure') && text.includes('运行错误：state_write_failed'),text);
}
console.log('status page: 5 fixture cases passed');
