#!/usr/bin/env bash
#
# migrate_rename.sh -- move an existing deployment from the Context0 names to
# the Kora ones.
#
# The rename to Kora changed the code, the chart, and the environment
# variables. It deliberately did NOT change three things that live in the
# database rather than in the repo:
#
#   - the Postgres role      (context0)
#   - the Postgres database  (context0)
#   - the AGE graph / schema (context0)
#
# Those are data, not branding. A find-and-replace in the repo cannot rename
# them; it can only point a new build at names that do not exist yet. That
# failure is not hypothetical -- running the renamed docker-compose.yaml
# against an existing volume produces:
#
#   FATAL: role "kora" does not exist
#
# and, because the Postgres healthcheck reports healthy regardless, the API
# starts and fails to connect rather than failing at startup.
#
# This script renames the role and the database, which is what docker-compose
# and the chart now expect. Both are catalog-only operations: no table is
# rewritten and no row is copied, so the cost does not scale with the size of
# the database.
#
# The AGE graph and its schema are left as `context0` on purpose -- see
# GraphName in internal/graph/age.go. Renaming the graph means rewriting every
# label table's schema qualification and updating ag_catalog, which is a real
# data migration for no functional gain.
#
# Passwords survive the role rename because this deployment uses
# scram-sha-256, which does not derive the stored verifier from the role name.
# Under md5 the hash IS derived from the username, so the password would have
# to be reset; this script checks and refuses rather than silently locking the
# role out.
#
# Usage:
#   scripts/migrate_rename.sh                  # against docker compose
#   PGHOST=... PGPORT=... scripts/migrate_rename.sh --direct
#
# The script is idempotent: it reports and exits successfully if the rename has
# already happened.

set -euo pipefail

OLD_NAME="context0"
NEW_NAME="kora"
CONTAINER="${CONTAINER:-kora-postgres}"
DIRECT=0
[ "${1:-}" = "--direct" ] && DIRECT=1

# Load POSTGRES_PASSWORD from .env when present, matching docker-compose.
if [ -f .env ]; then
    # shellcheck disable=SC1091
    source .env
fi

# psql_as runs a statement as the given role against the given database.
# Renaming a role or database requires that neither is in use by the session
# doing the renaming, so these connect to `postgres` where possible.
psql_as() {
    local user="$1" db="$2" sql="$3"
    if [ "$DIRECT" = "1" ]; then
        PGPASSWORD="${POSTGRES_PASSWORD:-}" psql -U "$user" -d "$db" -tAc "$sql"
    else
        docker exec -e PGPASSWORD="${POSTGRES_PASSWORD:-}" "$CONTAINER" \
            psql -U "$user" -d "$db" -tAc "$sql"
    fi
}

echo "==> Checking current state"

# Determine which role actually exists. Doing this first makes the script
# idempotent and lets it give a specific message instead of a psql error.
if psql_as "$NEW_NAME" postgres "SELECT 1" >/dev/null 2>&1; then
    echo "    role '$NEW_NAME' already exists -- rename already applied"
    ALREADY_ROLE=1
elif psql_as "$OLD_NAME" postgres "SELECT 1" >/dev/null 2>&1; then
    echo "    role '$OLD_NAME' found, will rename to '$NEW_NAME'"
    ALREADY_ROLE=0
else
    echo "ERROR: neither role '$OLD_NAME' nor '$NEW_NAME' can connect." >&2
    echo "       Check POSTGRES_PASSWORD and that the database is running." >&2
    exit 1
fi

CONN_USER="$OLD_NAME"
[ "$ALREADY_ROLE" = "1" ] && CONN_USER="$NEW_NAME"

