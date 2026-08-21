#!/usr/bin/env bash
#
# backup.sh -- take and restore backups of the Kora database.
#
# This project had no backup path at all. For a memory engine that is a
# particularly bad gap: the data is not derivable from anything else, so losing
# the volume loses everything an agent has ever learned.
#
# Two things about this database make a naive `pg_dump` insufficient, both found
# by actually restoring one rather than by reading documentation:
#
#  1. The graph lives in its own schema (`context0`, named before the project
#     was renamed to Kora and unchanged because renaming it is a data
#     migration) alongside AGE's `ag_catalog`. A schema-scoped dump misses the
#     catalog and the restored graph is unusable. This dumps the whole database.
#
#  2. pg_restore rebuilds the pgvector HNSW index, and that build needs more
#     shared memory in one allocation than Kubernetes' default 64Mi /dev/shm
#     allows -- at 94k embeddings it asked for 131MB and failed with "could not
#     resize shared memory segment". The data restored fine and the index
#     silently did not, leaving vector search scanning every row. Worse, the API
#     treats a failed index creation as fatal at startup, so the deployment
#     could not come up on the recovered data at all.
#
#     The chart now sets postgres.shmSize (default 256Mi). verify_restore below
#     checks the index actually exists after a restore, because that failure is
#     silent in pg_restore's output unless you read every line.
#
# Usage:
#   scripts/backup.sh dump [file]        # write a compressed dump locally
#   scripts/backup.sh restore <file>     # restore into a scratch database
#   scripts/backup.sh verify <file>      # restore, check, and drop it again
#
# Environment:
#   NS         namespace (default: kora)
#   PG_POD     postgres pod (default: postgres-age-0)
#   DB         database name (default: kora)
#   PG_USER    postgres role (default: kora)
#
# DB and PG_USER are separate knobs because the rename to Kora left them free
# to disagree. An operator upgrading from Context0 may keep the old names by
# setting postgres.user and postgres.database on the chart -- the documented
# alternative to running scripts/migrate_rename.sh -- and this script has to be
# usable against that deployment. The role used to be hardcoded, so it was not.
set -euo pipefail

NS="${NS:-kora}"
PG_POD="${PG_POD:-postgres-age-0}"
DB="${DB:-kora}"
PG_USER="${PG_USER:-kora}"

pg() { kubectl exec -n "$NS" "$PG_POD" -- "$@"; }
psql_q() { pg psql -U "$PG_USER" -d "$1" -tAc "$2"; }

# The dump is streamed to the caller rather than written inside the pod: the
# container has a read-only root filesystem apart from /tmp, and /tmp is
# ephemeral, so a backup left there disappears with the pod that holds it.
cmd_dump() {
  local out="${1:-kora-$(date -u +%Y%m%dT%H%M%SZ).dump}"
  echo "Dumping $DB from $NS/$PG_POD..." >&2
  kubectl exec -n "$NS" "$PG_POD" -- \
    pg_dump -U "$PG_USER" -d "$DB" -Fc --no-owner --no-acl > "$out"
  local size
  size=$(wc -c < "$out" | tr -d ' ')
  if [[ "$size" -lt 1024 ]]; then
    echo "error: dump is only ${size} bytes; refusing to call that a backup" >&2
    exit 1
  fi
  echo "Wrote $out ($(numfmt --to=iec "$size" 2>/dev/null || echo "$size bytes"))" >&2
  echo "$out"
}

# Restores into a named database, which is never the live one: a restore that
# can silently overwrite production is a foot-gun, and the useful operation
# during an incident is to restore beside the live data and compare.
cmd_restore() {
  local file="$1" target="${2:-kora_restore}"
  [[ -f "$file" ]] || { echo "error: no such file: $file" >&2; exit 1; }

  echo "Restoring $file into database '$target'..." >&2
  psql_q postgres "DROP DATABASE IF EXISTS $target" >/dev/null
  psql_q postgres "CREATE DATABASE $target" >/dev/null
  kubectl exec -i -n "$NS" "$PG_POD" -- \
    pg_restore -U "$PG_USER" -d "$target" --no-owner --no-acl < "$file" 2>&1 \
    | grep -v '^pg_restore: warning: errors ignored' || true
  echo "$target"
}

