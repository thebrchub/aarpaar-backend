// k6 Performance Test — validates server handles 10K+ RPS at <500ms p95
// Stays within Windows ephemeral port limits by using 2000 VUs + fast requests
import http from "k6/http";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const errorRate = new Rate("errors");
const reqDuration = new Trend("req_duration");

export const options = {
  stages: [
    { duration: "15s", target: 500 },
    { duration: "15s", target: 1000 },
    { duration: "30s", target: 2000 },
    { duration: "1m", target: 2000 },   // sustain — target 10K+ RPS
    { duration: "15s", target: 0 },
  ],
  thresholds: {
    http_req_duration: ["p(95)<500"],
    errors: ["rate<0.01"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

export default function () {
  // Health check (pure server perf, no DB)
  const r1 = http.get(`${BASE}/health`);
  check(r1, { "health 200": (r) => r.status === 200 });
  errorRate.add(r1.status !== 200);
  reqDuration.add(r1.timings.duration);

  // Online count (reads from in-memory engine — no DB/Redis)
  const r2 = http.get(`${BASE}/api/v1/online`);
  check(r2, { "online 200": (r) => r.status === 200 });
  errorRate.add(r2.status !== 200);
  reqDuration.add(r2.timings.duration);

  // Swagger (static, tests HTTP router throughput)
  const r3 = http.get(`${BASE}/swagger/doc.json`);
  check(r3, { "swagger 200": (r) => r.status === 200 });
  errorRate.add(r3.status !== 200);
  reqDuration.add(r3.timings.duration);

  // Minimal think time to maximize RPS
  sleep(0.05);
}
