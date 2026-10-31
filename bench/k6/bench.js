import http from 'k6/http';
import grpc from 'k6/net/grpc';
import { sleep } from 'k6';
import { Counter } from 'k6/metrics';
import { hmac } from 'k6/crypto';
import encoding from 'k6/encoding';

const PROTOCOL = __ENV.PROTOCOL || 'grpc';
const OP = __ENV.OP || 'get';
const VUS = parseInt(__ENV.VUS || '16', 10);
const REQUESTS = parseInt(__ENV.REQUESTS || '20000', 10);
const USERS = parseInt(__ENV.USERS || '1000', 10);
const PER_USER = parseInt(__ENV.PER_USER || '100', 10);
const LABEL = __ENV.LABEL || 'cache=on';
const OUT = __ENV.OUT || '';
const HTTP_ADDR = __ENV.HTTP_ADDR || 'http://task-service:8080';
const GRPC_ADDR = __ENV.GRPC_ADDR || 'task-service:9090';
const SECRET = __ENV.JWT_SECRET || 'change-me-dev-secret';

const ATTEMPT_TIMEOUT = '300ms';
const MAX_ATTEMPTS = 3;
const INITIAL_BACKOFF_S = 0.02;
const MAX_BACKOFF_S = 0.2;
const DESCRIPTION = 'Lorem ipsum dolor sit amet. '.repeat(4);

const callErrors = new Counter('call_errors');
const callRetries = new Counter('call_retries');

export const options = {
  scenarios: {
    load: { executor: 'shared-iterations', vus: Math.min(VUS, REQUESTS), iterations: REQUESTS, maxDuration: '30m' },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

const client = new grpc.Client();
client.load(['/proto'], 'tasktracker/v1/task.proto');
let connected = false;

function jwt(userID) {
  const now = Math.floor(Date.now() / 1000);
  const header = encoding.b64encode(JSON.stringify({ alg: 'HS256', typ: 'JWT' }), 'rawurl');
  const payload = encoding.b64encode(JSON.stringify({ sub: String(userID), iat: now, exp: now + 7200 }), 'rawurl');
  const signature = hmac('sha256', SECRET, `${header}.${payload}`, 'base64rawurl');
  return `${header}.${payload}.${signature}`;
}

export function setup() {
  if (PROTOCOL !== 'rest') {
    return { tokens: [] };
  }
  const tokens = [''];
  for (let u = 1; u <= USERS; u++) {
    tokens.push(jwt(u));
  }
  return { tokens };
}

function pick() {
  const user = 1 + Math.floor(Math.random() * USERS);
  const taskID = (user - 1) * PER_USER + 1 + Math.floor(Math.random() * PER_USER);
  return { user, taskID };
}

function backoff(attempt) {
  const base = Math.min(INITIAL_BACKOFF_S * 2 ** (attempt - 1), MAX_BACKOFF_S);
  sleep(base * (0.8 + 0.4 * Math.random()));
}

function restCall(token, taskID, key) {
  const params = { headers: { Authorization: `Bearer ${token}` }, timeout: ATTEMPT_TIMEOUT };
  switch (OP) {
    case 'get':
      return http.get(`${HTTP_ADDR}/v1/tasks/${taskID}`, params);
    case 'list':
      return http.get(`${HTTP_ADDR}/v1/me/tasks?sort=priority&limit=50`, params);
    case 'create':
      params.headers['Content-Type'] = 'application/json';
      params.headers['Idempotency-Key'] = key;
      return http.post(`${HTTP_ADDR}/v1/tasks`, JSON.stringify({
        title: 'Benchmark task',
        description: DESCRIPTION,
        priority: Math.floor(Math.random() * 4),
      }), params);
    default:
      throw new Error(`unknown OP ${OP}`);
  }
}

const REQUEST_TIMEOUT_CODE = 1050;

function grpcAttempt(user, taskID, key) {
  if (!connected) {
    client.connect(GRPC_ADDR, { plaintext: true });
    connected = true;
  }
  const params = { metadata: { 'x-user-id': String(user) }, timeout: ATTEMPT_TIMEOUT, discardResponseMessage: true };
  let res;
  if (OP === 'get') {
    res = client.invoke('tasktracker.v1.TaskService/GetTask', { id: taskID }, params);
  } else if (OP === 'list') {
    res = client.invoke('tasktracker.v1.TaskService/ListTasks',
      { scope: 'SCOPE_PERSONAL', sort_by: 'SORT_BY_PRIORITY', page_size: 50 }, params);
  } else {
    res = client.invoke('tasktracker.v1.TaskService/CreateTask', {
      title: 'Benchmark task',
      description: DESCRIPTION,
      priority: Math.floor(Math.random() * 4),
      idempotency_key: key,
    }, params);
  }
  if (res.status === grpc.StatusOK) {
    return 'ok';
  }
  return res.status === grpc.StatusUnavailable ? 'retry' : 'fail';
}

function restAttempt(token, taskID, key) {
  const res = restCall(token, taskID, key);
  if (res.status >= 200 && res.status < 300) {
    return 'ok';
  }
  if (res.status === 503) {
    return 'retry';
  }
  if (res.status === 0 && res.error_code !== REQUEST_TIMEOUT_CODE) {
    return 'retry';
  }
  return 'fail';
}

export default function (data) {
  const { user, taskID } = pick();
  const key = crypto.randomUUID();
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    if (attempt > 1) {
      callRetries.add(1);
      backoff(attempt - 1);
    }
    const result = PROTOCOL === 'rest'
      ? restAttempt(data.tokens[user], taskID, key)
      : grpcAttempt(user, taskID, key);
    if (result === 'ok') {
      return;
    }
    if (result === 'fail') {
      break;
    }
  }
  callErrors.add(1);
}

function metric(data, name, stat) {
  const m = data.metrics[name];
  return m && m.values[stat] !== undefined ? m.values[stat] : 0;
}

export function handleSummary(data) {
  const latency = 'iteration_duration';
  const requests = metric(data, 'iterations', 'count');
  const fixed = (v) => v.toFixed(3);
  const perRequest = (v) => (requests > 0 ? Math.round(v / requests) : 0);
  const row = [
    PROTOCOL, OP, VUS, LABEL, requests,
    metric(data, 'call_errors', 'count'),
    metric(data, 'call_retries', 'count'),
    fixed(metric(data, latency, 'med')),
    fixed(metric(data, latency, 'p(95)')),
    fixed(metric(data, latency, 'p(99)')),
    fixed(metric(data, latency, 'max')),
    Math.round((Math.min(VUS, REQUESTS) * 1000) / metric(data, latency, 'avg')),
    perRequest(metric(data, 'data_sent', 'count')),
    perRequest(metric(data, 'data_received', 'count')),
  ];
  const line = `${PROTOCOL} ${OP} c=${VUS} ${LABEL}  p50=${row[7]}ms p95=${row[8]}ms p99=${row[9]}ms `
    + `max=${row[10]}ms  ${row[11]} req/s  sent=${row[12]}B/req recv=${row[13]}B/req  `
    + `errors=${row[5]} retries=${row[6]}\n`;
  const out = { stdout: line };
  if (OUT) {
    out[OUT] = row.join(',') + '\n';
  }
  return out;
}
