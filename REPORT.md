# Report: protocol, resilience, measurements

Details behind the [README](README.md): protocol rationale, idempotency, timeouts and retries,
benchmark methodology and results, tests.

## Why gRPC (summary of [ADR 0001](docs/adr/0001-protocol.md))

- **gRPC (HTTP/2 + protobuf) between services:** the p99 < 100 ms budget is split across several hops;
  protobuf is smaller and faster than JSON; HTTP/2 multiplexes requests over one connection.
- **Deadlines** are built into the protocol and flow down the call chain; **retries** are declared
  in the service config; the **strict `.proto` contract** comes with code generation.
- The public API is **REST/JSON** (browsers, mobile clients); it is also the baseline.
- **Not WebSocket:** there is no real-time push requirement; it is a future candidate (live group updates).
- **Trade-offs:** gRPC does not work from a browser without gRPC-Web, binary traffic is harder to debug
  (`grpcurl`), HTTP/2 needs L7 load balancing (long-lived connections).

Code generation — `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (`make gen`). Generated code is
committed to `gen/`, so the project builds without `protoc`.

## Idempotency, timeouts, retries

### Idempotency

| Operation | Mechanism |
| --- | --- |
| CreateTask | `idempotency_key` (gRPC) / `Idempotency-Key` (REST), a UUID, required. `INSERT ... ON CONFLICT (author_id, client_request_id) DO NOTHING RETURNING *`; if nothing was inserted, `SELECT` the existing task and return it (REST: 200 instead of 201). The same key with a different body → INVALID_ARGUMENT / 422 (compares the body's sha256 with the `request_hash` column) |
| UpdateTask | Optimistic locking: `UPDATE ... WHERE id = ? AND version = ?`. Repeating an already applied request gets ABORTED / 412, and the client re-reads the task. That is why it is **never retried automatically** |
| DeleteTask | Soft delete; deleting again → success (204 / Empty) |
| JoinByInvite | Already a member → success without using up the link |
| GetTask, ListTasks, CheckMembership | Read-only, idempotent by nature |

### Timeouts (request budget)

| Hop | Timeout | Where it is set |
| --- | --- | --- |
| External client → task-service | 300 ms per attempt (gRPC deadline / HTTP client); the REST server sets 300 ms itself, in gRPC the client sets the deadline | `bench/k6/bench.js`, `httpserver.RequestTimeout` |
| task-service → group-service `CheckMembership` | 50 ms, but never more than what is left of the incoming deadline; retries happen inside it | `groupclient.MembershipTimeout` |
| task-service → Redis | 20 ms per command; no answer means a miss and a database read | `cache.commandTimeout` |
| task-service → PostgreSQL | `statement_timeout` 80 ms + an 80 ms context per query; an expired `ctx` means the query never starts | `repository/task`: `NewPool`, `begin` |
| `http.Server` | `ReadHeaderTimeout` 2 s, `Read/WriteTimeout` 5 s, `IdleTimeout` 90 s | `httpserver.NewServer` |

Orders of magnitude: normally each hop takes a few milliseconds; the timeouts are ceilings for
failures, and on the worst path they add up to more than 100 ms (50 + 20 + 80). This is deliberate:
p99 < 100 ms is about normal operation, while the timeouts stop one stuck hop from holding a request
for up to 300 ms.

### Retries

gRPC service config ([`internal/client/groupclient/retry.go`](internal/client/groupclient/retry.go)) — used by the
task-service → group-service client:

| Parameter | Value |
| --- | --- |
| Methods | `GroupService/CheckMembership`, `TaskService/{GetTask, ListTasks, CreateTask, DeleteTask}` |
| `maxAttempts` | 3 |
| Backoff | 20 ms → ×2 → at most 200 ms; jitter ±20% (built into grpc-go) |
| Codes | `UNAVAILABLE` only |

- **Only idempotent** operations are retried; CreateTask qualifies thanks to its idempotency key.
  UpdateTask and JoinByInvite are not retried automatically.
- `DEADLINE_EXCEEDED` is **not retried**: the time is already spent.
- Load test: the k6 gRPC client does not support a service config, so `bench/k6/bench.js` implements the
  same rules by hand for both protocols (3 attempts, the same backoff with jitter; gRPC retries only
  `UNAVAILABLE`, REST only 503 and network errors other than a timeout; the same idempotency key on
  every attempt).
- **Retries live inside the deadline.** grpc-go pauses of ≈16–24 ms and ≈32–48 ms add up to ≥ 48 ms, so
  only one retry fits into the 50 ms CheckMembership deadline; all three attempts only happen for calls
  with a long deadline (300 ms for the external client). This is measured by
  `TestCheckMembershipRetryBudgetLimitedByDeadline` and `TestRetryPolicyMaxAttempts`.

## Measurement results

### Methodology

- One machine: AMD Ryzen 5 4600H (6 cores / 12 threads), 19 GB RAM, Windows 11, Docker Desktop
  (WSL 2, 12 CPUs and ~9.4 GB RAM for Docker). PostgreSQL 16, Redis 7 and the services run in
  docker-compose, the bench client runs on the host.
- Seed: `cmd/seed`, 1,000 users × 100 tasks (100,000 rows); the data is re-seeded and Redis is
  flushed before each cache mode.
- Every combination: 2,000 warm-up requests (not measured), then 20,000 measured ones.
- Matrix: protocol {grpc, rest} × op {get, list (first page, 50 tasks), create} ×
  concurrency {1, 16, 64}; a separate run with `CACHE_ENABLED=false`.
- **REST runs over HTTP/1.1** (`net/http`, keep-alive), **gRPC over HTTP/2**: this compares "typical
  REST" with gRPC. The REST client uses `MaxIdleConnsPerHost = 2 × concurrency`, so at concurrency 64
  it measures requests rather than TCP connection setup.
- Latency is the full call time on the client, retries included (HdrHistogram). Sizes are message
  bodies: protobuf `proto.Size`, JSON body length; HTTP/1.1 headers (including the ~150-byte JWT in every
  REST request) and HTTP/2 HPACK headers are not counted.
- Run: `./bench/run-all.sh` (≈ 50 minutes); output — `bench/results/results.csv` and
  [`bench/results/results.md`](bench/results/results.md).

> **Note:** these numbers were taken while gRPC still verified a JWT in an interceptor (like REST).
> gRPC now takes the user from `x-user-id` without checking a token, so the benchmark has to be rerun
> for the current code: `./bench/run-all.sh`.

### Key table (cache on)

Latency in ms, req/s is throughput, resp B is the average response body size.

| op | conc | gRPC p50 | REST p50 | gRPC p99 | REST p99 | gRPC req/s | REST req/s | gRPC resp B | REST resp B | gRPC / REST +gzip B |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| get | 1 | 2.70 | 2.22 | 4.69 | 4.54 | 363 | 458 | 175 | 335 | 107 / 200 |
| get | 16 | 3.57 | 3.68 | 10.74 | 8.49 | 4,227 | 4,138 | 175 | 335 | 108 / 200 |
| get | 64 | 7.39 | 9.81 | 16.78 | 22.59 | 8,151 | 6,142 | 175 | 335 | 107 / 200 |
| list | 1 | 2.31 | 1.77 | 4.18 | 3.62 | 398 | 543 | 8,959 | 16,838 | 576 / 702 |
| list | 16 | 3.70 | 5.03 | 45.98 | 10.93 | 3,187 | 3,015 | 8,960 | 16,838 | 577 / 702 |
| list | 64 | 9.26 | 15.04 | 19.62 | 34.21 | 6,484 | 4,075 | 8,960 | 16,839 | 576 / 702 |
| create | 1 | 5.24 | 4.98 | 11.79 | 10.01 | 183 | 197 | 169 | 329 | 102 / 198 |
| create | 16 | 8.01 | 8.17 | 18.10 | 13.26 | 1,970 | 1,975 | 169 | 329 | 102 / 197 |
| create | 64 | 12.97 | 15.90 | 24.05 | 25.98 | 4,680 | 3,916 | 169 | 329 | 103 / 198 |

Request size: get — 3 B (protobuf) versus an empty GET body; create — 169 B versus 168 B of JSON.
There were no errors and no retries with the cache on. With the cache off there were none either,
except for one combination: REST create at concurrency 1, where 19 of 20,000 requests (0.1%) got a 504.
That is the 300 ms server deadline (max 703 ms); by the rules a 504 is not retried. The cause of the
stall (probably on the PostgreSQL side) was not investigated.

### Conclusions

- **Size:** a protobuf response is ~1.9× smaller than JSON (a task is 175 vs 335 B, a list of 50 tasks
  8,960 vs 16,838 B). With gzip the gap narrows (576 vs ~700 B per list), but gzip costs CPU on every
  response.
- **gRPC is faster under load:** at concurrency 64 gRPC delivers 1.2–1.6× more req/s and a lower p50 on
  every operation. At concurrency 1 REST over HTTP/1.1 is slightly faster (REST p50 is 0.8–0.95× of gRPC):
  on one machine with no network latency, HTTP/2 overhead outweighs the size advantage.
- **gRPC tails:** at concurrency 16 gRPC p99 is ≈ 43–46 ms for get (cache off) and list, versus 7–12 ms
  for REST, and max at concurrency 1 is 47–68 ms. These are rare stalls of about 40–50 ms on the gRPC path,
  present in both cache modes. They are an artifact of Docker Desktop port forwarding (see the experiment
  below), not of gRPC itself. Every p99 in both modes is still under 50 ms, so the p99 < 100 ms SLO
  holds on this machine at ~2,000–12,000 req/s.
- **The cache did not speed up reads here:** get at concurrency 1 has p50 2.70 ms with the cache versus
  1.74 ms without. A hit costs 2–3 Redis round trips, while the tasks table fits in PostgreSQL memory
  (even after all runs, 232k rows take 104 MB with indexes against `shared_buffers` of 128 MB), and the
  "replica" is the same container. The cache pays off when the data does not fit in database memory and
  a real primary/replicas need offloading; this run did not measure that.

### Experiment: client inside the docker network

A separate run after the main one: the same 1,000 × 100 seed, cache on, gRPC already without a JWT
(`x-user-id`). The bench ran in a container on the docker-compose network, with no port forwarding to the
host. The "on the host" row was taken within the same minutes for comparison.

| op | conc | client location | gRPC p50 | REST p50 | gRPC p99 | REST p99 | gRPC max | REST max |
|---|---:|---|---:|---:|---:|---:|---:|---:|
| get | 1 | on the host (port forwarding) | 2.59 | 2.12 | 4.67 | 4.13 | 54.30 | 7.87 |
| get | 1 | inside the docker network | 1.59 | 1.47 | 2.63 | 2.35 | 3.65 | 3.11 |
| list | 1 | inside the docker network | 1.24 | 1.18 | 2.59 | 1.97 | 3.69 | 2.70 |
| get | 16 | inside the docker network | 2.45 | 2.35 | 4.76 | 5.17 | 14.92 | 8.72 |

- The 40–50 ms gRPC outliers disappear without port forwarding: max drops from 54 to 3.7 ms. They come from
  the Docker Desktop proxy on the host → container path; gRPC is more sensitive because all of its
  traffic goes over a single HTTP/2 connection. Why the proxy produces such pauses was not investigated.
- The remaining gap at concurrency 1 is 5–8% in favour of REST. The most likely reason is gRPC's per-call
  overhead (HTTP/2 frames, HPACK, flow control) versus HTTP/1.1 keep-alive; this was not confirmed by
  profiling. Without network latency, protobuf's size advantage does not affect response time.
- The full matrix was not run inside the docker network: the main methodology keeps the client on the host.

## Tests

`go test -race ./...` — integration tests start PostgreSQL and Redis with testcontainers (Docker required).

| Test | What it checks |
| --- | --- |
| `task.TestCreateTaskIdempotent` | the same key twice → one row in the database, identical response |
| `task.TestCreateTaskSameKeyDifferentBody` | same key, different body → error |
| `task.TestUpdateTaskStaleVersion` | stale version → ABORTED |
| `task.TestDeleteTaskTwice` | deleting twice → success both times |
| `task.TestGroupTasksHiddenFromNonMembers` | a non-member cannot see group tasks → PERMISSION_DENIED |
| `task.TestListTasksKeysetPagination`, `TestUpdateTaskClearsDescription` | pagination and partial update |
| `groupclient.TestCheckMembershipRetriesUnavailable` | UNAVAILABLE → retry → success, 2 attempts |
| `groupclient.TestRetryPolicyMaxAttempts` | with a long deadline — exactly 3 attempts |
| `groupclient.TestCheckMembershipRetryBudgetLimitedByDeadline` | retries do not go past the 50 ms deadline |
| `groupclient.TestCheckMembershipRespectsIncomingDeadline` | an incoming 30 ms deadline (< 50 ms) is honoured, DEADLINE_EXCEEDED is not retried |
| `groupclient.TestCreateGroupTaskDeadlineSkipsDatabase` | `FAULT_DELAY_MS` > deadline → DEADLINE_EXCEEDED, the database is never called |
| `cache.TestReadThroughAndInvalidation` | miss → database; repeat → hit; write → version bumped, next request misses |
| `cache.TestListFirstPageCached`, `TestRedisDownFallsBackToDatabase` | list caching; with Redis down, requests still succeed |
| `httpserver.TestRESTContract` | REST codes: 201/200/422/400/412/204/403/401, RFC 7807 |
| `apperr.TestMapping` | gRPC/HTTP error mapping table |
