#!/usr/bin/env bash
# A throwaway PostgreSQL + AGE + pgvector for `make eval`.
#
# Fresh on every `up`: the evaluation loads a fixed corpus into an empty
# database and refuses a non-empty one, because a corpus loaded twice, or
# beside another, ranks differently. Tearing the container down and back up
# is what makes two runs comparable.
#
# The image is the repository's own docker/postgres-age-vector, built once and
# cached by Docker; set KORA_EVAL_PG_IMAGE to use an image already on the
# machine.
set -euo pipefail

NAME=${KORA_EVAL_PG_NAME:-kora-eval-pg}
PORT=${KORA_EVAL_PG_PORT:-55450}
IMAGE=${KORA_EVAL_PG_IMAGE:-kora-eval-postgres}

case "${1:-}" in
  up)
    if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
      docker build -t "$IMAGE" docker/postgres-age-vector
    fi
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    docker run -d --rm --name "$NAME" -p "$PORT:5432" \
      -e POSTGRES_USER=kora -e POSTGRES_PASSWORD=eval -e POSTGRES_DB=kora \
      --shm-size=256m "$IMAGE" >/dev/null
    for _ in $(seq 1 60); do
      if docker exec "$NAME" pg_isready -U kora -d kora >/dev/null 2>&1; then
        # pg_isready passes before the init scripts have finished on first
        # boot; the extension query is what proves the database is usable.
        if docker exec "$NAME" psql -U kora -d kora -Atc "SELECT 1 FROM pg_extension WHERE extname='age'" 2>/dev/null | grep -q 1; then
          echo "postgres://kora:eval@localhost:$PORT/kora?sslmode=disable"
          exit 0
        fi
      fi
      sleep 1
    done
    echo "$NAME did not become ready" >&2
    docker logs "$NAME" >&2 || true
    exit 1
    ;;
  down)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    ;;
  *)
    echo "usage: $0 up|down" >&2
    exit 2
    ;;
esac
