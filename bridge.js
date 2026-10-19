// Inspected against Snapchat's public web bundle 16a8351e5948f582369c.js.
// This uses the running client's API. It never exports cookies or private keys.
(() => {
  const chunk = globalThis.webpackChunk_snapchat_web_calling_app;
  if (!Array.isArray(chunk)) throw Error("Open and sign in to Snapchat for Web first.");
  let req;
  const key = "qq_snap_" + crypto.randomUUID();
  chunk.push([[key], {}, r => { req = r; }]);
  if (!req) throw Error("Snapchat's module loader changed; update the adapter.");
  const store = req(68047).M;
  const uuid = req(74918);
  if (!store?.getState || !uuid?.Bh || !uuid?.QA) throw Error("Unsupported Snapchat web build.");
  const state = () => store.getState();
  if (!state().messaging?.client || !state().user?.syncFriends) {
    throw Error("Wait for Snapchat to finish signing in and loading chats.");
  }
  const idString = id => typeof id === "string" ? id : uuid.QA(id);
  const api = {
    async check(id) {
      const self = state().auth.userId;
      const conversations = await state().messaging.fetchConversationByParticipants([uuid.Bh(id), uuid.Bh(self)]);
      const c = conversations.find(c => (c.participants || []).some(p => idString(p.participantId || p.userId || p) === id));
      if (!c) return {conversation_found: false};
      let legacyError = "";
      try { c.participants.map(p => idString(p.userId || p)); }
      catch(e) { legacyError = e.message; }
      const members = c.participants.map(p => idString(p.participantId || p.userId || p));
      return {conversation_found: true, participant_fields: Object.keys(c.participants[0]), participant_count: members.length,
        corrected_validation_ok: members.length === 2 && members.includes(id) && members.includes(self), legacy_validation_error: legacyError,
        active_send_stage: globalThis.__qq_snap_progress?.stage || null, conversation_fields: Object.keys(c)};
    },
    async friends() {
      await state().user.syncFriends();
      if (state().user.friendsSyncStatus !== "success") throw Error("Friend sync did not finish successfully.");
      const ids = [...new Set(state().user.mutuallyConfirmedFriendIds.map(idString))].filter(id => id !== state().auth.userId);
      const result = [];
      for (let i = 0; i < ids.length; i += 32) {
        const batch = ids.slice(i, i + 32);
        const users = await state().user.ensureUsers(batch.map(uuid.Bh));
        for (const id of batch) {
          const user = users.find(u => u.user_id && idString(u.user_id) === id);
          result.push({id, username: user?.mutable_username || user?.username || "", display_name: user?.display_name || ""});
        }
      }
      return result.sort((a, b) => a.id.localeCompare(b.id));
    },
    async send(id, message, options = {}) {
      globalThis.__qq_snap_progress = {id, stage: "validate"};
      if (typeof message !== "string" || !message.trim()) throw Error("The message must contain text.");
      const confirmed = state().user.mutuallyConfirmedFriendIds.some(friend => idString(friend) === id);
      const added = options.allowAdded === true && (state().user.outgoingFriendRequestIds || []).some(friend => idString(friend) === id);
      if (!confirmed && !added) throw Error("Recipient is no longer a confirmed friend or selected added contact.");
      const self = state().auth.userId;
      if (!self) throw Error("Could not determine the current user's ID.");
      const people = [uuid.Bh(id), uuid.Bh(self)];
      globalThis.__qq_snap_progress = {id, stage: "resolve"};
      let conversations = await state().messaging.fetchConversationByParticipants(people);
      // Matching all participants excludes groups, even if an API returns extra results.
      let conversation = conversations.find(c => {
        const p = (c.participants || []).map(x => idString(x.participantId || x.userId || x));
        return p.length === 2 && p.includes(id) && p.includes(self);
      });
      if (!conversation) {
        globalThis.__qq_snap_progress = {id, stage: "create"};
        const created = await state().messaging.createConversationForUsers([id], uuid.Bh(self));
        conversation = created.conversation;
        if (!conversation?.conversationId) throw Error("Could not create a one-to-one conversation.");
      }
      if (!conversation?.conversationId) throw Error("Could not obtain a one-to-one conversation.");
      const members = (conversation.participants || []).map(x => idString(x.participantId || x.userId || x));
      if (members.length !== 2 || !members.includes(id) || !members.includes(self)) throw Error("Conversation participants do not match the intended recipient.");
      // The inspected implementation awaits the SDK's onSuccess/onError callback.
      globalThis.__qq_snap_progress = {id, stage: "send"};
      await state().messaging.sendTextMessage(conversation.conversationId, message);
      globalThis.__qq_snap_progress = {id, stage: "acknowledged"};
      return {ok: true};
    }
  };
  globalThis.__qq_snap = api;
  return true;
})()
