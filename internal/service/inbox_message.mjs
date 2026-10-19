// Project incoming history into text or snap metadata. Snap content is never
// decoded, downloaded, or passed to any viewing/read-state API.
export async function collectInboxMessages(messages,{chatType,snapType,decode,identify,uuid}) {
 const items=[];
 for(const message of messages) {
  const kind=message.messageContent?.contentType;
  if(kind!==chatType&&kind!==snapType)continue;
  let text;
  if(kind===chatType) {
   const decoded=decode(message.messageContent.content);
   if(decoded.content?.$case!=='text')continue;
   text=decoded.content.text.text;
   if(typeof text!=='string'||text.length>65536)throw Error('invalid inbox text');
  }
  const value=await identify(message);
  const raw=typeof value==='object'&&value!==null?(value.messageId??value.serverMessageId):value;
  if(typeof raw==='number'&&!Number.isSafeInteger(raw))throw Error('unsafe server message identifier');
  if(!['bigint','number','string'].includes(typeof raw)||!/^\d+$/.test(String(raw))||BigInt(raw)<=0n)throw Error('unrecognized server message identifier');
  const created=Number(message.metadata?.createdAt);
  if(!Number.isSafeInteger(created)||created<0)throw Error('invalid inbox timestamp');
  const item={id:String(BigInt(raw)),sender_id:uuid(message.senderId),type:kind===snapType?'snap':'text',created_at:created};
  if(kind===chatType)item.text=text;
  items.push(item);
 }
 items.sort((a,b)=>BigInt(a.id)<BigInt(b.id)?-1:BigInt(a.id)>BigInt(b.id)?1:0);
 return items;
}
