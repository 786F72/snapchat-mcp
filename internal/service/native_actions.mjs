const actionRPC='/messagingcoreservice.MessagingCoreService/UpdateConversation';
export function actionRequestPath(action) {
 if(!['groups.rename','groups.leave'].includes(action))throw Error('unsupported group preparation');
 return actionRPC;
}
function actionUUID(bytes,expected) {
 const fields=bytes&&protoFields(bytes),value=fields?.get(1)?.[0];
 if(fields?.size!==1||fields.get(1)?.length!==1||!(value instanceof Uint8Array)||!Buffer.from(value).equals(Buffer.from(expected.replaceAll('-',''),'hex')))throw Error('unexpected action identity');
}
export async function validateActionPacket(request,snapshot) {
 const bytes=new Uint8Array(await request.clone().arrayBuffer());
 if(bytes.length<5||bytes.length>1048576||bytes[0]!==0||new DataView(bytes.buffer,bytes.byteOffset,bytes.byteLength).getUint32(1)!==bytes.length-5)throw Error('invalid action frame');
 const fields=protoFields(bytes.subarray(5)),action=snapshot.action;
 const expectedField={'groups.rename':4,'groups.leave':6}[action];
 if(!expectedField||[...fields.keys()].some(k=>![1,2,3,expectedField].includes(k))||[...fields.values()].some(v=>v.length!==1))throw Error('unexpected group action');
 actionUUID(fields.get(1)?.[0],snapshot.conversation_id);
 const resolution=fields.get(2)?.[0],version=fields.get(3)?.[0];
 if(typeof resolution!=='bigint'||resolution<=0n||typeof version!=='bigint'||version<=0n||!(fields.get(expectedField)?.[0] instanceof Uint8Array))throw Error('missing action resolution or version');
 const update=protoFields(fields.get(expectedField)[0]);
 if([...update.keys()].some(k=>!(action==='groups.rename'?[1,2,3]:[1,2]).includes(k))||[...update.values()].some(v=>v.length!==1))throw Error('unexpected action fields');
 actionUUID(update.get(1)?.[0],snapshot.user_id);
 if(action==='groups.rename'&&(!(update.get(3)?.[0] instanceof Uint8Array)||Buffer.from(update.get(3)[0]).toString('utf8')!==snapshot.title))throw Error('unexpected group title');
 if(action==='groups.leave')actionUUID(update.get(2)?.[0],snapshot.user_id);
 return {action,client_resolution_id:String(resolution),conversation_id:snapshot.conversation_id,current_version:String(version)};
}
export async function invokeNativeAction(manager,conv,snapshot) {
 const callback=(resolve,reject)=>({onComplete:resolve,onSuccess:resolve,onError:()=>reject(Error('native action failed'))});
 if(snapshot.action==='groups.rename')return new Promise((resolve,reject)=>manager.updateConversationTitle(conv,snapshot.title,callback(resolve,reject)));
 if(snapshot.action==='groups.leave')return new Promise((resolve,reject)=>manager.leaveConversation(conv,callback(resolve,reject)));
 throw Error('native group action not implemented');
}
