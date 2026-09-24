#!/bin/sh
set -eu

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 2; }

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
name=nodeflow-migration-test-$$
temporary=$(mktemp -d)
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$temporary"
}
trap cleanup EXIT HUP INT TERM

docker run -d --name "$name" \
  -e POSTGRES_DB=nodeflow \
  -e POSTGRES_USER=nodeflow \
  -e POSTGRES_PASSWORD=migration-test-password \
  postgres:17-alpine >/dev/null

attempt=0
until docker exec "$name" pg_isready -h 127.0.0.1 -U nodeflow -d nodeflow >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 40 ]; then
    echo "PostgreSQL did not become ready" >&2
    exit 1
  fi
  sleep 0.25
done

run_migrations() {
  directory=$1
	 database=${2:-nodeflow}
  docker run --rm --network "container:$name" \
	-e PGHOST=127.0.0.1 -e PGDATABASE="$database" -e PGUSER=nodeflow \
    -e PGPASSWORD=migration-test-password \
    -v "$directory:/migrations:ro" -v "$repo/scripts/migrate.sh:/migrate.sh:ro" \
    postgres:17-alpine /bin/sh /migrate.sh
}

run_migrations "$repo/migrations"
run_migrations "$repo/migrations"

count=$(docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -XAt -U nodeflow -d nodeflow -c 'SELECT count(*) FROM schema_migrations')
expected=$(find "$repo/migrations" -maxdepth 1 -name '*.up.sql' | wc -l | tr -d ' ')
if [ "$count" != "$expected" ]; then
  echo "migration count=$count expected=$expected" >&2
  exit 1
fi

# Upgrade fixture: an existing enabled route on actual revision 5 must remain
# visibly deployed/active after lifecycle columns are introduced. runtime_names
# exercises older membership metadata; route_fingerprints exercises the later
# canonical ledger projection without requiring route_backends.
fixture_db=nodeflow_fixture
docker exec -e PGPASSWORD=migration-test-password "$name" \
  createdb -U nodeflow "$fixture_db"
docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -X -v ON_ERROR_STOP=1 -U nodeflow -d nodeflow -c \
  "ALTER DATABASE $fixture_db SET timezone TO 'Europe/Moscow'" >/dev/null
fixture_pre="$temporary/pre-lifecycle"
mkdir -p "$fixture_pre"
cp "$repo"/migrations/00000[1-9]_*.up.sql "$repo"/migrations/000010_*.up.sql "$fixture_pre/"
run_migrations "$fixture_pre" "$fixture_db"
docker exec -i -e PGPASSWORD=migration-test-password "$name" \
  psql -X -v ON_ERROR_STOP=1 -U nodeflow -d "$fixture_db" >/dev/null <<'SQL'
