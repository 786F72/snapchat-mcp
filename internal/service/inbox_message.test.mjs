import test from 'node:test';
import assert from 'node:assert/strict';
import {collectInboxMessages} from './inbox_message.mjs';
const message=(id,kind=2)=>({descriptor:{messageId:id},senderId:'fixture',messageContent:{contentType:kind,content:new Uint8Array([1]),remoteMediaReferences:[{key:'private'}]},metadata:{createdAt:1000}});
const options={chatType:2,snapType:1,uuid:x=>x,identify:m=>m.descriptor.messageId,decode:()=>({content:{$case:'text',text:{text:'hello'}}})};
test('inbox returns text and bare snap metadata in server ID order',async()=>{
 let decoded=0;
 const items=await collectInboxMessages([message(9n,1),message(3n),message(4n,99)],{...options,decode:()=>{decoded++;return options.decode()}});
 assert.equal(decoded,1);assert.deepEqual(items,[{id:'3',sender_id:'fixture',type:'text',created_at:1000,text:'hello'},{id:'9',sender_id:'fixture',type:'snap',created_at:1000}]);
 assert.ok(!JSON.stringify(items).includes('private'));
});
test('snap collection never decodes snap content',async()=>{
 const items=await collectInboxMessages([message('7',1)],{...options,decode:()=>{throw Error('snap decoding is forbidden')}});
 assert.equal(items[0].type,'snap');assert.equal(items[0].text,undefined);
});
test('rejects unstable identifiers and invalid timestamps',async()=>{
 for(const id of [undefined,0n,'bad',Number.MAX_SAFE_INTEGER+1])await assert.rejects(collectInboxMessages([message(id)],options));
 const invalid=message(1n);invalid.metadata.createdAt=NaN;await assert.rejects(collectInboxMessages([invalid],options));
});
test('skips nontext chat payloads and accepts native identifier objects',async()=>{
 assert.deepEqual(await collectInboxMessages([message(1n)],{...options,decode:()=>({content:{$case:'sticker'}})}),[]);
 const items=await collectInboxMessages([message(1n)],{...options,identify:()=>({serverMessageId:12n})});assert.equal(items[0].id,'12');
});