# The part that matters. pg_restore reports success while having skipped an
# index, so "it restored" is not the same as "it is usable".
cmd_verify() {
  local file="$1" target="kora_verify_$$"
  local fail=0

  cmd_restore "$file" "$target" >/dev/null

  local memories edges embeddings idx
  memories=$(psql_q "$target" \
    "LOAD 'age'; SET search_path=ag_catalog,public;
     SELECT count(*) FROM cypher('context0', \$\$ MATCH (m:Memory) RETURN m \$\$) AS (m agtype);" \
    | tail -1)
  edges=$(psql_q "$target" \
    "LOAD 'age'; SET search_path=ag_catalog,public;
     SELECT count(*) FROM cypher('context0', \$\$ MATCH ()-[e]->() RETURN e \$\$) AS (e agtype);" \
    | tail -1)
  embeddings=$(psql_q "$target" "SELECT count(*) FROM memory_embeddings")
  idx=$(psql_q "$target" \
    "SELECT count(*) FROM pg_indexes WHERE indexname='idx_memory_embeddings_vector'")

  echo "  memories:   $memories"
  echo "  edges:      $edges"
  echo "  embeddings: $embeddings"

  [[ "${memories:-0}" -gt 0 ]] || { echo "  FAIL: no memories restored"; fail=1; }
  [[ "${edges:-0}" -gt 0 ]] || { echo "  FAIL: no edges restored"; fail=1; }

  # The silent failure this whole script exists to catch.
  if [[ "${idx:-0}" -eq 0 ]]; then
    echo "  FAIL: the HNSW vector index is missing after restore."
    echo "        pg_restore reports success while skipping it. Check /dev/shm:"
    echo "        the build needs more than Kubernetes' 64Mi default in one"
    echo "        allocation. See postgres.shmSize in values.yaml."
    fail=1
  else
    echo "  vector index: present"
  fi

  # Internal consistency: an embedding belongs to a memory, so there cannot be
  # more embeddings than memories.
  #
  # A backup holding 50 of 290,703 memories passed every check above -- the
  # counts were printed and nothing objected to 289,851 embeddings against 50
  # memories. "More than zero" is not the assurance a backup is meant to give,
  # and this ratio proves the dump is incoherent without needing the source,
  # which is the situation a restore exists for.
  if [[ "${embeddings:-0}" -gt "${memories:-0}" ]]; then
    echo "  FAIL: $embeddings embeddings against $memories memories."
    echo "        An embedding belongs to a memory, so this dump is missing"
    echo "        memories that its embeddings still reference. A dump taken"
    echo "        while writes were in flight, or a partial one, looks like this."
    fail=1
  fi

  # Compare against the source when it is reachable. A dump that captured half
  # the graph restores cleanly and reads correctly; only the comparison shows
  # it is not a backup of what exists.
  #
  # Skipped rather than failed when the live database is gone, because that is
  # precisely when a restore matters most and the check cannot be run.
  local live_memories
  live_memories=$(psql_q "$DB" \
    "LOAD 'age'; SET search_path=ag_catalog,public;
     SELECT count(*) FROM cypher('context0', \$\$ MATCH (m:Memory) RETURN m \$\$) AS (m agtype);" \
    2>/dev/null | tail -1 || true)
  if [[ -n "${live_memories:-}" && "${live_memories:-0}" -gt 0 ]]; then
    # A tolerance, not equality: the live database keeps accepting writes while
    # the dump is taken, so the restore is legitimately a little behind.
    local floor=$(( live_memories * 90 / 100 ))
    if [[ "${memories:-0}" -lt "$floor" ]]; then
      echo "  FAIL: restored $memories memories against $live_memories live"
      echo "        (below the $floor floor). The dump does not hold what the"
      echo "        database holds, so restoring it would lose data."
      fail=1
    else
      echo "  vs live:      $memories restored / $live_memories live"
    fi
  else
    echo "  vs live:      skipped (source database unreachable)"
  fi

  # A count is not proof the rows are readable. Read one back.
  local sample
  sample=$(psql_q "$target" \
    "LOAD 'age'; SET search_path=ag_catalog,public;
     SELECT count(*) FROM cypher('context0', \$\$ MATCH (m:Memory) WHERE m.content IS NOT NULL RETURN m \$\$) AS (m agtype) LIMIT 1;" \
    | tail -1)
  [[ "${sample:-0}" -gt 0 ]] || { echo "  FAIL: restored memories have no readable content"; fail=1; }

  psql_q postgres "DROP DATABASE IF EXISTS $target" >/dev/null

  if [[ "$fail" -ne 0 ]]; then
    echo "RESTORE VERIFICATION FAILED" >&2
    return 1
  fi
  echo "Restore verified: the backup is usable."
}

case "${1:-}" in
  dump)    shift; cmd_dump "$@" ;;
  restore) shift; cmd_restore "$@" ;;
  verify)  shift; cmd_verify "$@" ;;
  *)
    cat >&2 <<EOF
Usage: scripts/backup.sh <command>

  dump [file]       Write a compressed dump to the local filesystem.
  restore <file>    Restore into a scratch database (never the live one).
  verify <file>     Restore, assert the result is usable, then drop it.

A backup that has never been restored is not a backup. Run 'verify'
periodically, not just when you need it.
EOF
    exit 1
    ;;
esac