# md5 verifiers are salted with the role name, so renaming the role under md5
# invalidates the stored password. Refuse rather than lock the account out.
if [ "$ALREADY_ROLE" = "0" ]; then
    ENC=$(psql_as "$CONN_USER" postgres \
        "SELECT CASE WHEN rolpassword LIKE 'md5%' THEN 'md5' ELSE 'other' END
         FROM pg_authid WHERE rolname = '$OLD_NAME'" 2>/dev/null || echo unknown)
    if [ "$ENC" = "md5" ]; then
        echo "ERROR: role '$OLD_NAME' uses an md5 password verifier, which is" >&2
        echo "       salted with the role name. Renaming it would invalidate" >&2
        echo "       the password. Re-hash with scram-sha-256 first:" >&2
        echo "         ALTER ROLE $OLD_NAME PASSWORD '<same password>';" >&2
        echo "       (with password_encryption = scram-sha-256), then re-run." >&2
        exit 1
    fi
fi

# cleanup_tmp_role removes the temporary superuser created below. It is
# defined unconditionally, and does nothing when TMP_ROLE is unset, so the
# already-migrated path can call it without special-casing.
TMP_ROLE=""
cleanup_tmp_role() {
    [ -n "$TMP_ROLE" ] || return 0
    psql_as "$CONN_USER" postgres \
        "DROP ROLE IF EXISTS $TMP_ROLE" >/dev/null 2>&1 || true
}

# reap_stray_roles drops any kora_rename_* superuser left behind by an earlier
# run that died before its own cleanup.
#
# This matters more than ordinary tidiness: the helper is created LOGIN
# SUPERUSER, so a leaked one is a fully privileged account on the database with
# a password that no longer exists anywhere. An interrupted migration must not
# leave that behind, and a later run is the only thing likely to notice, so
# every run reaps strays before doing anything else.
reap_stray_roles() {
    local as_user="$1"
    local strays
    strays=$(psql_as "$as_user" postgres \
        "SELECT rolname FROM pg_roles WHERE rolname LIKE 'kora_rename\\_%'" \
        2>/dev/null || true)
    [ -n "$strays" ] || return 0
    while IFS= read -r r; do
        [ -n "$r" ] || continue
        if psql_as "$as_user" postgres "DROP ROLE IF EXISTS $r" >/dev/null 2>&1; then
            echo "    dropped leftover privileged role $r from an interrupted run"
        else
            echo "    WARNING: could not drop leftover superuser role $r" >&2
            echo "             Drop it manually: DROP ROLE $r;" >&2
        fi
    done <<< "$strays"
}

reap_stray_roles "$CONN_USER"

echo "==> Renaming role"
if [ "$ALREADY_ROLE" = "0" ]; then
    # Postgres refuses "ALTER ROLE ... RENAME TO" when the role being renamed
    # is the session user ("session user cannot be renamed"), and this
    # deployment ships exactly one login role, so there is no second account to
    # run the rename from. Create a short-lived superuser, do both renames
    # through it, and drop it at the end.
    #
    # The password is random and never leaves this shell. The role is removed
    # in the same run, including on failure, via the trap below.
    TMP_ROLE="kora_rename_$$"
    TMP_PASS=$(openssl rand -hex 16)

    trap cleanup_tmp_role EXIT

    psql_as "$OLD_NAME" postgres \
        "CREATE ROLE $TMP_ROLE LOGIN SUPERUSER PASSWORD '$TMP_PASS'" >/dev/null

    # Connections must be made as the temporary role from here on, so
    # POSTGRES_PASSWORD is swapped for the duration of the two renames.
    REAL_PASS="${POSTGRES_PASSWORD:-}"
    POSTGRES_PASSWORD="$TMP_PASS"

    # The old role may still hold open connections; the rename itself does not
    # require them closed, but the database rename below does.
    psql_as "$TMP_ROLE" postgres \
        "ALTER ROLE $OLD_NAME RENAME TO $NEW_NAME" >/dev/null
    echo "    role $OLD_NAME -> $NEW_NAME"
    CONN_USER="$TMP_ROLE"
    RESTORE_PASS="$REAL_PASS"
else
    echo "    skipped (already $NEW_NAME)"
    RESTORE_PASS="${POSTGRES_PASSWORD:-}"
fi

echo "==> Renaming database"
DB_EXISTS=$(psql_as "$CONN_USER" postgres \
    "SELECT 1 FROM pg_database WHERE datname = '$OLD_NAME'" || true)
if [ "$DB_EXISTS" = "1" ]; then
    # A database cannot be renamed while any session is connected to it, and
    # the API pods reconnect automatically. Terminate them first; the caller is
    # expected to have scaled the API down.
    psql_as "$CONN_USER" postgres \
        "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
         WHERE datname = '$OLD_NAME' AND pid <> pg_backend_pid()" >/dev/null
    psql_as "$CONN_USER" postgres \
        "ALTER DATABASE $OLD_NAME RENAME TO $NEW_NAME" >/dev/null
    echo "    database $OLD_NAME -> $NEW_NAME"
else
    echo "    skipped (already $NEW_NAME)"
fi

echo "==> Verifying"

# Drop the temporary superuser before verifying, so the check below exercises
# the same credentials the application will actually use rather than a
# privileged account that is about to disappear.
cleanup_tmp_role 2>/dev/null || true
trap - EXIT
POSTGRES_PASSWORD="$RESTORE_PASS"

# Connect under the new names and confirm the graph data is still reachable.
# Renaming the role and database must not have disturbed the AGE graph, which
# keeps its original name.
COUNT=$(psql_as "$NEW_NAME" "$NEW_NAME" \
    "LOAD 'age'; SET search_path = ag_catalog, public;
     SELECT count(*) FROM cypher('$OLD_NAME',
       \$\$ MATCH (m:Memory) RETURN m \$\$) AS (m agtype);" | tail -1)

GRAPH=$(psql_as "$NEW_NAME" "$NEW_NAME" \
    "LOAD 'age'; SELECT name FROM ag_catalog.ag_graph WHERE name = '$OLD_NAME';" | tail -1)

if [ "$GRAPH" != "$OLD_NAME" ]; then
    echo "ERROR: the AGE graph '$OLD_NAME' is missing after the rename." >&2
    exit 1
fi

echo "    connected as role '$NEW_NAME' to database '$NEW_NAME'"
echo "    AGE graph '$OLD_NAME' intact, $COUNT memories readable"
echo
echo "Done. The graph and schema keep the name '$OLD_NAME' by design."
echo "Update any CONTEXT0_* environment variables to KORA_* -- the engine"
echo "now refuses to start when the old names are still set."
