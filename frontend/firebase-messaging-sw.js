/* eslint-env serviceworker */
/* global firebase */

// ---------------------------------------------------------------------------
// Firebase Cloud Messaging – Service Worker
//
// This file MUST be served at the root scope (e.g. /firebase-messaging-sw.js)
// so the browser can register it with the correct scope for push events.
//
// The Firebase config is passed via query parameters on the SW registration URL
// so that Firebase can be initialized at the TOP LEVEL during initial evaluation.
// This is required because the FCM SDK needs push/notificationclick handlers
// registered synchronously, not deferred.
// ---------------------------------------------------------------------------

importScripts('https://www.gstatic.com/firebasejs/10.12.0/firebase-app-compat.js');
importScripts('https://www.gstatic.com/firebasejs/10.12.0/firebase-messaging-compat.js');

// Parse Firebase config from the SW registration URL query params
const swUrl = new URL(self.location.href);
const swConfig = {
  apiKey:            swUrl.searchParams.get('apiKey')            || '',
  authDomain:        swUrl.searchParams.get('authDomain')        || '',
  projectId:         swUrl.searchParams.get('projectId')         || '',
  storageBucket:     swUrl.searchParams.get('storageBucket')     || '',
  messagingSenderId: swUrl.searchParams.get('messagingSenderId') || '',
  appId:             swUrl.searchParams.get('appId')             || '',
};

if (swConfig.apiKey && swConfig.projectId) {
  firebase.initializeApp(swConfig);
  const messaging = firebase.messaging();

  messaging.onBackgroundMessage((payload) => {
    console.log('[SW] Background message received:', payload);

    const data = payload.data || {};
    let title = 'Aarpaar';
    let body = '';
    let tag = 'aarpaar-' + Date.now();
    let requireInteraction = false;

    switch (data.type) {
      case 'incoming_call':
        title = (data.callerName || 'Someone') + ' is calling';
        body = (data.hasVideo === 'video' ? 'Video' : 'Audio') + ' call';
        tag = 'call-' + data.callId;
        requireInteraction = true;
        break;
      case 'missed_call':
        title = 'Missed call';
        body = 'from ' + (data.callerName || 'Unknown');
        tag = 'missed-' + data.callId;
        break;
      case 'new_message':
        title = data.senderName || 'New message';
        body = data.preview || 'Sent you a message';
        tag = 'msg-' + data.roomId;
        break;
      case 'dm_request':
        title = 'Message request';
        body = (data.senderName || 'Someone') + ' wants to message you';
        tag = 'dm-req-' + data.roomId;
        break;
      case 'friend_request':
        title = 'Friend request';
        body = (data.senderName || 'Someone') + ' sent you a friend request';
        tag = 'fr-' + data.senderId;
        break;
      default:
        title = 'Aarpaar';
        body = data.type || 'You have a new notification';
        break;
    }

    self.registration.showNotification(title, {
      body,
      tag,
      requireInteraction,
      icon: '/favicon.ico',
      data: data,
    });
  });

  console.log('[SW] Firebase Messaging initialized (project: ' + swConfig.projectId + ')');
} else {
  console.warn('[SW] No Firebase config in SW URL params — push notifications disabled');
}

// Handle notification click — focus existing tab or open new one
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clients) => {
      if (clients.length > 0) {
        return clients[0].focus();
      }
      return self.clients.openWindow('/');
    })
  );
});
