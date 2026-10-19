import test from 'node:test';
import assert from 'node:assert/strict';
import {validateSendPacket} from './packet.mjs';
const id='00112233-4455-6677-8899-aabbccddeeff';
const varint=n=>{const bytes=[];do{let b=n&127;n>>>=7;if(n)b|=128;bytes.push(b)}while(n);return Buffer.from(bytes)};
const field=(tag,data)=>Buffer.concat([varint((tag<<3)|2),varint(data.length),data]);
function fixture({wireType=1,cryptoMethod=5,recipient=id,count=1,resolution=123,payload=Buffer.from([255,0,255]),storyData,destinationType='conversation',mediaObject}={}){
 const uuid=field(1,Buffer.from(recipient.replaceAll('-',''),'hex'));
 const destination=field(destinationType==='story'?2:1,Buffer.concat([field(1,uuid),...(storyData?[field(2,storyData)]:[])]));
 const content=Buffer.concat([varint(2<<3),varint(wireType),...(cryptoMethod===null?[]:[field(3,field(cryptoMethod,Buffer.from([8,1])))]),field(4,payload),...(mediaObject?[field(5,field(1,field(3,mediaObject)))]:[])]);
 const body=Buffer.concat([...Array.from({length:count},()=>field(3,destination)),field(4,content),Buffer.from([16,resolution])]);
 const frame=Buffer.alloc(5);frame.writeUInt32BE(body.length,1);
 return new Request('https://web.snapchat.com/messagingcoreservice.MessagingCoreService/CreateContentMessage',{method:'POST',body:Buffer.concat([frame,body])});
}
test('accepts one intended EEL-encrypted wire CHAT destination',async()=>{
 assert.deepEqual(await validateSendPacket(fixture(),id),{wire_content_type:1,encryption_method:5,client_resolution_id:'123'});
});
test('rejects the native SDK enum when used as a wire CHAT enum',async()=>{
 await assert.rejects(validateSendPacket(fixture({wireType:2}),id));
});
test('rejects cleartext encryption and other recipients',async()=>{
 await assert.rejects(validateSendPacket(fixture({cryptoMethod:1}),id));
 await assert.rejects(validateSendPacket(fixture({recipient:'ffeeddcc-bbaa-9988-7766-554433221100'}),id));
});
test('rejects multiple destinations and truncated gRPC messages',async()=>{
 await assert.rejects(validateSendPacket(fixture({count:2}),id));
 await assert.rejects(validateSendPacket(new Request('https://example.test',{method:'POST',body:Buffer.from([0,0,0])}),id));
});

test('rejects missing resolution identity',async()=>{await assert.rejects(validateSendPacket(fixture({resolution:0}),id))});

test('group transport-only text requires exact expected plaintext and group context',async()=>{
 const payload=Buffer.from('expected group content');
 const options={conversationType:'USER_CREATED_GROUP',plaintext:payload};
 assert.equal((await validateSendPacket(fixture({cryptoMethod:null,payload}),id,options)).encryption_method,0);
 await assert.rejects(validateSendPacket(fixture({cryptoMethod:null,payload}),id));
 await assert.rejects(validateSendPacket(fixture({cryptoMethod:null,payload:Buffer.from('different')}),id,options));
});
test('snap packets require explicit SNAP wire type; story audience must match',async()=>{
 assert.equal((await validateSendPacket(fixture({wireType:0}),id,{wireContentType:0})).wire_content_type,0);
 await assert.rejects(validateSendPacket(fixture({wireType:0}),id));
 const storyData=Buffer.from([10,2,8,1]),payload=Buffer.from('expected encrypted media metadata');
 const options={conversationType:'MY_STORY',plaintext:payload,wireContentType:0,destinationType:'story',storyData};
 const packet=fixture({wireType:0,cryptoMethod:null,payload,destinationType:'story',storyData});
 assert.equal((await validateSendPacket(packet,id,options)).encryption_method,0);
 await assert.rejects(validateSendPacket(fixture({wireType:0,cryptoMethod:null,payload,destinationType:'story',storyData:Buffer.from([10,2,8,0])}),id,options));
});

test('media packets must reference the exact uploaded content object',async()=>{
 const mediaObject=Buffer.from('opaque uploaded object');const options={wireContentType:0,mediaObject};
 assert.equal((await validateSendPacket(fixture({wireType:0,mediaObject}),id,options)).wire_content_type,0);
 await assert.rejects(validateSendPacket(fixture({wireType:0}),id,options));
 await assert.rejects(validateSendPacket(fixture({wireType:0,mediaObject:Buffer.from('another object')}),id,options));
});
