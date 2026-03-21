// ---------------------------------------------------------------------------
// Arena Max RPS Test — Pure Throughput Ceiling
//
// Designed to hit 10L+ (1,000,000+) total requests as fast as possible.
// Uses constant-arrival-rate executor to guarantee exact request rate
// regardless of response time.
//
// Usage:
//   k6 run tests/load/arena_max_rps.js \
//     --env BASE_URL=https://your-app.railway.app \
//     --env AUTH_TOKEN=<jwt_access_token> \
//     --env TARGET_RPS=5000
//
// Default: 5000 RPS sustained for 4 minutes = 1,200,000 requests
// ---------------------------------------------------------------------------

import http from "k6/http";
import { check } from "k6";
import { Rate, Trend, Counter } from "k6/metrics";

const errorRate = new Rate("errors");
const latency = new Trend("req_latency_ms");
const totalReqs = new Counter("total_requests");

const BASE_URL = __ENV.BASE_URL || "http://localhost:2028";
const AUTH_TOKEN = __ENV.AUTH_TOKEN || "";
const TARGET_RPS = parseInt(__ENV.TARGET_RPS || "5000", 10);

const headers = {
  Authorization: `Bearer ${AUTH_TOKEN}`,
  "Content-Type": "application/json",
};

export const options = {
  scenarios: {
    arena_throughput: {
      executor: "constant-arrival-rate",
      rate: TARGET_RPS,
      timeUnit: "1s",
      duration: "4m",
      preAllocatedVUs: Math.min(TARGET_RPS, 5000),
      maxVUs: Math.min(TARGET_RPS * 2, 10000),
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<1000", "p(99)<3000"],
    errors: ["rate<0.05"],
    total_requests: [`count>=${TARGET_RPS * 240 * 0.9}`], // 90% of theoretical max
  },
  noConnectionReuse: false,
  batch: 4,
};

const postIDs = [];

export default function () {
  const rand = Math.random();

  if (rand < 0.50) {
    // 50% feed reads (fast, cacheable)
    const res = http.get(
      `${BASE_URL}/api/v1/arena/feed/global?limit=10`,
      { headers }
    );
    totalReqs.add(1);
    errorRate.add(res.status !== 200);
    latency.add(res.timings.duration);
  } else if (rand < 0.65) {
    // 15% views (very fast — Redis SADD pipeline)
    const ids = [];
    for (let i = 0; i < 5; i++) {
      if (postIDs.length > 0) {
        ids.push(postIDs[Math.floor(Math.random() * postIDs.length)]);
      } else {
        ids.push(i + 1); // dummy IDs during warmup
      }
    }
    const res = http.post(
      `${BASE_URL}/api/v1/arena/posts/views`,
      JSON.stringify({ postIds: ids }),
      { headers }
    );
    totalReqs.add(1);
    errorRate.add(res.status !== 204);
    latency.add(res.timings.duration);
  } else if (rand < 0.80) {
    // 15% likes (fast — Redis SADD)
    if (postIDs.length > 0) {
      const pid = postIDs[Math.floor(Math.random() * postIDs.length)];
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
  } else if (rand < 0.90) {
    // 10% post creation
    const res = http.post(
      `${BASE_URL}/api/v1/arena/posts`,
      JSON.stringify({ caption: `Max RPS ${Date.now()}-${__VU}` }),
      { headers }
    );
    totalReqs.add(1);
    errorRate.add(res.status !== 200);
    latency.add(res.timings.duration);
    if (res.status === 200) {
      try {
        const data = JSON.parse(res.body);
        if (data.id) postIDs.push(data.id);
        if (postIDs.length > 500) postIDs.shift();
      } catch (_) {}
    }
  } else {
    // 10% trending feed
    const res = http.get(
      `${BASE_URL}/api/v1/arena/feed/trending?limit=10`,
      { headers }
    );
    totalReqs.add(1);
    errorRate.add(res.status !== 200);
    latency.add(res.timings.duration);
  }
}
