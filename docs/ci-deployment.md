# Dashboard CI delivery

The existing required `CI / Verify` job retains module verification, vet, race
and disposable database integration tests, and builds of every command. It also
builds the actual production image and exercises its API against an isolated
TimescaleDB fixture using the read-only `susq_api` role. This checks every static
asset, the native JavaScript import versioning, rendered pages, health and the
uncached build marker. No collector or production credential is used in the fixture.

Successful canonical-main pushes and manual workflow dispatches receive and
activate only service `api` in Compose project `headcount` on Kvant. The existing
collector, database, volume and backup service retain their native procedures.
The image still defaults to the collector for those procedures; the reviewed
API baseline explicitly selects `/usr/local/bin/api`. CI never starts a collector.

## Data admission

The fixed `scripts/predeploy-ci.py` hook is installed from the exact green merged
source at its registered hash/path. A missing hook fails before activation. It
checks the original private environment-file hash, API startup, absence of
mounts, and the accepted image's canonical source/revision labels. Its migration
file hashes must equal the reviewed native contract, and the live migration
ledger must contain exactly those names. Migration-changing releases require
native collector/data admission and a reviewed registration update first.

A read-only database metadata transaction checks the API role's effective
privileges: no superuser, role/database creation, replication or RLS bypass;
no database/schema creation, writable persistent tables/sequences or executable
application security-definer functions. The role must default to read-only
transactions, and neither private research table may be selected. CI does not
apply migrations, alter roles, call a provider, or modify production records.

This API has no persistent mounts and cannot write production data under the
admitted role. Image promotion therefore does not run a new database restore or
change the native backup schedule. Collector releases and database upgrades keep
their existing backup/restore and provider qualification requirements.

## Identity and recovery

`/build-info.json` embeds the exact source commit during image construction and
always returns `Cache-Control: no-store`, including requests with an asset-version
query. Dashboard HTML is rendered from live data; its hash is not a build identity.
After activation, CI independently checks the public marker, health and selected
brand icons. The first activation uses the legacy root/health checks so rollback
can still verify the original image. After independent live verification, the
operator adds an image-backed marker check and retains the legacy profile for
recovery to that original image. Later image rollbacks use their own embedded marker.

The scoped SSH key permits only Kvant's verified LAN/default Docker bridge
sources, the pinned receiver and the `headcount-api` profile. It refuses a shell
and collector selection. Gitea stores only `HOMELAB_DEPLOY_KEY` and
`HOMELAB_KNOWN_HOSTS`; API and collector credentials stay in the existing private
`/home/onnwee/Work/Subcult/sus-quotient/.env`. The baseline interpolates only the
API password and trusted-proxy setting, without loading that file as an API env_file.
The homelab operator owns key review/rotation on February 3, 2027.

Private profile and baseline files are `~/.config/fleet-deploy/headcount-api.json`
and `headcount-api-compose.json`. Registration and receipts live under
`~/.local/state/fleet-deploy/headcount-api/`. Current-main checks, an application
lock, immutable accepted artifacts and a runtime guard precede promotion. An
activation failure restores the prior immutable API image and verifies its public
checks. This does not roll back a database.

After main CI succeeds, independently compare the public marker and packaged
assets, page metadata, native environment/runtime settings and collector,
database, backup and other service identities. Data-dependent dashboard content
may change normally while the collector continues operating.
