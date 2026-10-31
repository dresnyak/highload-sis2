# Mechanisms: what, where, questions and answers

For each mechanism: what it does, where it is in the code, a question that may come up, and a short answer.

---

## 1. One service layer for REST and gRPC

- **What:** all task logic lives in `task.Service`; the transports only turn a request into a service call and an error into a code.
- **Where:** `internal/service/task/service.go` (`Service`), `internal/transport/httpserver/server.go` (`Handler.*`), `internal/transport/grpcserver/task.go` (`TaskServer.*`), wiring — `cmd/task-service/main.go` (`run`).
- **Q:** is the REST vs gRPC comparison fair? — **A:** yes: one process, one `task.Service`, one database and cache. Only the transport (HTTP/1.1 vs HTTP/2) and the format (JSON vs protobuf) differ.

## 2. Authentication (JWT, MOCK auth service)

- **What:** REST — an HS256 JWT in `Authorization: Bearer`, `user_id` goes into the `context`. gRPC — no JWT and no interceptors: the handler reads `user_id` from the `x-user-id` metadata (MOCK: an API Gateway would set it after verifying the token).
- **Where:** `internal/service/auth/auth.go` (`Issue`, `Parse`, `Authenticate`), `internal/transport/httpserver/middleware.go` (`authMiddleware`), `internal/transport/grpcserver/user.go` (`callerID`), `cmd/devtoken/main.go`.
- **Q:** why `jwt.WithValidMethods`? — **A:** otherwise a token with `alg: none` or another algorithm could pass verification (a classic JWT attack).
- **Q:** how does group-service know the user? — **A:** task-service passes `x-user-id` in the metadata (`groupclient.Client.CheckMembership`). group-service allows `CheckMembership` only about the caller (`grpcserver.GroupServer.CheckMembership`). Production would use a service token or mTLS.

## 3. One error mapping

- **What:** a domain error `apperr.Error{Kind}` → gRPC code and HTTP status from one table; REST returns RFC 7807.
- **Where:** `internal/apperr/apperr.go` (`KindOf`, `GRPCCode`, `HTTPStatus`, `ToGRPC`, `FromGRPC`), `httpserver.Handler.writeError`.
- **Q:** how does `statement_timeout` become a 504? — **A:** `KindOf` recognizes SQLSTATE `57014` and `context.DeadlineExceeded` as `KindDeadline` → `DEADLINE_EXCEEDED` / 504.
- **Q:** why do internal errors carry no details? — **A:** so as not to reveal database internals; the details go to the log.

## 4. Idempotent CreateTask

- **What:** a required `idempotency_key` / `Idempotency-Key` (UUID). `INSERT ... ON CONFLICT (author_id, client_request_id) DO NOTHING RETURNING`; if nothing was inserted, `SELECT` the existing task. The body hash is stored in `request_hash`; the same key with a different body → 422 / INVALID_ARGUMENT.
- **Where:** `internal/repository/task/repo.go` (`Repo.Create`), `internal/model/task.go` (`CreateInput.RequestHash`), `migrations/0001_init.sql` (`uq_tasks_idem`), `httpserver.Handler.createTask` (201 vs 200).
- **Q:** why not "SELECT, then INSERT"? — **A:** between the two, two parallel retries would both see "not there" and insert two tasks. A unique index + `ON CONFLICT` make the check and the insert atomic.
- **Q:** why is the key unique together with `author_id`? — **A:** clients generate the keys; a collision between different users must not hand out someone else's task.
- **Q:** why a body hash? — **A:** if a client reuses a key for a different task by mistake, silently returning the old one would lose the new one. An explicit error is better.
- **Q:** why does a retry read from the primary? — **A:** the replica may not yet know about the row inserted by the first attempt.

## 5. Optimistic locking in UpdateTask

- **What:** `UPDATE ... WHERE id = $1 AND version = $2`, `version = version + 1`. No rows → ABORTED / 412. In REST the version comes in `If-Match` and is returned in `ETag`.
- **Where:** `internal/repository/task/repo.go` (`Repo.Update`), `internal/service/task/service.go` (`Service.UpdateTask`), `httpserver.parseIfMatch`.
- **Q:** why is UpdateTask not retried automatically? — **A:** if the first attempt was applied and the response was lost, a retry gets 412 because the version has already moved on. The client has to re-read the task and decide.
- **Q:** how does a partial update tell "not sent" from "clear"? — **A:** `optional` in proto and pointers in Go: `nil` → `COALESCE($n, col)` keeps the column, `""` clears the description.

## 6. Idempotent DeleteTask (soft delete)

- **What:** `deleted_at = now()`; deleting again succeeds (204 / Empty).
- **Where:** `Service.DeleteTask`, `Repo.GetForWrite` (sees deleted rows), `Repo.SoftDelete` (`WHERE deleted_at IS NULL`).
- **Q:** why soft delete? — **A:** a repeated DELETE can tell "already deleted" (success) from "never existed" (404); the row stays for auditing. Partial indexes `WHERE deleted_at IS NULL` do not bloat with deleted rows.

