// k6 Soak Test — sustained load over longer period
// Usage: k6 run tests/load/soak_test.js --env BASE_URL=http://localhost:2028
import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const errorRate = new Rate("errors");
const latency = new Trend("latency_ms");

export const options = {
  stages: [
    { duration: "1m", target: 30 },    // ramp up
    { duration: "10m", target: 30 },   // sustain
    { duration: "1m", target: 0 },     // ramp down
  ],
  thresholds: {
    http_req_duration: ["p(95)<500", "p(99)<1500"],
    errors: ["rate<0.02"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

export default function () {
  const res = http.get(`${BASE}/health`);
  check(res, { "status 200": (r) => r.status === 200 });
  errorRate.add(res.status !== 200);
  latency.add(res.timings.duration);

  // Also hit online count endpoint
  const res2 = http.get(`${BASE}/api/v1/online`);
  check(res2, { "online 200": (r) => r.status === 200 });
  errorRate.add(res2.status !== 200);
  latency.add(res2.timings.duration);

  sleep(0.5 + Math.random() * 0.5);
}
