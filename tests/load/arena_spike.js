// ---------------------------------------------------------------------------
// Arena Spike Test — Sudden Traffic Surge on Arena Endpoints
//
// Simulates a viral post scenario: traffic spikes from 50 → 5000 → 10000 VUs
// in seconds, then recovers. Tests the flusher backpressure, Redis buffer
// capacity, and Postgres connection pool under sudden load.
//
// Usage:
//   k6 run tests/load/arena_spike.js \
//     --env BASE_URL=https://your-app.railway.app \
//     --env AUTH_TOKEN=<jwt_access_token>
//
// Target: ~200K–500K requests in 8 minutes
// ---------------------------------------------------------------------------

import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const errorRate = new Rate("errors");
const spikeLatency = new Trend("spike_latency_ms");

const BASE_URL = __ENV.BASE_URL || "http://localhost:2028";
const AUTH_TOKEN = __ENV.AUTH_TOKEN || "";

const headers = {
  Authorization: `Bearer ${AUTH_TOKEN}`,
  "Content-Type": "application/json",
};

export const options = {
  stages: [
    { duration: "30s", target: 50 },    // warm up
    { duration: "10s", target: 5000 },   // spike!
    { duration: "1m", target: 5000 },    // sustain spike
    { duration: "10s", target: 10000 },  // double spike!
    { duration: "2m", target: 10000 },   // sustain peak
    { duration: "30s", target: 1000 },   // partial recovery
    { duration: "2m", target: 50 },      // cool down
    { duration: "30s", target: 0 },      // drain
  ],
  thresholds: {
    http_req_duration: ["p(95)<2000", "p(99)<5000"],
    errors: ["rate<0.15"], // 15% error budget during extreme spikes
    spike_latency_ms: ["p(95)<3000"],
  },
};

const postIDs = [];

export default function () {
  const rand = Math.random();

  if (rand < 0.50) {
    // 50% reads — the bulk of spike traffic is feed scrolling
    const res = http.get(
      `${BASE_URL}/api/v1/arena/feed/global?limit=20`,
      { headers }
    );
    check(res, { "feed ok": (r) => r.status === 200 });
    errorRate.add(res.status !== 200);
    spikeLatency.add(res.timings.duration);
  } else if (rand < 0.65) {
    // 15% views — users scrolling triggers view recording
    const ids = [];
    for (let i = 0; i < 10; i++) {
      if (postIDs.length > 0) {
        ids.push(postIDs[Math.floor(Math.random() * postIDs.length)]);
      }
    }
    if (ids.length > 0) {
      const res = http.post(
        `${BASE_URL}/api/v1/arena/posts/views`,
        JSON.stringify({ postIds: ids }),
        { headers }
      );
      errorRate.add(res.status !== 204);
      spikeLatency.add(res.timings.duration);
    }
  } else if (rand < 0.80) {
    // 15% likes — viral post attracts rapid likes
    if (postIDs.length > 0) {
      const pid = postIDs[Math.floor(Math.random() * postIDs.length)];
      const res = http.request(
        "POST",
        `${BASE_URL}/api/v1/arena/posts/${pid}/like`,
        null,
        { headers }
      );
      errorRate.add(res.status !== 200);
      spikeLatency.add(res.timings.duration);
    }
  } else if (rand < 0.90) {
    // 10% comments
    if (postIDs.length > 0) {
      const pid = postIDs[Math.floor(Math.random() * postIDs.length)];
      const res = http.post(
        `${BASE_URL}/api/v1/arena/posts/${pid}/comments`,
        JSON.stringify({ body: `Spike comment ${Date.now()}` }),
        { headers }
      );
      errorRate.add(res.status !== 200);
      spikeLatency.add(res.timings.duration);
    }
  } else {
    // 10% posts
    const res = http.post(
      `${BASE_URL}/api/v1/arena/posts`,
      JSON.stringify({ caption: `Spike post ${Date.now()}` }),
      { headers }
    );
    errorRate.add(res.status !== 200);
    spikeLatency.add(res.timings.duration);
    if (res.status === 200) {
      try {
        const data = JSON.parse(res.body);
        if (data.id) postIDs.push(data.id);
        if (postIDs.length > 200) postIDs.shift();
      } catch (_) {}
    }
  }

  sleep(Math.random() * 0.05); // 0–50ms think time (aggressive)
}
