// ---------------------------------------------------------------------------
// Arena Soak Test — Sustained Load Over Extended Period
//
// Runs 1000 VUs for 30 minutes to detect memory leaks, connection pool
// exhaustion, Redis buffer growth, and goroutine leaks in the flusher.
//
// Usage:
//   k6 run tests/load/arena_soak.js \
//     --env BASE_URL=https://your-app.railway.app \
//     --env AUTH_TOKEN=<jwt_access_token>
//
// Target: 1M+ requests over 35 minutes
// ---------------------------------------------------------------------------

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";

const errorRate = new Rate("errors");
const latency = new Trend("soak_latency_ms");
const totalReqs = new Counter("total_requests");

const BASE_URL = __ENV.BASE_URL || "http://localhost:2028";
const AUTH_TOKEN = __ENV.AUTH_TOKEN || "";

const headers = {
  Authorization: `Bearer ${AUTH_TOKEN}`,
  "Content-Type": "application/json",
};

export const options = {
  stages: [
    { duration: "2m", target: 500 },     // ramp up
    { duration: "2m", target: 1000 },    // reach cruise
    { duration: "30m", target: 1000 },   // sustained load
    { duration: "1m", target: 0 },       // ramp down
  ],
  thresholds: {
    http_req_duration: ["p(95)<500", "p(99)<1500"],
    errors: ["rate<0.02"], // strict: < 2% errors over soak
    soak_latency_ms: ["p(95)<500"],
  },
};

const postIDs = [];

export default function () {
  const rand = Math.random();

  if (rand < 0.45) {
    // 45% feed reads
    const feeds = [
      "/api/v1/arena/feed/global?limit=20",
      "/api/v1/arena/feed/trending?limit=10",
    ];
    const url = feeds[Math.floor(Math.random() * feeds.length)];
    const res = http.get(`${BASE_URL}${url}`, { headers });
    totalReqs.add(1);
    errorRate.add(res.status !== 200);
    latency.add(res.timings.duration);
  } else if (rand < 0.55) {
    // 10% post creation
    const res = http.post(
      `${BASE_URL}/api/v1/arena/posts`,
      JSON.stringify({ caption: `Soak ${Date.now()}-${__VU}` }),
      { headers }
    );
    totalReqs.add(1);
    errorRate.add(res.status !== 200);
    latency.add(res.timings.duration);
    if (res.status === 200) {
      try {
        const data = JSON.parse(res.body);
        if (data.id) postIDs.push(data.id);
        if (postIDs.length > 300) postIDs.shift();
      } catch (_) {}
    }
  } else if (rand < 0.65) {
    // 10% likes
    const pid = pickPost();
    if (pid) {
      const res = http.request(
        "POST",
        `${BASE_URL}/api/v1/arena/posts/${pid}/like`,
        null,
        { headers }
      );
      totalReqs.add(1);
      errorRate.add(res.status !== 200);
      latency.add(res.timings.duration);
    }
  } else if (rand < 0.75) {
    // 10% comments
    const pid = pickPost();
    if (pid) {
      const res = http.post(
        `${BASE_URL}/api/v1/arena/posts/${pid}/comments`,
        JSON.stringify({ body: `Soak comment ${Date.now()}` }),
        { headers }
      );
      totalReqs.add(1);
      errorRate.add(res.status !== 200);
      latency.add(res.timings.duration);
    }
  } else if (rand < 0.85) {
    // 10% views
    const ids = [];
    for (let i = 0; i < 8; i++) {
      const pid = pickPost();
      if (pid) ids.push(pid);
    }
    if (ids.length > 0) {
      const res = http.post(
        `${BASE_URL}/api/v1/arena/posts/views`,
        JSON.stringify({ postIds: ids }),
        { headers }
      );
      totalReqs.add(1);
      errorRate.add(res.status !== 204);
      latency.add(res.timings.duration);
    }
  } else if (rand < 0.92) {
    // 7% bookmarks
    const pid = pickPost();
    if (pid) {
      const res = http.request(
        "POST",
        `${BASE_URL}/api/v1/arena/posts/${pid}/bookmark`,
        null,
        { headers }
      );
      totalReqs.add(1);
      errorRate.add(res.status !== 200);
      latency.add(res.timings.duration);
    }
  } else if (rand < 0.96) {
    // 4% get single post
    const pid = pickPost();
    if (pid) {
      const res = http.get(
        `${BASE_URL}/api/v1/arena/posts/${pid}`,
        { headers }
      );
      totalReqs.add(1);
      errorRate.add(res.status !== 200);
      latency.add(res.timings.duration);
    }
  } else {
    // 4% post activity (owner analytics)
    const pid = pickPost();
    if (pid) {
      const res = http.get(
        `${BASE_URL}/api/v1/arena/posts/${pid}/activity`,
        { headers }
      );
      totalReqs.add(1);
      // activity returns 404 if not owner — that's expected
      errorRate.add(res.status !== 200 && res.status !== 404);
      latency.add(res.timings.duration);
    }
  }

  sleep(0.3 + Math.random() * 0.7); // 300–1000ms think time (realistic)
}

function pickPost() {
  if (postIDs.length === 0) return null;
  return postIDs[Math.floor(Math.random() * postIDs.length)];
}
