// k6 Maximum throughput test — measures the absolute RPS ceiling
import http from "k6/http";
import { check } from "k6";
import { Rate } from "k6/metrics";

const errorRate = new Rate("errors");

export const options = {
  stages: [
    { duration: "10s", target: 1000 },
    { duration: "1m", target: 1000 },     // sustained — measure ceiling
    { duration: "10s", target: 0 },
  ],
  noConnectionReuse: false,
  batch: 2,
  thresholds: {
    http_req_duration: ["p(95)<500"],
    errors: ["rate<0.01"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

export default function () {
  // Batch both requests together for max throughput
  const responses = http.batch([
    ["GET", `${BASE}/health`],
    ["GET", `${BASE}/api/v1/online`],
  ]);

  for (const r of responses) {
    check(r, { "status 200": (res) => res.status === 200 });
    errorRate.add(r.status !== 200);
  }
  // Zero sleep — pure throughput
}
