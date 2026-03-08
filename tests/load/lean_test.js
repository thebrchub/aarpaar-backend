// k6 Lean API test — pure API perf without large static files
import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const errorRate = new Rate("errors");

export const options = {
  stages: [
    { duration: "10s", target: 500 },
    { duration: "10s", target: 1000 },
    { duration: "20s", target: 2000 },
    { duration: "1m30s", target: 2000 },   // sustained peak
    { duration: "10s", target: 0 },
  ],
  thresholds: {
    http_req_duration: ["p(95)<300"],
    errors: ["rate<0.01"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

export default function () {
  // Health — pure server perf
  const r1 = http.get(`${BASE}/health`);
  check(r1, { "health 200": (r) => r.status === 200 });
  errorRate.add(r1.status !== 200);

  // Online count — reads from in-memory engine
  const r2 = http.get(`${BASE}/api/v1/online`);
  check(r2, { "online 200": (r) => r.status === 200 });
  errorRate.add(r2.status !== 200);
}
