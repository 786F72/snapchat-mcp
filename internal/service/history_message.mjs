// Text-only projection and stable server pagination boundaries. Never decodes
// snap/media payloads or invokes viewing/read-state methods.
export function historyServerID(value) {
 const raw=typeof value==='object'&&value!==null?(value.messageId??value.serverMessageId):value;
 if(typeof raw==='number'&&!Number.isSafeInteger(raw))throw Error('unsafe server message identifier');
 if(!['bigint','number','string'].includes(typeof raw)||!/^\d+$/.test(String(raw))||BigInt(raw)<=0n||BigInt(raw)>9223372036854775807n)throw Error('invalid server message identifier');
 return String(BigInt(raw));
}

export async function collectTextHistory(messages,{chatType,decode,identify,uuid,before,hasMore}) {
 const items=[],seen=new Set();let oldest,oldestNative;
 for(const message of messages) {
  const id=historyServerID(await identify(message));
  if(before&&BigInt(id)>=BigInt(before))continue;
  if(seen.has(id))continue;
  seen.add(id);
  if(oldest===undefined||BigInt(id)<BigInt(oldest)){oldest=id;oldestNative=String(message.descriptor.messageId)}
  if(message.messageContent?.contentType!==chatType)continue;
  const decoded=decode(message.messageContent.content);
  if(decoded.content?.$case!=='text')continue;
  const text=decoded.content.text.text,created=Number(message.metadata?.createdAt);
  if(typeof text!=='string'||text.length>65536||!Number.isSafeInteger(created)||created<0)throw Error('invalid history message');
  items.push({id,sender_id:uuid(message.senderId),text,created_at:created});
 }
 items.sort((a,b)=>BigInt(a.id)>BigInt(b.id)?-1:BigInt(a.id)<BigInt(b.id)?1:0);
 if(hasMore&&oldest===undefined)throw Error('history pagination made no progress');
 return {items,has_more:Boolean(hasMore),next_before:hasMore?oldest:undefined,next_native:hasMore?oldestNative:undefined,total_events:seen.size,text_only:true,backend:'standalone',media_opened:false,read_receipts_sent:false};
}
