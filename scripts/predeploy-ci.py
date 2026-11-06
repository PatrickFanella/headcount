#!/usr/bin/env python3
"""Admit only a dashboard API image against the unchanged native data contract."""
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
from urllib.parse import urlsplit

STATE = Path.home() / '.local/state/fleet-deploy/headcount-api'
SOURCE = 'https://git.subcult.tv/subculture-collective/headcount'
SQL = """
BEGIN READ ONLY;
SELECT json_build_object(
 'role',rolname,'login',rolcanlogin,'superuser',rolsuper,
 'create_role',rolcreaterole,'create_database',rolcreatedb,
 'replication',rolreplication,'bypass_rls',rolbypassrls,
 'database_create',has_database_privilege(rolname,current_database(),'CREATE'),
 'writable_schemas',(SELECT count(*) FROM pg_namespace n WHERE n.nspname NOT LIKE 'pg_%'
   AND n.nspname <> 'information_schema' AND has_schema_privilege('susq_api',n.oid,'CREATE')),
 'writable_tables',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema' AND c.relkind IN ('r','p','f','v','m')
   AND has_table_privilege('susq_api',c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')),
 'writable_sequences',(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema' AND c.relkind='S'
   AND CASE WHEN c.relkind='S' THEN has_sequence_privilege('susq_api',c.oid,'USAGE,UPDATE') ELSE false END),
 'security_definer_functions',(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
   WHERE n.nspname NOT LIKE 'pg_%' AND n.nspname <> 'information_schema' AND p.prosecdef
   AND has_function_privilege('susq_api',p.oid,'EXECUTE')),
 'read_only_default',coalesce('default_transaction_read_only=on'=ANY(rolconfig),false),
 'private_observations_select',has_table_privilege(rolname,'public.user_observations','SELECT'),
 'private_snapshots_select',has_table_privilege(rolname,'public.score_snapshots','SELECT'),
 'applied_migrations',(SELECT json_agg(name ORDER BY name) FROM public.schema_migrations))
FROM pg_roles WHERE rolname='susq_api';
ROLLBACK;
"""


def run(args, data=None):
    result = subprocess.run(args, input=data, capture_output=True, timeout=30)
    if result.returncode:
        raise RuntimeError('Headcount admission command failed')
    return result.stdout


def inspect(name, kind='container'):
    return json.loads(run(['docker', kind, 'inspect', name]))[0]


def admit(metadata, migrations):
    assert metadata['role'] == 'susq_api' and metadata['login'] is True
    assert metadata['read_only_default'] is True
    for key in ['superuser', 'create_role', 'create_database', 'replication', 'bypass_rls',
                'database_create', 'private_observations_select', 'private_snapshots_select']:
        assert metadata[key] is False, 'Native API privilege changed: ' + key
    for key in ['writable_schemas', 'writable_tables', 'writable_sequences', 'security_definer_functions']:
        assert metadata[key] == 0, 'Native API privilege changed: ' + key
    assert set(metadata['applied_migrations']) == set(migrations), 'Native migration admission is required'


def main():
    registration = json.loads((STATE / 'registration.json').read_text())
    assert hashlib.sha256(Path(registration['env_file']).read_bytes()).hexdigest() == registration['env_file_sha256']
    api = inspect('headcount-api-1')
    assert api['Config']['Entrypoint'] == ['/usr/local/bin/api'] and api['Config']['Cmd'] is None
    assert api['Config']['User'] == registration['startup']['User'] and not api['Mounts']
    assert api['HostConfig'].get('Tmpfs', {}) == registration['tmpfs']
    environment = dict(value.split('=', 1) for value in api['Config']['Env'])
    database = urlsplit(environment['DATABASE_URL'])
    assert (database.username, database.hostname, database.path) == ('susq_api', 'db', '/susq')
    revision = run(['git', 'ls-remote', SOURCE + '.git', 'refs/heads/main']).decode().split()[0]
    assert re.fullmatch(r'[0-9a-f]{40}', revision)
    candidate = inspect('fleet-deploy/headcount-api/api:' + revision, 'image')
    labels = candidate['Config']['Labels']
    assert labels['org.opencontainers.image.source'] == SOURCE
    assert labels['org.opencontainers.image.revision'] == revision
    archive = run(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--entrypoint', 'tar',
                   candidate['Id'], '-C', '/usr/local/share/headcount/migrations', '-cf', '-', '.'])
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        migrations = {m.name.removeprefix('./'): hashlib.sha256(tar.extractfile(m).read()).hexdigest()
                      for m in tar.getmembers() if m.isfile()}
    assert migrations == registration['migration_sha256'], 'Migration-changing releases require native collector/data admission'
    metadata = json.loads(run(['docker', 'exec', '-i', 'headcount-db-1', 'psql', '-X', '-qAt',
                              '-U', 'susq', '-d', 'susq', '-v', 'ON_ERROR_STOP=1'], SQL.encode()))
    admit(metadata, migrations)
    print(json.dumps({'result': 'PASS', 'source_commit': revision, 'read_only_role': 'susq_api',
                      'unchanged_migrations': len(migrations), 'collector_started': False,
                      'database_mutated': False}))


if __name__ == '__main__':
    main()
