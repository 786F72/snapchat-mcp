import test from 'node:test';
import assert from 'node:assert/strict';
import {preuploadedMediaDelegate,attachPreuploadedMedia} from './media_upload.mjs';

const ref={id:new TextEncoder().encode('1')};
for(const [mime,hasAudio,type] of [['image/jpeg',false,2],['image/png',false,2],['video/mp4',false,9],['video/mp4',true,3]]){
 test(`native preuploaded callback returns ${mime}, audio=${hasAudio}`,()=>{
  const object=Buffer.from([1,2,3]);const media={mime_type:mime,has_audio:hasAudio,content_object:object.toString('base64')};
  let calls=0;const content={localMediaReferences:[ref],content:new Uint8Array([4,5])};
  preuploadedMediaDelegate(media).uploadMedia(content,undefined,{onUploadFinished(results,updated){
   calls++;
   assert.equal(updated,content);
   assert.equal(results.length,1);
   assert.equal(results[0].status,0);
   assert.ok(results[0].timers instanceof Map);
   assert.equal(results[0].mediaOrchestrationAttemptId.id.length,16);
   const refs=results[0].remoteMediaReferences.mediaReferences;
   assert.equal(refs.length,1);
   assert.deepEqual(Buffer.from(refs[0].contentObject),object);
   assert.equal(refs[0].mediaListId,0n);
   assert.equal(refs[0].mediaType,type);
  }});
  assert.equal(calls,1);
 });
}
test('rejects missing, extra, or unexpected local references before callback',()=>{
 const media={content_object:'AQ==',mime_type:'image/png'};let calls=0;
 const cb={onUploadFinished(){calls++}};
 for(const refs of [[],[ref,ref],[{id:new Uint8Array([2])}]])assert.throws(()=>preuploadedMediaDelegate(media).uploadMedia({localMediaReferences:refs},null,cb));
 assert.throws(()=>preuploadedMediaDelegate(null).uploadMedia({localMediaReferences:[ref]},null,cb));
 assert.throws(()=>preuploadedMediaDelegate({...media,content_object:''}).uploadMedia({localMediaReferences:[ref]},null,cb));
 assert.throws(()=>preuploadedMediaDelegate({...media,mime_type:'text/html'}).uploadMedia({localMediaReferences:[ref]},null,cb));
 assert.equal(calls,0);
});

for(const [mime,type] of [['image/jpeg',0],['image/png',0],['video/mp4',1]]){
 test(`post-upload ${mime} attaches both key formats and preserves the local root reference`,()=>{
  const media={mime_type:mime,key:Buffer.alloc(32,4).toString('base64'),iv:Buffer.alloc(16,5).toString('base64')};
  const rootRef={mediaListId:'1',localCacheKey:'1',mediaType:2};
  const original={content:{$case:'snapdoc',snapdoc:{mediaReferences:[rootRef],mediaListIdCounter:'2',playback:{playbackLayers:[{layer:{$case:'media',media:{mediaId:{mediaListId:'1'},dimensions:{width:1080,height:1920}}}}]}}}};
  let encoded;
  const input={content:new Uint8Array([1]),localMediaReferences:[ref],savePolicy:2};
  const codec={decode:bytes=>{assert.equal(bytes,input.content);return structuredClone(original)},encode:value=>{encoded=value;return{finish:()=>new Uint8Array([2])}}};
  const updated=attachPreuploadedMedia(input,media,codec);
  const doc=encoded.content.snapdoc,layer=doc.playback.playbackLayers[0].layer.media;
  assert.deepEqual(doc.mediaReferences,[rootRef]);
  assert.equal(doc.mediaListIdCounter,'2');
  assert.deepEqual(layer.mediaId,{mediaListId:'0'});
  assert.deepEqual(layer.dimensions,{width:1080,height:1920});
  assert.equal(layer.type,type);assert.equal(layer.zipped,false);
  assert.equal(new TextDecoder().decode(layer.encryptionInfoV1.key),media.key);
  assert.equal(new TextDecoder().decode(layer.encryptionInfoV1.iv),media.iv);
  assert.deepEqual(Buffer.from(layer.encryptionInfoV2.key),Buffer.alloc(32,4));
  assert.deepEqual(Buffer.from(layer.encryptionInfoV2.iv),Buffer.alloc(16,5));
  assert.deepEqual(updated.content,new Uint8Array([2]));
  assert.equal(updated.localMediaReferences,input.localMediaReferences);
  assert.equal(updated.savePolicy,2);
  assert.deepEqual(original.content.snapdoc.playback.playbackLayers[0].layer.media.mediaId,{mediaListId:'1'});
 });
}
test('post-upload rejects invalid keys and ambiguous media layers',()=>{
 const media={mime_type:'image/jpeg',key:Buffer.alloc(32).toString('base64'),iv:Buffer.alloc(16).toString('base64')};
 const codec={decode:()=>({content:{$case:'snapdoc',snapdoc:{playback:{playbackLayers:[]}}}})};
 for(const patch of [{key:'AA=='},{iv:'AA=='},{mime_type:'text/html'}])assert.throws(()=>attachPreuploadedMedia({}, {...media,...patch},codec));
 assert.throws(()=>attachPreuploadedMedia({},media,codec),/one media/);
 assert.throws(()=>attachPreuploadedMedia({},media,{decode:()=>({content:{$case:'text'}})}),/snapdoc/);
});
