/* eslint-env serviceworker */
/* global firebase */

// ---------------------------------------------------------------------------
// Firebase Cloud Messaging – Service Worker
//
// This file MUST be served at the root scope (e.g. /firebase-messaging-sw.js)
// so the browser can register it with the correct scope for push events.
//
// For the test-frontend.html flow:
//   - The page sends the Firebase config to this SW via a "FIREBASE_CONFIG" message.
//   - The SW initializes Firebase Messaging and handles background pushes.
//   - Data-only payloads from the Go backend are turned into visible notifications.
// ---------------------------------------------------------------------------

importScripts('https://www.gstatic.com/firebasejs/10.12.0/firebase-app-compat.js');
importScripts('https://www.gstatic.com/firebasejs/10.12.0/firebase-messaging-compat.js');

let messagingInitialized = false;

// Listen for config message from the main page
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'FIREBASE_CONFIG' && !messagingInitialized) {
    firebase.initializeApp(event.data.config);
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

    messagingInitialized = true;
    console.log('[SW] Firebase Messaging initialized');
  }
});

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
