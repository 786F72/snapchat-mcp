// Snapchat wire CHAT is 1; native SDK CHAT is 2. Keep the wire check explicit.
export function protoFields(bytes){let pos=0;const fields=new Map();const read=()=>{let value=0n,shift=0n;for(let n=0;n<10;n++){if(pos>=bytes.length)throw Error('truncated protobuf');const byte=bytes[pos++];value|=BigInt(byte&127)<<shift;if(!(byte&128))return value;shift+=7n}throw Error('invalid protobuf')};while(pos<bytes.length){const tag=Number(read()),field=tag>>>3,wire=tag&7;if(!field)throw Error('invalid field');let value;if(wire===0)value=read();else if(wire===2){const length=Number(read());if(length>bytes.length-pos)throw Error('truncated bytes');value=bytes.subarray(pos,pos+length);pos+=length}else if(wire===1){pos+=8}else if(wire===5){pos+=4}else throw Error('unsupported wire type');if(pos>bytes.length)throw Error('truncated field');if(!fields.has(field))fields.set(field,[]);fields.get(field).push(value)}return fields}
export async function validateSendPacket(request,conversationID,options={}){
 const bytes=new Uint8Array(await request.clone().arrayBuffer());
 if(bytes.length<5||bytes[0]!==0||new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength).getUint32(1)!==bytes.length-5)throw Error('invalid send frame');
 const envelope=protoFields(bytes.subarray(5));const resolution=envelope.get(2);
 if(resolution?.length!==1||typeof resolution[0]!=='bigint'||resolution[0]===0n)throw Error('invalid client resolution');
 const destinations=envelope.get(3);if(destinations?.length!==1)throw Error('expected one destination');
 const expected=Buffer.from(conversationID.replaceAll('-',''),'hex');const destination=protoFields(destinations[0]);
 const destinationField=options.destinationType==='story'?2:1;
 if([...destination.keys()].some(k=>[1,2,3].includes(k)&&k!==destinationField)||destination.get(destinationField)?.length!==1)throw Error('unexpected destination type');
 const conversation=protoFields(destination.get(destinationField)[0]);const id=protoFields(conversation.get(1)?.[0]??new Uint8Array()).get(1)?.[0];
 if(!id||!Buffer.from(id).equals(expected))throw Error('unexpected destination');
 if(options.destinationType==='story'&&(!options.storyData||!conversation.get(2)?.[0]||!Buffer.from(conversation.get(2)[0]).equals(Buffer.from(options.storyData))))throw Error('unexpected story audience metadata');
 const content=protoFields(envelope.get(4)?.[0]??new Uint8Array());
 const cryptoEnvelope=protoFields(content.get(3)?.[0]??new Uint8Array());
 if(options.mediaObject){
  const lists=content.get(5)??[];const references=lists.flatMap(list=>protoFields(list).get(1)??[]);
  if(references.length!==1)throw Error('expected one remote media reference');
  const reference=protoFields(references[0]);if(reference.has(2)||reference.get(3)?.length!==1||!Buffer.from(reference.get(3)[0]).equals(Buffer.from(options.mediaObject))||(reference.get(6)?.[0]??0n)!==0n)throw Error('unexpected remote media reference');
 }
 const wireType=content.get(2)?.[0]??0n;
 if(wireType!==BigInt(options.wireContentType??1)||!content.get(4)?.[0]?.length)throw Error('unexpected message content');
 const methods=[...cryptoEnvelope.keys()];let encryptionMethod;
 if(methods.length===1&&methods[0]===5)encryptionMethod=5;
 else if(methods.length===0&&['USER_CREATED_GROUP','MY_STORY'].includes(options.conversationType)&&options.plaintext&&Buffer.from(content.get(4)[0]).equals(Buffer.from(options.plaintext)))encryptionMethod=0;
 else throw Error('unvalidated message encryption');
 return{wire_content_type:Number(wireType),encryption_method:encryptionMethod,client_resolution_id:String(resolution[0])};
}