## 7. Keyset pagination and sorting by priority

- **What:** cursor = (priority, id) or (created_at, id) of the last row; `WHERE (priority, id) < ($p, $id) ORDER BY priority DESC, id DESC LIMIT n+1`.
- **Where:** `internal/repository/task/repo.go` (`Repo.List`), `internal/model/cursor.go`, indexes in `migrations/0001_init.sql`.
- **Q:** why not OFFSET? — **A:** OFFSET reads and throws away all previous rows (page 100 is slow), and inserts between requests shift the results. Keyset goes through the index starting right at the needed position.
- **Q:** why `n+1`? — **A:** the extra row shows that there is a next page, without a `COUNT(*)`.

## 8. Deadlines

- **What:** a budget derived from the p99 < 100 ms SLO. External client → 300 ms per attempt; CheckMembership — 50 ms, but never more than what is left of the incoming deadline; Redis — 20 ms; PostgreSQL — `statement_timeout` 80 ms + an 80 ms context per query.
- **Where:** `groupclient.Client.CheckMembership` (`MembershipTimeout`), `taskrepo.NewPool` (`statement_timeout`), `begin` in `repository/task` (checks `ctx.Err()` and applies `WithTimeout`), `cache.redisCtx` (`commandTimeout`), `httpserver.withTimeout` and `httpserver.NewServer`. In gRPC the client sets the deadline.
- **Q:** how does the deadline propagate? — **A:** the outgoing call uses the incoming request's `ctx` wrapped in `context.WithTimeout(ctx, 50ms)`; `WithTimeout` picks the earlier of the two deadlines. gRPC sends the remaining time in the `grpc-timeout` header. Test — `TestCheckMembershipRespectsIncomingDeadline`.
- **Q:** what if the deadline has already passed? — **A:** `begin` in `repository/task` does not start the database query. `TestCreateGroupTaskDeadlineSkipsDatabase` checks that the Store was never called.
- **Q:** why both `statement_timeout` and a context? — **A:** the context cancels the wait on the client; `statement_timeout` stops the query itself on the database server, even if the cancellation never arrives.
- **Q:** why `ReadHeaderTimeout`/`WriteTimeout`? — **A:** protection against slow clients (slowloris), which would otherwise hold connections and goroutines.
- **Q:** why `WaitReady` at startup? — **A:** setting up the HTTP/2 connection takes tens of milliseconds (~30 ms on this machine). Without warming it up, the first request would pay for it out of its 50 ms.

## 9. Retries

- **What:** gRPC service config: `maxAttempts 3`, backoff 20 ms → 200 ms, ×2, `UNAVAILABLE` only, idempotent methods only. The k6 load test repeats the same rules by hand for both protocols.
- **Where:** `internal/client/groupclient/retry.go` (`RetryServiceConfig`), `bench/k6/bench.js` (default function, `grpcAttempt`, `restAttempt`, `backoff`).
- **Q:** why not retry DEADLINE_EXCEEDED? — **A:** the request's time is already spent; a retry will not fit into the deadline and only adds load to an overloaded service.
- **Q:** why can CreateTask be retried but not UpdateTask and JoinByInvite? — **A:** CreateTask is protected by the idempotency key. UpdateTask gets 412 on a retry. JoinByInvite is not retried automatically: it is an explicit user action that the client repeats (a repeat is safe — an existing member succeeds without using up the link).
- **Q:** why jitter? — **A:** so that clients that failed at the same moment do not retry at the same moment too. In grpc-go 1.85 it is ±20% of the current backoff (`stream.go`, `shouldRetry`).
- **Q:** how many attempts does CheckMembership actually get? — **A:** pauses of ≈16–24 ms and ≈32–48 ms add up to ≥ 48 ms, so one retry fits into 50 ms. This is measured (`TestCheckMembershipRetryBudgetLimitedByDeadline`). A call with a 300 ms deadline makes all three attempts (`TestRetryPolicyMaxAttempts`). That is intended: failing fast is better than breaking p99.

## 10. Fault injection

- **What:** `FAULT_UNAVAILABLE_RATE` — the share of UNAVAILABLE responses; `FAULT_DELAY_MS` — an added delay (honours the call deadline).
- **Where:** `internal/transport/grpcserver/fault.go` (`FaultConfig.inject`, called at the start of `GroupServer.CheckMembership`), env parsing — `cmd/group-service/main.go` (`faultFromEnv`).
- **Q:** how does the test make random faults deterministic? — **A:** `FaultConfig.Rand` is replaced with a sequence (`sequence(0, 0.9)` = the first call fails, the second passes).

## 11. Read-through cache

