// Implements the native upload delegate for already encrypted, account-bound
// media handles. No network upload or key generation takes place here.
export function preuploadedMediaDelegate(media, {onUpload=()=>{},updateContent=content=>content} = {}) {
 return {
  uploadMedia(content, _options, callback) {
   if(!media || content.localMediaReferences?.length!==1 ||
      Buffer.from(content.localMediaReferences[0].id??[]).toString()!=='1') {
    throw Error('unexpected local media reference');
   }
   if(typeof callback?.onUploadFinished!=='function')throw Error('missing native media upload callback');
   if(!['image/png','image/jpeg','video/mp4'].includes(media.mime_type))throw Error('unsupported uploaded media type');
   const object=new Uint8Array(Buffer.from(media.content_object,'base64'));
   if(!object.length)throw Error('missing uploaded media object');
   const isVideo=media.mime_type==='video/mp4';
   const result={status:0,timers:new Map(),mediaOrchestrationAttemptId:{id:new Uint8Array(16)},
    remoteMediaReferences:{mediaReferences:[{contentObject:object,mediaListId:0n,
     mediaType:isVideo?(media.has_audio?3:9):2,mediaReferenceKey:''}]}};
   const updated=updateContent(content);
   onUpload(updated);
   callback.onUploadFinished([result], updated);
  },
  uploadMediaReferences() { throw Error('unsupported native media reference upload'); }
 };
}

// Match the web client's post-upload update: the native pipeline receives its
// local media placeholder first; encryption metadata is attached in the callback.
export function attachPreuploadedMedia(content, media, codec) {
 if(!media||!['image/png','image/jpeg','video/mp4'].includes(media.mime_type))throw Error('unsupported uploaded media type');
 const key=Buffer.from(media.key??'','base64'),iv=Buffer.from(media.iv??'','base64');
 if(key.length!==32||iv.length!==16||key.toString('base64')!==media.key||iv.toString('base64')!==media.iv)throw Error('invalid uploaded media encryption metadata');
 const message=codec.decode(content.content);
 if(message.content?.$case!=='snapdoc')throw Error('expected snapdoc media content');
 const layers=message.content.snapdoc.playback?.playbackLayers??[];
 const mediaLayers=layers.filter(layer=>layer.layer?.$case==='media');
 if(mediaLayers.length!==1)throw Error('expected one media playback layer');
 const layer=mediaLayers[0].layer.media;
 layer.mediaId={mediaListId:'0'};
 layer.type=media.mime_type==='video/mp4'?1:0;
 layer.zipped=false;
 layer.encryptionInfoV1={key:new TextEncoder().encode(media.key),iv:new TextEncoder().encode(media.iv)};
 layer.encryptionInfoV2={key:new Uint8Array(key),iv:new Uint8Array(iv)};
 return {...content,content:codec.encode(message).finish()};
}
