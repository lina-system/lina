// SPDX-License-Identifier: Apache-2.0

// The only SDK methods a platform driver may reach. The contract test enumerates
// each pinned platform module and fails when the add-on's source names anything
// outside this table, so a write, a read-state change or a credential
// extraction cannot be added without failing the build.
//
// Sign-in is the one place a driver calls SDK code that writes: `loginFlow`,
// `loginWithRemoteAuth` and `connectForQR`/`connectForPairing` set up the
// account's own session, and the credential managers store that session in the
// add-on's data directory. They are listed here for the sign-in path only; no
// read operation reaches them. Token extractors are never listed.
export const READ_METHODS: Record<string, readonly string[]> = {
  kakaotalk: [
    'KakaoTalkClient', 'login', 'acquireSession', 'getChats', 'getMessagePage', 'getMembers', 'getProfile',
    'onPush', 'close', 'KakaoCredentialManager', 'getAccount', 'setAccount', 'setCurrentAccount',
    'loadPendingLogin', 'savePendingLogin', 'clearPendingLogin',
    'attemptLogin', 'requestPasscode', 'registerDevice', 'generateDeviceUuid', 'classifyKakaoChat',
  ],
  discord: [
    'DiscordClient', 'login', 'testAuth', 'listChannels', 'listDMChannels', 'getMessages',
    'listUsers', 'DiscordCredentialManager', 'getToken', 'setToken', 'loginWithRemoteAuth',
  ],
  whatsapp: [
    'WhatsAppClient', 'login', 'listChats', 'getMessages', 'getProfile', 'close',
    'connectForQR', 'connectForPairing',
  ],
  line: [
    'LineClient', 'login', 'getChats', 'getMessages', 'getProfile', 'getFriends', 'close',
    'loginWithQR', 'loginWithEmail', 'LineCredentialManager', 'setAccount',
  ],
};

// SDK methods that are never allowed anywhere in the add-on, including sign-in.
export const FORBIDDEN_METHODS: readonly string[] = [
  'sendMessage', 'sendTyping', 'sendAttachment', 'sendPhoto', 'sendVideo', 'sendAudio', 'sendFile',
  'sendMultiPhoto', 'sendReaction', 'markRead', 'getReadWatermarks', 'leaveChat', 'ackMessage',
  'addReaction', 'removeReaction', 'editMessage', 'deleteMessage', 'deleteEmoji', 'createEmoji',
  'createSticker', 'deleteSticker', 'createDM', 'createThread', 'archiveThread', 'uploadFile',
  'setUserNote', 'setNote', 'gatewayConnect', 'request', 'requestFormData', 'getUnreadDMs',
  'getUnreadMentions', 'fetchReadState', 'fetchReadStateSnapshot', 'searchMessages', 'sendMessageBatch',
  'removeAccount', 'sendPacket', 'sendReply', 'extractViaCDP', 'getReadState', 'getReadStateSnapshot',
  'tokenExtractor', 'extract', 'extractToken', 'KakaoTokenExtractor', 'importCredentials',
];