INSERT INTO nodes(id,name,address,status)
VALUES('11111111-1111-4111-8111-111111111111','existing-edge','192.0.2.10','online');
INSERT INTO routes(
  id,node_id,hostname,target_host,target_port,enabled,listener_ip,listener_port,
  fallback,target_type,unix_socket_path,proxy_protocol,quota_action,custom_fragment
) VALUES(
  '22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
  'vpn.example.com','192.0.2.20',443,true,'*',443,false,'tcp','','none','observe',''
);
INSERT INTO route_snis(route_id,node_id,listener_ip,listener_port,position,sni)
VALUES(
  '22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
  '*',443,0,'vpn.example.com'
);
INSERT INTO config_revisions(node_id,revision,config,sha256,note,metadata,created_by)
VALUES(
  '11111111-1111-4111-8111-111111111111',5,'global
    daemon
defaults
    mode tcp
','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','existing',
  jsonb_build_object(
    'renderer','haproxy-tcp-sni-v1',
    'runtime_names',jsonb_build_array(jsonb_build_object(
      'route_id','22222222-2222-4222-8222-222222222222'
    )),
    'route_fingerprints',jsonb_build_object(
      '22222222-2222-4222-8222-222222222222',repeat('b',64)
    )
  ),
  'fixture'
);
INSERT INTO node_config_state(node_id,desired_revision,actual_revision,state)
VALUES('11111111-1111-4111-8111-111111111111',5,5,'in_sync');
INSERT INTO traffic_monthly(node_id,month,scope,proxy_name,bytes_in,bytes_out,updated_at)
VALUES(
  '11111111-1111-4111-8111-111111111111','2026-07-01','backend',
  'nf_be_22222222222242228222222222222222',123,456,'2026-07-15 12:00:00+00'
);
SQL
run_migrations "$repo/migrations" "$fixture_db"
fixture_ok=$(docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -XAt -U nodeflow -d "$fixture_db" -c \
  "SELECT enabled AND deployed AND deployment_state='active' AND desired_revision=5 AND applied_revision=5 AND version=1 AND quota_period='calendar_month' AND name='vpn.example.com' AND match_mode='sni' AND health_check AND NOT dns_pool AND desired_fingerprint=repeat('b',64) AND deployed_fingerprint=repeat('b',64) AND EXISTS (SELECT 1 FROM traffic_quota_usage WHERE node_id=routes.node_id AND proxy_name='nf_be_22222222222242228222222222222222' AND period='calendar_month' AND window_start='2026-07-01 00:00:00+00'::timestamptz AND window_end='2026-08-01 00:00:00+00'::timestamptz AND bytes_in=123 AND bytes_out=456) FROM routes WHERE id='22222222-2222-4222-8222-222222222222'")
if [ "$fixture_ok" != "t" ]; then
  echo "route lifecycle upgrade did not preserve existing active route" >&2
  exit 1
fi

# Route semantics: disabled drafts may overlap. Conflicts are enforced when a
# route is enabled under the per-node transaction lock, not by global indexes.
docker exec -i -e PGPASSWORD=migration-test-password "$name" \
  psql -X -v ON_ERROR_STOP=1 -U nodeflow -d "$fixture_db" >/dev/null <<'SQL'
INSERT INTO routes(
  id,node_id,name,match_mode,health_check,hostname,target_host,target_port,enabled,
  listener_ip,listener_port,fallback,target_type,unix_socket_path,proxy_protocol,
  quota_action,quota_period,custom_fragment,sort_order
) VALUES
  ('33333333-3333-4333-8333-333333333333','11111111-1111-4111-8111-111111111111','raw-one','any_tcp',false,'','192.0.2.30',443,false,'*',10443,true,'tcp','','none','observe','calendar_month','',2),
  ('44444444-4444-4444-8444-444444444444','11111111-1111-4111-8111-111111111111','raw-two','any_tcp',true,'','192.0.2.31',443,false,'*',10443,true,'tcp','','none','observe','calendar_month','',3),
  ('55555555-5555-4555-8555-555555555555','11111111-1111-4111-8111-111111111111','sni-one','sni',true,'duplicate.example','192.0.2.32',443,false,'*',10444,false,'tcp','','none','observe','calendar_month','',4),
  ('66666666-6666-4666-8666-666666666666','11111111-1111-4111-8111-111111111111','sni-two','sni',true,'duplicate.example','192.0.2.33',443,false,'*',10444,false,'tcp','','none','observe','calendar_month','',5);
INSERT INTO route_snis(route_id,node_id,listener_ip,listener_port,position,sni)
VALUES
  ('55555555-5555-4555-8555-555555555555','11111111-1111-4111-8111-111111111111','*',10444,0,'duplicate.example'),
  ('66666666-6666-4666-8666-666666666666','11111111-1111-4111-8111-111111111111','*',10444,0,'duplicate.example');
