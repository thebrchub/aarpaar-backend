// k6 WebSocket Load Test for aarpaar-backend
// Usage: k6 run tests/load/ws_load.js --env WS_URL=ws://localhost:2028 --env AUTH_TOKEN=<jwt>
import ws from "k6/ws";
import { check, sleep } from "k6";
import { Rate, Trend } from "k6/metrics";

const errorRate = new Rate("ws_errors");
const msgLatency = new Trend("ws_msg_latency");

export const options = {
  stages: [
    { duration: "20s", target: 10 },
    { duration: "40s", target: 50 },
    { duration: "20s", target: 0 },
  ],
  thresholds: {
    ws_errors: ["rate<0.05"],
    ws_msg_latency: ["p(95)<200"],
  },
};

const WS_URL = __ENV.WS_URL || "ws://localhost:2028";
const TOKEN = __ENV.AUTH_TOKEN || "";

export default function () {
  const url = `${WS_URL}/ws`;
  const params = { headers: { Authorization: `Bearer ${TOKEN}` } };

  const res = ws.connect(url, params, function (socket) {
    socket.on("open", function () {
      // Send a heartbeat/ping
      const start = Date.now();
      socket.send(JSON.stringify({ type: "ping" }));

      socket.on("message", function (msg) {
        msgLatency.add(Date.now() - start);
        try {
          const data = JSON.parse(msg);
          check(data, {
            "has type": (d) => d.type !== undefined,
          });
        } catch (e) {
          // Binary or non-JSON message
        }
      });
    });

    socket.on("error", function (e) {
      errorRate.add(1);
    });

    // Keep connection open for a bit
    socket.setTimeout(function () {
      socket.close();
    }, 5000);
  });

  check(res, { "ws connected": (r) => r && r.status === 101 });
  sleep(1);
}