- **What:** Redis, keys carrying the owner version, INCR on write, TTL 10 min ± 15%, singleflight, read-your-writes flag. Details — ADR 0002.
- **Where:** `internal/repository/cache/cache.go` (`TaskCache.GetTask`, `ListTasks`, `AfterWrite`, `ttl`).
- **Q:** why INCR a version instead of DEL on the keys? — **A:** there are unboundedly many lists with different filters, so they cannot be enumerated. INCR makes all of them unreachable with one command.
- **Q:** what if Redis is down? — **A:** a read is a miss and goes to the primary (the rw flag cannot be read, so we choose consistency). A write does not fail: invalidation gets up to 3 attempts, then a log line and a counter. Test — `TestRedisDownFallsBackToDatabase`.
- **Q:** what is a cache stampede and how is it prevented? — **A:** a popular key expires and hundreds of requests hit the database at once. `singleflight` lets one through to the database while the others wait for its result. TTL jitter keeps many keys from expiring at the same time.
- **Q:** why is only the first page cached? — **A:** it is read most often; a key per cursor would bloat Redis for no benefit.

## 12. Primary / replica (MOCK)

- **What:** two `pgxpool` pools; writes and the permission-check read before a write go to the primary, other reads go to the replica, except within the read-your-writes window.
- **Where:** `repository/task.Repo` (`pool`), `cache.TaskCache` (`fromPrimary`), `cmd/task-service/main.go`.
- **Q:** what is mocked? — **A:** both DSNs point at one container. The routing is real: plug in a replica DSN and no code has to change.

## 13. Groups and invites (MOCK storage)

- **What:** groups, members, bans, invite links; memory holds the token's sha256, and a link has `expires_at`, `max_uses`, `uses_count`, `revoked_at`. `JoinByInvite` for an existing member succeeds without using up the link.
- **Where:** `internal/repository/group/repository.go` (`Repository`, `MemoryRepository.Join`), `internal/service/group/service.go` (`HashToken`, `CreateGroup`), `internal/transport/grpcserver/group.go`.
- **Q:** why a hash without a salt? — **A:** the token is 256 random bits, so dictionary attacks are impossible. A salt protects weak passwords, not random tokens.
- **Q:** why `RWMutex`? — **A:** `CheckMembership` (a read) runs on every group task request; readers must not block each other.
- **Q:** why join via POST rather than GET? — **A:** GET must be safe: link previews in messengers would otherwise join the group on the user's behalf.

## 14. Access control

- **What:** a personal task is visible only to its author (someone else's → 404, so the id's existence is not revealed); a group task is visible to members (otherwise 403); a group task can be deleted by its author or the group owner.
- **Where:** `task.Service.checkRead`, `task.Service.DeleteTask`, `groupclient.Client.CheckMembership`.
- **Q:** why is membership checked on every request? — **A:** a removed or banned member must lose access immediately, not after a cache TTL.

## 15. Benchmark

- **What:** a k6 script, a protocol × op × concurrency × cache matrix; latency percentiles from k6's `iteration_duration` (one iteration = one call with its retries), bytes per request from `data_sent` / `data_received`, custom `call_errors` and `call_retries` counters.
- **Where:** `bench/k6/bench.js` (`options`, `setup`, default function, `handleSummary`), the `k6` service in `docker-compose.yml`, `bench/run-all.sh`, `cmd/benchreport/main.go`.
- **Q:** why percentiles rather than an average? — **A:** the SLO is defined on p99; an average hides the tail.
- **Q:** why does k6 run inside the docker network? — **A:** on Windows, Docker Desktop port forwarding added rare 40–50 ms stalls to gRPC; running the client next to the services removes that proxy from the path.
- **Q:** where do the REST JWTs come from? — **A:** `setup()` signs one HS256 token per user with `k6/crypto` HMAC and the same `JWT_SECRET` as the service; gRPC sends `x-user-id`.
- **Q:** what do the byte numbers include? — **A:** everything k6 counts on the wire: HTTP/1.1 headers (including the JWT in every REST request), HTTP/2 frames and HPACK headers, and the bodies.
- **Q:** why `iteration_duration` and not `http_req_duration`? — **A:** in long REST runs k6 reported a single `http_req_waiting` of ~2.3 s while the longest whole iteration containing it took under 20 ms, so the per-request timing had a bogus point. `iteration_duration` is the full call with retries, measured the same way for both protocols.
- **Q:** why `discardResponseMessage` for gRPC? — **A:** otherwise the k6 gRPC client decodes every response into JavaScript objects; for a 50-task list that made the load generator the bottleneck (1,376 vs 4,975 req/s at concurrency 64). REST bodies are not parsed either, so both sides only receive the bytes.
- **Q:** how is req/s computed? — **A:** VUs × 1000 / mean call time, so k6 start-up (loading `.proto`, connecting) is not counted as load time.
