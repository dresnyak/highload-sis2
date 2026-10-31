#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

REQUESTS=${REQUESTS:-20000}
WARMUP=${WARMUP:-2000}
USERS=${USERS:-1000}
PER_USER=${PER_USER:-100}
export JWT_SECRET=${JWT_SECRET:-change-me-dev-secret}
export PRIMARY_DSN=${PRIMARY_DSN:-postgres://tasks:tasks@localhost:55432/tasks?sslmode=disable}
export MSYS_NO_PATHCONV=1

RAW=bench/results/raw
OUT=bench/results/results.csv
rm -rf "$RAW" && mkdir -p "$RAW"

wait_ready() {
  for _ in $(seq 1 60); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/v1/me/tasks)" = "401" ]; then
      return 0
    fi
    sleep 1
  done
  echo "task-service did not start" >&2
  exit 1
}

k6() {
  docker compose run --rm -T \
    -e PROTOCOL="$1" -e OP="$2" -e VUS="$3" -e REQUESTS="$4" -e LABEL="$5" -e OUT="${6:-}" \
    -e USERS="$USERS" -e PER_USER="$PER_USER" \
    k6 run --quiet /scripts/bench.js 2>&1 | { grep -v 'level=info' || true; }
}

docker compose up -d --build postgres redis group-service

for cache in true false; do
  label="cache=$([ "$cache" = true ] && echo on || echo off)"
  echo "=== $label ==="
  go run ./cmd/seed -users "$USERS" -tasks-per-user "$PER_USER"
  docker compose exec -T redis redis-cli FLUSHALL >/dev/null
  CACHE_ENABLED=$cache docker compose up -d --build task-service
  wait_ready

  for op in get list create; do
    for c in 1 16 64; do
      for p in grpc rest; do
        if ! warmup=$(k6 "$p" "$op" "$c" "$WARMUP" "$label"); then
          echo "warm-up failed: $p $op c=$c" >&2
          echo "$warmup" >&2
          exit 1
        fi
        k6 "$p" "$op" "$c" "$REQUESTS" "$label" "/results/raw/$p-$op-c$c-${label#cache=}.csv"
      done
    done
  done
done

CACHE_ENABLED=true docker compose up -d task-service >/dev/null

echo "protocol,op,concurrency,label,requests,errors,retries,p50_ms,p95_ms,p99_ms,max_ms,rps,sent_bytes_per_req,recv_bytes_per_req" > "$OUT"
for label in on off; do
  for op in get list create; do
    for c in 1 16 64; do
      for p in grpc rest; do
        cat "$RAW/$p-$op-c$c-$label.csv" >> "$OUT"
      done
    done
  done
done
rm -rf "$RAW"

go run ./cmd/benchreport -in "$OUT" -out bench/results/results.md
echo "done: bench/results/results.md"
