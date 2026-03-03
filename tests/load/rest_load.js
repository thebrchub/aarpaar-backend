// k6 REST API Load Test for aarpaar-backend
// Usage: k6 run tests/load/rest_load.js --env BASE_URL=http://localhost:2028
import http from "k6/http";
import { check, sleep, group } from "k6";
import { Rate, Counter, Trend } from "k6/metrics";

// ---------- Custom metrics ----------
const errorRate = new Rate("errors");
const reqDuration = new Trend("req_duration");
const requests = new Counter("total_requests");

// ---------- Options ----------
export const options = {
  stages: [
    { duration: "30s", target: 20 },  // ramp up
    { duration: "1m", target: 50 },   // sustained load
    { duration: "30s", target: 100 }, // peak
    { duration: "30s", target: 0 },   // ramp down
  ],
  thresholds: {
    http_req_duration: ["p(95)<500", "p(99)<1000"],
    errors: ["rate<0.05"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

// ---------- Setup: create test users and get tokens ----------
export function setup() {
  // In a real setup you'd seed users via Google auth or direct DB.
  // For this script, we assume the server is already seeded or use health check.
  return { baseUrl: BASE };
}

// ---------- Scenarios ----------
export default function (data) {
  const url = data.baseUrl;

  group("Health Check", function () {
    const res = http.get(`${url}/health`);
    check(res, { "health 200": (r) => r.status === 200 });
    errorRate.add(res.status !== 200);
    reqDuration.add(res.timings.duration);
    requests.add(1);
  });

  group("Online Count", function () {
    const res = http.get(`${url}/api/v1/online`);
    check(res, { "online 200": (r) => r.status === 200 });
    errorRate.add(res.status !== 200);
    reqDuration.add(res.timings.duration);
    requests.add(1);
  });

  sleep(0.1 + Math.random() * 0.2);
}
