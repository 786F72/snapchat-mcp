import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const source=(await readFile(new URL('./packet.mjs',import.meta.url),'utf8'))+'\n'+(await readFile(new URL('./native_actions.mjs',import.meta.url),'utf8'));
const {validateActionPacket,actionRequestPath}=await import('data:text/javascript;base64,'+Buffer.from(source).toString('base64'));
const conversation='11111111-1111-4111-8111-111111111111',self='22222222-2222-4222-8222-222222222222',other='33333333-3333-4333-8333-333333333333';
const varint=n=>{const b=[];do{let v=n&127;n>>>=7;if(n)v|=128;b.push(v)}while(n);return Buffer.from(b)};
const bytes=(n,v)=>Buffer.concat([varint(n*8+2),varint(v.length),v]);
const number=(n,v)=>Buffer.concat([varint(n*8),varint(v)]);
const uuid=id=>bytes(1,Buffer.from(id.replaceAll('-',''),'hex'));
function packet({action='rename',actor=self,destination=conversation,title='Fixture title',leaver=self,version=1,extra=Buffer.alloc(0)}={}){
 const change=Buffer.concat([bytes(1,uuid(actor)),action==='rename'?bytes(3,Buffer.from(title)):bytes(2,uuid(leaver))]);
 const body=Buffer.concat([bytes(1,uuid(destination)),number(2,123),number(3,version),bytes(action==='rename'?4:6,change),extra]);const header=Buffer.alloc(5);header.writeUInt32BE(body.length,1);
 return new Request('https://web.snapchat.com'+actionRequestPath('groups.'+action),{method:'POST',body:Buffer.concat([header,body])});
}
const snapshot=action=>({action:'groups.'+action,conversation_id:conversation,user_id:self,title:'Fixture title'});
test('prepares only the intended rename or self leave packet',async()=>{
 for(const action of ['rename','leave']){const r=await validateActionPacket(packet({action}),snapshot(action));assert.equal(r.conversation_id,conversation);assert.equal(r.client_resolution_id,'123')}
 assert.throws(()=>actionRequestPath('groups.create'));
});
test('rejects altered destinations, actors, titles, leavers and versions',async()=>{
 for(const options of [{destination:other},{actor:other},{title:'Wrong title'},{version:0},{extra:bytes(5,uuid(other))},{extra:bytes(1,uuid(other))}])await assert.rejects(validateActionPacket(packet(options),snapshot('rename')));
 await assert.rejects(validateActionPacket(packet({action:'leave',leaver:other}),snapshot('leave')));
 await assert.rejects(validateActionPacket(packet({action:'leave'}),snapshot('rename')));
});
test('rejects truncated or compressed action frames',async()=>{
 for(const body of [Buffer.from([0,0,0]),Buffer.from([1,0,0,0,0])])await assert.rejects(validateActionPacket(new Request('https://example.test',{method:'POST',body}),snapshot('rename')));
});
