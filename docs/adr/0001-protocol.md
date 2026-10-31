# ADR 0001: gRPC between services, REST/JSON for external clients

- Status: accepted
- Context: Task Tracker. SLO: 2,000 RPS (80% reads), p99 < 100 ms, 99.9%.

## Context

A user request crosses several network hops: client → API Gateway → task-service →
group-service (membership check) → Redis / PostgreSQL. The p99 < 100 ms budget is split between them.
We need a protocol for internal calls and a format for the external API, which also serves as the
baseline for comparison.

Options considered: REST/JSON over HTTP/1.1, gRPC (HTTP/2 + Protocol Buffers), WebSocket.

## Decision

- **gRPC between services** over HTTP/2 + Protocol Buffers (`proto/tasktracker/v1`).
- **REST/JSON for the external API** (browsers and mobile clients); it is also the benchmark baseline.
  Both transports in task-service call the same `task.Service`, so the comparison measures only the
  transport and the format.

## Why

1. **Latency budget.** Binary protobuf is smaller than JSON (field names are not sent, numbers are
   varints) and serializes faster. HTTP/2 multiplexes requests over one connection: there is no
   HTTP-level request queue and no need for a pool of dozens of TCP connections.
2. **Deadlines are built into the protocol.** The client sends its deadline (`grpc-timeout`), the server
   receives it in `ctx` and passes it on. If the client has 40 ms left, task-service will not wait
   200 ms for group-service. REST has no such standard, so the deadline has to be set on the server.
3. **Declarative retries.** The service config sets `maxAttempts`, backoff and the list of codes per
   method (`internal/client/groupclient/retry.go`), with no hand-written loops in the code.
4. **Strict contract.** `.proto` is the single source of truth; client and server are generated, and an
   incompatible change shows up at compile time.

## Why not WebSocket

There is no real-time push requirement: every operation is request/response. WebSocket is a future
candidate (live updates of group tasks), but for CRUD it would add its own connection state, manual
request/response correlation and manual timeouts.

## Trade-offs

- gRPC does not work from a browser directly: it needs a gRPC-Web proxy or REST at the gateway (REST was chosen).
- Binary traffic is harder to debug: `grpcurl` instead of `curl`, and bodies are not visible in ordinary proxy logs.
- HTTP/2 is harder to balance at L4: connections are long-lived, and an L4 balancer "sticks" to one
  backend. It needs L7 balancing (Envoy) or client-side gRPC balancing.
- gRPC retries make their attempts within the call deadline: with a 50 ms CheckMembership deadline and
  pauses of ≈20/40 ms only one retry fits (see REPORT.md → "Retries").

## Measurements

See `bench/results/results.md` and the "Measurement results" section in REPORT.md.
