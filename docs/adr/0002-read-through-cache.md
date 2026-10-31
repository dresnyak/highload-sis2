# ADR 0002: read-through cache in Redis, synchronous writes to PostgreSQL

- Status: accepted

## Context

The load is 80% reads / 20% writes, p99 < 100 ms. The most frequent reads are "my tasks" and
"group tasks" (first page) and a single task. PostgreSQL is the source of truth
(primary + read replicas, eventual consistency for replicas, read-your-writes for the author).

## Decision

A **read-through** cache (`internal/repository/cache`): business logic reads only through `cache.TaskCache`;
on a miss the cache loads the data from the replica itself and puts it into Redis.
**Writes go synchronously to the primary**, followed by cache invalidation. **No write-behind.**

### Keys and invalidation through an owner version

```
u:{uid}:ver                       user version (INCR on a personal task write)
g:{gid}:ver                       group version (INCR on a group task write)
u:{uid}:v{ver}:task:{id}          personal task
g:{gid}:v{ver}:task:{id}          group task
u:{uid}:v{ver}:list:{hash}        first page of the personal list for a filter set
g:{gid}:v{ver}:list:{hash}        the same for a group
t:{id}:g                          pointer "the task belongs to group gid" (group_id never changes)
u:{uid}:rw                        read-your-writes flag, TTL 5 s
```

A write does `INCR` on the owner's version, and all of the owner's cached lists (with any filters) and
tasks become unreachable at once. There is no need to delete keys one by one, and they could not be
enumerated anyway: the number of filter combinations is unbounded. Old values live out their TTL.

### Details

- **TTL 10 minutes ± 15%** — jitter, so keys created at the same moment do not expire at the same moment.
- **Stampede:** `singleflight.Group` — one loader per key within a process.
- **Read-your-writes:** after a write `u:{uid}:rw` is set (TTL 5 s); while the flag exists, this user's
  misses are read from the primary. The flag is read by the same `MGET` as the version, with no extra RTT.
- **Redis timeout 20 ms:** no answer is a miss, we go to the database, and the request does not fail.
- **`INCR` error:** up to 3 attempts, then a log line and the `InvalidationFailures` counter; staleness is
  bounded by the TTL. Invalidation runs with `context.WithoutCancel`: the write is already committed, and
  invalidation has to finish even if the request's deadline has expired.
- **Value format** — protobuf bytes, the same for REST and gRPC.
- **`CACHE_ENABLED` flag:** with `false` Redis is not used at all and reads go to the primary, so the
  benchmark can compare the paths with and without the cache.
- **Membership checks are not cached** together with a group list: a removed member must lose access
  immediately.

## Why not write-behind

Write-behind (write to Redis first, to the database asynchronously) speeds up writes, but:

1. **Data loss.** If Redis goes down before flushing to the database, created tasks are gone even though
   the client got a 201. For a task tracker a lost task is worse than a slow write.
2. **Idempotency and uniqueness** (`ON CONFLICT` on the idempotency key, optimistic locking on
   `version`) rely on PostgreSQL constraints. With write-behind they would have to be reimplemented in Redis.
3. **Writes are 20% of traffic**, mostly task creation; a single indexed `INSERT` fits the budget.
   The gain is not worth the risk.

## Consequences

- A read by another user after a write may return replica data that lags behind — this is the eventual
  consistency from the requirements. The author of a change sees their write (the rw flag).
- If the replica lags more than the rw window (5 s), a stale version may land in the cache under the new
  number. The staleness is bounded by the TTL; production needs replication lag monitoring.
- Singleflight works only within a process: N instances can produce up to N concurrent loads of one key.
  At 2,000 RPS this is acceptable; otherwise a distributed lock in Redis is needed.
