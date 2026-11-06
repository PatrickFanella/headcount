#!/usr/bin/env python3
"""Exercise the actual dashboard image with disposable data and a read-only role."""
import io
import json
import re
import subprocess
import sys
import tarfile
import time
import uuid

POSTGRES = 'timescale/timescaledb:latest-pg17@sha256:b346edcdb51a1fd6020e3965e0bd1c9f3406fa6d5fbce1e28f4852587ef934e2'


def run(args, data=None, timeout=45):
    result = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError('Image check command failed')
    return result.stdout


def image_files(image, directory):
    data = run(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--entrypoint', 'tar', image, '-C', directory, '-cf', '-', '.'])
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        return {m.name.removeprefix('./'): archive.extractfile(m).read() for m in archive.getmembers() if m.isfile()}


def main(image, revision):
    assert re.fullmatch(r'[0-9a-f]{40}', revision)
    metadata = json.loads(run(['docker', 'image', 'inspect', image]))[0]
    assert metadata['Config']['Labels']['org.opencontainers.image.revision'] == revision
    assert metadata['Config']['Labels']['org.opencontainers.image.source'] == 'https://git.subcult.tv/subculture-collective/headcount'
    image = metadata['Id']
    files = image_files(image, '/usr/local/share/headcount/static')
    migrations = image_files(image, '/usr/local/share/headcount/migrations')
    expected = (json.dumps({'schema_version': 1, 'source_commit': revision}, separators=(',', ':')) + '\n').encode()
    assert files['build-info.json'] == expected
    network = pg = api = None
    try:
        network = run(['docker', 'network', 'create', '--internal', 'headcount-ci-' + uuid.uuid4().hex[:12]]).decode().strip()
        pg = run(['docker', 'run', '-d', '--rm', '--init', '--network', network, '--network-alias', 'database',
                  '--memory', '1g', '--cpus', '2', '--tmpfs', '/var/lib/postgresql/data:rw,size=256m',
                  '-e', 'POSTGRES_USER=headcount_test', '-e', 'POSTGRES_PASSWORD=headcount_test', '-e', 'POSTGRES_DB=headcount_test',
                  '-e', 'TIMESCALEDB_TELEMETRY=off', '-e', 'TS_TUNE_MEMORY=1GB', '-e', 'TS_TUNE_NUM_CPUS=2',
                  '--entrypoint', '/usr/bin/timeout', POSTGRES, '-s', 'TERM', '-k', '5', '150', 'docker-entrypoint.sh', 'postgres']).decode().strip()
        for attempt in range(60):
            if subprocess.run(['docker', 'exec', pg, 'pg_isready', '-h', '127.0.0.1', '-U', 'headcount_test', '-d', 'headcount_test'], capture_output=True).returncode == 0:
                break
            time.sleep(0.3)
        else:
            raise RuntimeError('Own disposable PostgreSQL did not become ready')
        sql = ['docker', 'exec', '-i', pg, 'psql', '-X', '-U', 'headcount_test', '-d', 'headcount_test', '-v', 'ON_ERROR_STOP=1']
        run(sql, b'CREATE TABLE schema_migrations(name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());')
        for name, body in sorted(migrations.items()):
            assert re.fullmatch(r'[A-Za-z0-9_.-]+\.sql', name)
            run(sql, body)
            run(sql, ("INSERT INTO schema_migrations(name) VALUES ('" + name + "');").encode())
        run(sql, b"ALTER ROLE susq_api WITH LOGIN PASSWORD 'headcount-ci-only-password'; ALTER ROLE susq_api SET default_transaction_read_only=on;")
        api = run(['docker', 'create', '--rm', '--init', '--network', network, '--read-only', '--memory', '512m', '--cpus', '1',
                   '--pids-limit', '160', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
                   '--tmpfs', '/tmp:uid=10001,gid=10001,mode=1777,size=32m',
                   '-e', 'DATABASE_URL=postgres://susq_api:headcount-ci-only-password@database:5432/headcount_test?sslmode=disable',
                   '--entrypoint', '/usr/bin/timeout', image, '-s', 'TERM', '-k', '5', '90', '/usr/local/bin/api']).decode().strip()
        run(['docker', 'start', api])
        def fetch(path):
            result = subprocess.run(['docker', 'exec', api, 'wget', '-S', '-T', '5', '-O', '-', 'http://127.0.0.1:8081' + path], capture_output=True, timeout=12)
            assert result.returncode == 0, path
            return result.stdout, result.stderr.lower()
        for attempt in range(30):
            try:
                body, headers = fetch('/healthz')
                assert body == b'ok'
                break
            except AssertionError:
                time.sleep(0.3)
        else:
            raise RuntimeError('Read-only API did not become ready')
        body, _ = fetch('/')
        version = re.search(rb'/app\.js\?v=([0-9a-f]+)', body)[1]
        assert b'Headcount' in body and b'https://headcount.subcult.tv/' in body
        for query in ['', '?v=' + version.decode(), '?v=stale']:
            body, headers = fetch('/build-info.json' + query)
            assert body == expected and b'cache-control: no-store' in headers
        for name, original in files.items():
            if name == 'index.html':
                continue  # Its data-dependent pages were rendered above.
            expected_asset = original
            if name.endswith('.js'):
                expected_asset = re.sub(rb"(from\s+'\./[\w-]+\.js)'", lambda m: m[1] + b'?v=' + version + b"'", original)
            body, _ = fetch('/' + name)
            assert body == expected_asset, name
        assert fetch('/favicon.ico')[0] == files['brand/favicon.ico']
        for path in ['/about', '/engagement', '/search?q=fixture']:
            assert b'https://headcount.subcult.tv' in fetch(path)[0], path
        assert json.loads(fetch('/api/overview')[0])['channels'] == 0
        print(json.dumps({'result': 'PASS', 'public_assets': len(files) - 1, 'marker_no_store_all_queries': True,
                          'read_only_api': True, 'collector_started': False}))
    finally:
        for cid in [api, pg]:
            if cid:
                subprocess.run(['docker', 'rm', '-f', cid], capture_output=True)
        if network:
            subprocess.run(['docker', 'network', 'rm', network], capture_output=True)


if __name__ == '__main__':
    assert len(sys.argv) == 3
    main(sys.argv[1], sys.argv[2])
