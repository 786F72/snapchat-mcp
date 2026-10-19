// Offline probe for a pinned Snapchat messaging runtime. No network callbacks,
// registration, or credential output are permitted by this adapter.
import fs from 'node:fs';
import vm from 'node:vm';
import {createHash} from 'node:crypto';
import path from 'node:path';
const pins={
 'runtime.js':'b2f2d21f6e1e4214f2a47acb65a21a3ca950c8a7ab22a94ec16934db8cff131c',
 'messaging.wasm':'2ce913a96d256605ea3b9998e71a65ee93b4f736fa4289d27490ed7fa5a95cd5'
};
let stage='runtime',requestedRPC;const diagnostics={root_reads:0,wrapped_reads:0,write_attempts:0};
try {
 const root=process.argv[1];
 const read=name=>{const b=fs.readFileSync(path.join(root,name));if(createHash('sha256').update(b).digest('hex')!==pins[name])throw Error('artifact mismatch');return b};
 const source=read('runtime.js'),binary=read('messaging.wasm');
 let input='';for await(const chunk of process.stdin){input+=chunk;if(input.length>1<<20)throw Error('snapshot too large')}
 const snapshot=JSON.parse(input);
 // These callbacks deliberately make an offline key probe incapable of
 // registering or rotating server keys, including through a fallback path.
 globalThis.fetch=()=>{throw Error('network disabled')};globalThis.WebSocket=undefined;
 globalThis.webpackChunk_snapchat_web_calling_app=[];
 vm.runInThisContext(source.toString('utf8'),{filename:'pinned-snapchat-runtime.js'});
 const modules=Object.assign({},...globalThis.webpackChunk_snapchat_web_calling_app.map(x=>x[1])),cache={};
 function req(id){if(id===36675)return fs;if(cache[id])return cache[id].exports;const m={exports:{}};cache[id]=m;if(!modules[id])throw Error('missing module');modules[id](m,m.exports,req);return m.exports}
 req.d=(target,defs)=>{for(const[k,v]of Object.entries(defs))Object.defineProperty(target,k,{enumerable:true,get:v})};
 req.cw=fn=>{let m;return()=>{if(!m){m={exports:{}};fn(m,m.exports)}return m.exports}};
 req.o=(obj,key)=>Object.hasOwn(obj,key);req.g=globalThis;req.n=m=>{const getter=m&&m.__esModule?()=>m.default:()=>m;getter.a=getter;return getter};
 const native=await req(45117).A({wasmBinary:new Uint8Array(binary),print:()=>{},printErr:()=>{}});
 stage='platform';
 const enums=req(61056);
 native.shims_Platform.init({assertionMode:enums.S6.ALWAYS,minLogLevel:enums.$b.ERROR},{logTimedEvent:()=>{},log:()=>{}});
 native.shims_Platform.registerSerialTaskQueue({submit:(task,delay)=>setTimeout(()=>task.run(),Number(delay))});
 native.shims_Platform.installErrorReporter({reportError:()=>{}});
 native.shims_Platform.installNonFatalReporter({reportError:()=>{}});
 native.grpc_GrpcManager.registerWebFactory({createClient:()=>({unaryCall:(method)=>{requestedRPC=typeof method==='string'&&/^[A-Za-z0-9_./]{1,256}$/.test(method)?method:'unrecognized';throw Error('network disabled')},serverStreamingCall:()=>{throw Error('network disabled')},bidiStreamingCall:()=>{throw Error('network disabled')}})});
 stage='construct';
 const decode=s=>new Uint8Array(Buffer.from(s,'base64'));
 const wrapped=JSON.parse(snapshot.identity_storage).map(k=>({data:decode(k.data),lastUpdatedTimestamp:new Date(k.lastUpdatedTimestamp)}));
 const storedRoot=snapshot.root_wrapping_key;
 if(!storedRoot)throw Error('no captured root wrapping key');
 const rwk={rwk:{data:decode(storedRoot.data)},keyIdentifier:{data:decode(storedRoot.identity_key_id)}};
 const denyWrite=()=>{diagnostics.write_attempts++;throw Error('key changes disabled')};
 const keyEnums=req(2097);
 const manager=native.e2ee_E2EEKeyManager.constructPostLogin(
  {apiGatewayEndpoint:'https://web.snapchat.com',grpcPathPrefix:''},
  {storeUserWrappedIdentityKeys:denyWrite,loadUserWrappedIdentityKeys:async()=>{diagnostics.wrapped_reads++;return wrapped}},
  {storeRootWrappingKey:denyWrite,readRootWrappingKey:async()=>{diagnostics.root_reads++;return rwk},destroy:denyWrite,loadTemporaryIdentityKey:async()=>undefined,clearTemporaryIdentityKey:denyWrite},
  {onInitializationComplete:event=>{diagnostics.initialization_success=event.success;diagnostics.fresh_key=event.freshKey}},keyEnums.qg.HOLD,keyEnums.M0.TEN,false
 );
 stage='current_key';const key=await manager.getCurrentUserKeyAsync();
 const restored=key?.cleartextPrivateKey?.byteLength>0&&key?.cleartextPublicKey?.byteLength>0;
 if(!restored)throw Error('no restored key');
 console.log(JSON.stringify({wasm_loaded:true,keys_restored:true,network_enabled:false,key_version:key.version,protocol_ready:false}));
 process.exit(0);
} catch(error) {
 const allowed=new Set(['missing','field','key','identity','identifier','root','wrapping','decrypt','decryption','decode','invalid','mismatch','unsupported','version','load','failed','expected','type','undefined','logblizzardevent','logevent','public','private','synchronous','async','promise','value','array','buffer','byte','data']);const reasonTerms=[...new Set(String(error?.message??'').toLowerCase().split(/[^a-z]+/).filter(w=>allowed.has(w)))];
 console.log(JSON.stringify({wasm_loaded:stage!=='runtime',keys_restored:false,network_enabled:false,stage,diagnostics,reason_terms:reasonTerms,frame_names:String(error?.stack??'').split('\n').slice(1,6).map(x=>/at ([A-Za-z0-9_$.<> ]+) \(/.exec(x)?.[1]).filter(Boolean),missing_property:/reading ['"]([A-Za-z_]{1,64})['"]/.exec(String(error?.message??''))?.[1],requested_rpc:requestedRPC,error:'Offline key restoration did not complete; no network or key registration was allowed.'}));
 process.exit(1);
}
