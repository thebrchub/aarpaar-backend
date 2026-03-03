// k6 Mixed Load Test — REST + WebSocket combined
// Usage: k6 run tests/load/mixed_load.js --env BASE_URL=http://localhost:2028
import http from "k6/http";
import ws from "k6/ws";
import { check, sleep, group } from "k6";
import { Rate, Counter } from "k6/metrics";

const errorRate = new Rate("errors");
const apiCalls = new Counter("api_calls");

export const options = {
  scenarios: {
    rest_traffic: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 30 },
        { duration: "1m", target: 60 },
        { duration: "30s", target: 0 },
      ],
      exec: "restScenario",
    },
    ws_traffic: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 10 },
        { duration: "1m", target: 20 },
        { duration: "30s", target: 0 },
      ],
      exec: "wsScenario",
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<800"],
    errors: ["rate<0.1"],
  },
};

const BASE = __ENV.BASE_URL || "http://localhost:2028";
const WS_BASE = BASE.replace("http", "ws");
const TOKEN = __ENV.AUTH_TOKEN || "";

export function restScenario() {
  group("Health + Online", function () {
    const res = http.get(`${BASE}/health`);
    check(res, { "status 200": (r) => r.status === 200 });
    errorRate.add(res.status !== 200);
    apiCalls.add(1);
  });

  group("Online Count", function () {
    const res = http.get(`${BASE}/api/v1/online`);
    check(res, { "online ok": (r) => r.status === 200 });
    errorRate.add(res.status !== 200);
    apiCalls.add(1);
  });

  sleep(0.2 + Math.random() * 0.3);
}

export function wsScenario() {
  const url = `${WS_BASE}/ws`;
  const params = { headers: { Authorization: `Bearer ${TOKEN}` } };

  const res = ws.connect(url, params, function (socket) {
    socket.on("open", function () {
      socket.send(JSON.stringify({ type: "ping" }));
    });

    socket.setTimeout(function () {
      socket.close();
    }, 3000);
  });

  check(res, { "ws connected": (r) => r && r.status === 101 });
  errorRate.add(!res || res.status !== 101);
  sleep(1);
}