SQL
draft_overlap_ok=$(docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -XAt -U nodeflow -d "$fixture_db" -c \
  "SELECT (SELECT count(*)=2 FROM routes WHERE node_id='11111111-1111-4111-8111-111111111111' AND listener_port=10443 AND fallback) AND (SELECT count(*)=2 FROM route_snis WHERE node_id='11111111-1111-4111-8111-111111111111' AND listener_port=10444 AND sni='duplicate.example') AND to_regclass('public.route_snis_listener_sni_unique') IS NULL AND to_regclass('public.routes_listener_fallback_unique') IS NULL")
if [ "$draft_overlap_ok" != "t" ]; then
  echo "route semantics migration still blocks overlapping disabled drafts" >&2
  exit 1
fi

# A 000017 rollback must not turn an unconfirmed renewal candidate into an
# active legacy bearer after activated_at is removed.
docker exec -i -e PGPASSWORD=migration-test-password "$name" \
  psql -X -v ON_ERROR_STOP=1 -U nodeflow -d "$fixture_db" >/dev/null <<'SQL'
INSERT INTO enrollment_tokens(
  id,node_id,token_hash,token_prefix,expires_at
) VALUES(
  '77777777-7777-4777-8777-777777777777','11111111-1111-4111-8111-111111111111',
  repeat('1',64),'nfe_active',statement_timestamp()+interval '100 days'
);
INSERT INTO enrollment_tokens(
  id,node_id,token_hash,token_prefix,expires_at,activated_at,
  certificate_sha256,certificate_serial,certificate_not_after,
  renewal_id,predecessor_id,csr_sha256,csr_der,certificate_der,confirm_by
) VALUES(
  '88888888-8888-4888-8888-888888888888','11111111-1111-4111-8111-111111111111',
  repeat('2',64),'nfe_pending',statement_timestamp()+interval '100 days',NULL,
  repeat('3',64),'2',statement_timestamp()+interval '100 days',
  '99999999-9999-4999-8999-999999999999','77777777-7777-4777-8777-777777777777',
  repeat('4',64),decode('01','hex'),decode('02','hex'),statement_timestamp()+interval '1 day'
);
SQL
docker exec -i -e PGPASSWORD=migration-test-password "$name" \
  psql -X -v ON_ERROR_STOP=1 -1 -U nodeflow -d "$fixture_db" >/dev/null \
  < "$repo/migrations/000017_agent_credential_renewal.down.sql"
credential_rollback_ok=$(docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -XAt -U nodeflow -d "$fixture_db" -c \
  "SELECT (SELECT revoked_at IS NULL FROM enrollment_tokens WHERE id='77777777-7777-4777-8777-777777777777') AND (SELECT revoked_at IS NOT NULL FROM enrollment_tokens WHERE id='88888888-8888-4888-8888-888888888888') AND NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='enrollment_tokens' AND column_name='activated_at')")
if [ "$credential_rollback_ok" != "t" ]; then
  echo "credential renewal rollback exposed an unconfirmed candidate" >&2
  exit 1
fi

cp "$repo"/migrations/*.up.sql "$temporary/"
cat > "$temporary/000999_atomic_failure.up.sql" <<'SQL'
CREATE TABLE migration_atomic_probe(id integer);
SELECT 1 / 0;
SQL
if run_migrations "$temporary" >/dev/null 2>&1; then
  echo "intentionally failing migration unexpectedly succeeded" >&2
  exit 1
fi

atomic=$(docker exec -e PGPASSWORD=migration-test-password "$name" \
  psql -XAt -U nodeflow -d nodeflow -c \
  "SELECT to_regclass('public.migration_atomic_probe') IS NULL AND NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version='000999')")
if [ "$atomic" != "t" ]; then
  echo "failed migration was not rolled back atomically" >&2
  exit 1
fi

printf 'migrations=%s idempotent=yes lifecycle_backfill=yes route_semantics_backfill=yes route_fingerprint_projection=yes draft_overlap=yes quota_backfill_utc=yes credential_rollback_safe=yes atomic_failure_rollback=yes\n' "$count"
