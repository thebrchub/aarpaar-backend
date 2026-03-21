// ---------------------------------------------------------------------------
// Arena REST Load Test — Authenticated Endpoint Throughput
//
// Target: 10L+ (1,000,000+) total requests against Arena endpoints.
//
// Usage:
//   k6 run tests/load/arena_load.js \
//     --env BASE_URL=https://your-app.railway.app \
//     --env AUTH_TOKEN=<jwt_access_token>
//
// To generate a token, log in via the frontend or use:
//   curl -X POST $BASE_URL/api/v1/auth/google -d '{"idToken":"..."}' \
//     | jq -r '.accessToken'
//
// Stages:
//   • Ramp 100 → 500 → 1000 → 2000 VUs over 10 minutes
//   • Sustained 2000 VUs for 5 minutes (~500K+ requests)
//   • Ramp down over 2 minutes
//   Total: ~1M+ requests in ~17 minutes
// ---------------------------------------------------------------------------

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";

// Custom metrics
const errorRate = new Rate("errors");
const feedLatency = new Trend("feed_latency_ms");
const postLatency = new Trend("post_create_latency_ms");
const commentLatency = new Trend("comment_latency_ms");
const totalRequests = new Counter("total_requests");

const BASE_URL = __ENV.BASE_URL || "http://localhost:2028";
const AUTH_TOKEN = __ENV.AUTH_TOKEN || "";

const headers = {
  Authorization: `Bearer ${AUTH_TOKEN}`,
  "Content-Type": "application/json",
};

export const options = {
  stages: [
    { duration: "1m", target: 100 },
    { duration: "2m", target: 500 },
    { duration: "3m", target: 1000 },
    { duration: "2m", target: 2000 },
    { duration: "5m", target: 2000 }, // sustained peak
    { duration: "2m", target: 100 },
    { duration: "1m", target: 0 },
  ],
  thresholds: {
    http_req_duration: ["p(95)<1000", "p(99)<2000"],
    errors: ["rate<0.05"],
    feed_latency_ms: ["p(95)<800"],
    post_create_latency_ms: ["p(95)<1500"],
  },
  noConnectionReuse: false,
};

// Pre-created post IDs will be collected during the test
const createdPostIDs = [];

export default function () {
  const rand = Math.random();

  if (rand < 0.40) {
    // 40% — Read feeds (heaviest real-world traffic)
    readFeed();
  } else if (rand < 0.55) {
    // 15% — Create post
    createPost();
  } else if (rand < 0.70) {
    // 15% — Like/unlike
    likePost();
  } else if (rand < 0.80) {
    // 10% — Comment
    commentOnPost();
  } else if (rand < 0.90) {
    // 10% — Bookmark
    bookmarkPost();
  } else {
    // 10% — Record views
    recordViews();
  }

  sleep(Math.random() * 0.2); // 0–200ms think time
}

function readFeed() {
  const feeds = [
    "/api/v1/arena/feed/global?limit=20",
    "/api/v1/arena/feed/trending?limit=20",
  ];
  const url = feeds[Math.floor(Math.random() * feeds.length)];

  const res = http.get(`${BASE_URL}${url}`, { headers });
  totalRequests.add(1);

  const ok = check(res, {
    "feed 200": (r) => r.status === 200,
  });
  errorRate.add(!ok);
  feedLatency.add(res.timings.duration);
}

function createPost() {
  const caption = `Load test post ${Date.now()}-${Math.random()
    .toString(36)
    .slice(2, 8)}`;
  const body = JSON.stringify({ caption });

  const res = http.post(`${BASE_URL}/api/v1/arena/posts`, body, { headers });
  totalRequests.add(1);

  const ok = check(res, {
    "create 200": (r) => r.status === 200,
  });
  errorRate.add(!ok);
  postLatency.add(res.timings.duration);

  // Collect post ID for subsequent operations
  if (ok) {
    try {
      const data = JSON.parse(res.body);
      if (data.id) createdPostIDs.push(data.id);
      // Keep the array from growing unbounded
      if (createdPostIDs.length > 500) createdPostIDs.shift();
    } catch (_) {}
  }
}

function likePost() {
  const postId = getRandomPostId();
  if (!postId) return readFeed(); // fallback

  const res = http.request(
    "POST",
    `${BASE_URL}/api/v1/arena/posts/${postId}/like`,
    null,
    { headers }
  );
  totalRequests.add(1);

  const ok = check(res, {
    "like 200": (r) => r.status === 200,
  });
  errorRate.add(!ok);
}

function commentOnPost() {
  const postId = getRandomPostId();
  if (!postId) return readFeed();

  const body = JSON.stringify({
    body: `Load comment ${Date.now()}`,
  });
  const res = http.post(
    `${BASE_URL}/api/v1/arena/posts/${postId}/comments`,
    body,
    { headers }
  );
  totalRequests.add(1);

  const ok = check(res, {
    "comment 200": (r) => r.status === 200,
  });
  errorRate.add(!ok);
  commentLatency.add(res.timings.duration);
}

function bookmarkPost() {
  const postId = getRandomPostId();
  if (!postId) return readFeed();

  const res = http.request(
    "POST",
    `${BASE_URL}/api/v1/arena/posts/${postId}/bookmark`,
    null,
    { headers }
  );
  totalRequests.add(1);

  const ok = check(res, {
    "bookmark 200": (r) => r.status === 200,
  });
  errorRate.add(!ok);
}

function recordViews() {
  // Batch view 5–10 posts at once (mimics real scroll behavior)
  const ids = [];
  const count = 5 + Math.floor(Math.random() * 6);
  for (let i = 0; i < count; i++) {
    const pid = getRandomPostId();
    if (pid) ids.push(pid);
  }
  if (ids.length === 0) return readFeed();

  const body = JSON.stringify({ postIds: ids });
  const res = http.post(`${BASE_URL}/api/v1/arena/posts/views`, body, {
    headers,
  });
  totalRequests.add(1);

  const ok = check(res, {
    "views 204": (r) => r.status === 204,
  });
  errorRate.add(!ok);
}

function getRandomPostId() {
  if (createdPostIDs.length === 0) return null;
  return createdPostIDs[Math.floor(Math.random() * createdPostIDs.length)];
}
