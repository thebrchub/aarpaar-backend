// k6 WebSocket 10K Connection Load Test
//
// Tests 10K+ concurrent WebSocket connections with heartbeat keep-alive.
// Each VU opens a connection, sends periodic heartbeats, and holds it open.
//
// Prerequisites:
//   - Server running on BASE_URL (default: ws://localhost:2028)
//   - AUTH_TOKEN env var with a valid JWT (all VUs share the same token for
//     simplicity — the server allows multi-device, so this simulates
//     one user with many devices or use TOKENS_FILE for per-VU tokens)
//
// Usage:
//   k6 run tests/load/ws_10k.js --env AUTH_TOKEN=<jwt>
//   k6 run tests/load/ws_10k.js --env AUTH_TOKEN=<jwt> --env TARGET_VUS=5000
//
import ws from "k6/ws";
import { check, sleep } from "k6";
import { Rate, Counter, Trend, Gauge } from "k6/metrics";

// ---------- Custom Metrics ----------
const wsErrors = new Rate("ws_errors");
const wsConnected = new Counter("ws_connections_opened");
const wsMessages = new Counter("ws_messages_received");
const connDuration = new Trend("ws_conn_duration_ms");
const activeConns = new Gauge("ws_active_connections");

// ---------- Configuration ----------
const TARGET = parseInt(__ENV.TARGET_VUS || "10000");
const RAMP_TIME = __ENV.RAMP_TIME || "2m";
const HOLD_TIME = __ENV.HOLD_TIME || "5m";
const RAMP_DOWN = __ENV.RAMP_DOWN || "1m";
const CONN_LIFETIME_S = parseInt(__ENV.CONN_LIFETIME_S || "120");

export const options = {
  scenarios: {
    ws_connections: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: RAMP_TIME, target: TARGET },
        { duration: HOLD_TIME, target: TARGET },
        { duration: RAMP_DOWN, target: 0 },
      ],
      gracefulRampDown: "30s",
      exec: "wsConnection",
    },
  },
  thresholds: {
    ws_errors: ["rate<0.10"],
    ws_conn_duration_ms: ["p(95)<300000"],
  },
  // System-level tuning for 10K connections
  noConnectionReuse: true,
  batch: 100,
};

const BASE = (__ENV.BASE_URL || "http://localhost:2028").replace("http", "ws");
const TOKEN = __ENV.AUTH_TOKEN || "";

export function wsConnection() {
  const url = `${BASE}/ws`;
  const params = {
    headers: { Authorization: `Bearer ${TOKEN}` },
  };

  const startTime = Date.now();
  let msgCount = 0;

  const res = ws.connect(url, params, function (socket) {
    wsConnected.add(1);
    activeConns.add(1);

    // Send heartbeats every 30s to keep the connection alive
    const heartbeatInterval = setInterval(function () {
      socket.send(JSON.stringify({ type: "heartbeat" }));
    }, 30000);

    socket.on("message", function (data) {
      msgCount++;
      wsMessages.add(1);
    });

    socket.on("error", function (e) {
      wsErrors.add(1);
    });

    socket.on("close", function () {
      activeConns.add(-1);
      clearInterval(heartbeatInterval);
    });

    // Hold connection open for CONN_LIFETIME_S seconds
    socket.setTimeout(function () {
      socket.close();
    }, CONN_LIFETIME_S * 1000);
  });

  const duration = Date.now() - startTime;
  connDuration.add(duration);

  const connected = res && res.status === 101;
  check(res, { "ws connected": (r) => r && r.status === 101 });
  if (!connected) {
    wsErrors.add(1);
  }

  // Small stagger between VU iterations to avoid thundering herd on reconnect
  sleep(1 + Math.random() * 2);
}
