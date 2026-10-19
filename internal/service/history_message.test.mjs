import test from 'node:test';
import assert from 'node:assert/strict';
import {collectTextHistory,historyServerID} from './history_message.mjs';
const message=(server,kind=2,local=server)=>({server,descriptor:{messageId:local},senderId:'sender',messageContent:{contentType:kind,content:new Uint8Array()},metadata:{createdAt:100}});
const options={chatType:2,decode:()=>({content:{$case:'text',text:{text:'fixture text'}}}),identify:m=>m.server,uuid:x=>x};
test('history uses server order and retains the native continuation separately',async()=>{
 const result=await collectTextHistory([message(10n,2,900n),message(8n,1,800n),message(9n,2,850n)],{...options,hasMore:true});
 assert.deepEqual(result.items.map(x=>x.id),['10','9']);assert.equal(result.next_before,'8');assert.equal(result.next_native,'800');assert.equal(result.total_events,3);
 assert.equal(result.media_opened,false);assert.equal(result.read_receipts_sent,false);
});
test('media-only pages advance without decoding media',async()=>{
 const result=await collectTextHistory([message(7n,1)],{...options,decode:()=>{throw Error('must not decode media')},hasMore:true});
 assert.deepEqual(result.items,[]);assert.equal(result.next_before,'7');
});
test('inclusive/replayed boundary events are filtered and duplicate IDs deduplicated',async()=>{
 const result=await collectTextHistory([message(10n),message(9n),message(9n)],{...options,before:'10',hasMore:true});
 assert.equal(result.items.length,1);assert.equal(result.next_before,'9');assert.equal(result.total_events,1);
 await assert.rejects(collectTextHistory([message(10n)],{...options,before:'10',hasMore:true}),/no progress/);
 assert.equal((await collectTextHistory([],{...options,hasMore:false})).has_more,false);
});
test('unsafe IDs, invalid timestamps and oversized text fail closed',async()=>{
 for(const id of [undefined,-1,0n,'not-an-id',Number.MAX_SAFE_INTEGER+1,9223372036854775808n])assert.throws(()=>historyServerID(id));
 assert.equal(historyServerID({serverMessageId:12n}),'12');
 const invalid=message(1n);invalid.metadata.createdAt=-1;await assert.rejects(collectTextHistory([invalid],options));
 await assert.rejects(collectTextHistory([message(1n)],{...options,decode:()=>({content:{$case:'text',text:{text:'x'.repeat(65537)}}})}));
});
