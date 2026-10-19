const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const { webcrypto } = require('node:crypto');
const source = fs.readFileSync(`${__dirname}/bridge.js`, 'utf8');

function setup({missing = false, friends = ['friend'], outgoing = [], conversations, failure = false} = {}) {
  const calls = [];
  const uuid = {Bh: str => ({str}), QA: id => id.str};
  const one = {conversationId: {str: 'conversation'}, participants: [{participantId:{str:'self'}}, {participantId:{str:'friend'}}]};
  const state = {
    auth: {userId:'self'},
    user: {
      mutuallyConfirmedFriendIds:friends.map(str=>({str})), outgoingFriendRequestIds:outgoing.map(str=>({str})), friendsSyncStatus:'unset',
      async syncFriends(){state.user.friendsSyncStatus='success'},
      async ensureUsers(ids){assert.ok(ids.every(id=>typeof id.str==='string'));return ids.map(user_id=>({user_id,mutable_username:`name_${user_id.str}`,display_name:'Name'}))}
    },
    messaging: {
      client: {},
      async fetchConversationByParticipants(people){calls.push(['fetch',people.map(x=>x.str)]);return conversations ?? [one]},
      async createConversationForUsers(ids,self){calls.push(['create',ids,self.str]);return {conversation:one}},
      async ensureNetworkConversation(){throw Error('Separate network creation must not be called')},
      async sendTextMessage(id,text){calls.push(['send',id.str,text]);if(failure)throw Error('server rejected')}
    }
  };
  const context = vm.createContext({crypto:webcrypto});
  if (!missing) {
    const chunks=[];
    chunks.push = entry => entry[2](id=>id===68047?{M:{getState:()=>state}}:uuid);
    context.webpackChunk_snapchat_web_calling_app=chunks;
  }
  return {context, state, calls, init:()=>vm.runInContext(source,context)};
}
test('preview uses confirmed friends, deduplicates and excludes self',async()=>{
  const x=setup({friends:['friend','self','friend']});x.init();
  const list=await x.context.__qq_snap.friends();assert.equal(list.length,1);assert.equal(list[0].id,'friend');assert.equal(x.calls.length,0);
});
test('message is passed verbatim and send acknowledgement awaited',async()=>{
  const x=setup();x.init();const message='Hello "World"!\n${secret} `code`';
  assert.equal((await x.context.__qq_snap.send('friend',message)).ok,true);
  assert.deepEqual(x.calls.at(-1),['send','conversation',message]);
});
test('groups are excluded and a direct conversation is obtained',async()=>{
  const x=setup({conversations:[{conversationId:{str:'group'},participants:[{str:'self'},{str:'friend'},{str:'stranger'}]}]});x.init();
  await x.context.__qq_snap.send('friend','hello');assert.equal(x.calls.some(c=>c[0]==='create'),true);assert.equal(x.calls.at(-1)[1],'conversation');assert.equal(x.calls.at(-1)[0],'send');
});
test('unconfirmed recipients and empty messages are rejected before sending',async()=>{
  const x=setup();x.init();await assert.rejects(x.context.__qq_snap.send('stranger','hello'));await assert.rejects(x.context.__qq_snap.send('friend',' '));assert.equal(x.calls.length,0);
});
test('SDK failures propagate',async()=>{
  const x=setup({failure:true});x.init();await assert.rejects(x.context.__qq_snap.send('friend','hello'),/server rejected/);
});
test('adapter stops on unsupported app',()=>{
  const x=setup({missing:true});assert.throws(x.init,/sign in/);
});

test('added contacts require explicit scope and arbitrary recipients stay rejected',async()=>{
  const x=setup({friends:[],outgoing:['friend']});x.init();
  await assert.rejects(x.context.__qq_snap.send('friend','hello'));
  await assert.rejects(x.context.__qq_snap.send('stranger','hello',{allowAdded:true}));
  assert.equal(x.calls.length,0);
  await x.context.__qq_snap.send('friend','hello',{allowAdded:true});
  assert.equal(x.calls.at(-1)[0],'send');
});
