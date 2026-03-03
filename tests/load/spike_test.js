// k6 Spike Test — sudden load surge
// Usage: k6 run tests/load/spike_test.js --env BASE_URL=http://localhost:2028
import http from "k6/http";
import { check, sleep } from "k6";
import { Rate } from "k6/metrics";

const errorRate = new Rate("errors");

export const options = {
  stages: [
    { duration: "10s", target: 10 },   // warm up
    { duration: "5s", target: 200 },   // spike
    { duration: "30s", target: 200 },  // sustain spike
    { duration: "10s", target: 10 },   // recover
    { duration: "20s", target: 0 },    // ramp down
  ],
  thresholds: {
    http_req_duration: ["p(99)<2000"],
    errors: ["rate<0.15"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";

export default function () {
  const res = http.get(`${BASE}/health`);
  check(res, { "status 200": (r) => r.status === 200 });
  errorRate.add(res.status !== 200);
  sleep(0.05);
}
