# Headcount

Tracks Twitch channels that reach the global top 200 and scores each stream for
patterns that view-bots, follow-bots and chat-bots produce. The score is the sus
quotient. A high quotient means the data looks botted. It does not prove the
streamer bought bots.

The repository, Go module and Compose project are `headcount`. The database
(`susq`), its roles and the backup files keep their original names. Branding and interface rules are in
[docs/design-system.md](docs/design-system.md).

## What it collects

Once a channel enters the top 200 it is tracked from then on.

| Data | Source | Cadence |
|---|---|---|
| Top 200 live channels | Helix `Get Streams` | every minute |
| Viewer count, title, category for every tracked channel | Helix `Get Streams` (100 IDs per call) | every minute; cold tier every 10 minutes |
| Follower total | Helix `Get Channel Followers` (total only, app token) | live hot: 1 min, live tracked: 2 min, offline: hourly |
| Chat: messages, unique chatters, new chatters, repetition, bans | anonymous IRC | per minute, while live |
| Raids, chat modes (sub-only, emote-only, followers-only) | IRC notices | as they happen |
| Chatter account creation dates and lookup availability | Helix `Get Users` | new authors, omissions retried after 1–24 hours, found accounts refreshed after 7 days |
| Chat restrictions and Shared Chat sessions | public Helix endpoints, app token | low-priority polling, approximately every 10 minutes |
| Commands, configured utility/bot-badge activity and timing bins | IRC aggregates | per minute; no raw text retained |

Raw message text is never stored. Repetition is measured in memory per minute.

Counts can be delayed; the exact upstream refresh schedule is unknown. Repeated
values remain observations. Missing polls remain gaps, and follower totals are
analyzed as net changes over observed intervals.

### Tiers

- **hot**: in the top 200 this minute.
- **tracked**: has been in the top 200.
- **cold**: out of the top 200 for 7+ days with at least 5 scored streams and a
  channel quotient under 15. Polled every 10 minutes; chat is not joined.
  A cold channel returns to `tracked` if its quotient reaches 30, and to `hot`
  whenever it re-enters the top 200. Channels are never deleted.

## Scoring

When a stream ends (and every 15 minutes for streams live over an hour), these
detectors each rate one kind of evidence from 0 to 1:

| Component | Weight | Evidence |
|---|---|---|
| `periodic_spikes` | 0.6 | Upward viewer spikes that repeat at a fixed period and return to baseline |
| `synchronized_spikes` | 0.5 | Spikes that start in the same minutes as another channel's more often than chance (binomial test, Bonferroni-corrected; discounted when many channels spike together) |
| `spike_chat_lift` | 0.5 | Chat does not grow when viewers spike, comparing each unexplained spike with the minutes just before it; needs several material spikes |
| `chat_engagement` | 0.5 | Chatters per 1k viewers far below a peer baseline fitted on log(viewers), per language, adjusted for the stream's category |
| `follower_purge` | 0.4 | Large observed net follower losses; removal cause unknown |
| `chatter_accounts` | 0.4 | High share of resolved creation dates under 30 days old; requires 80% date coverage |
| `shared_accounts` | 0.4 | Chatters with accounts under 90 days old who also chatted in two or more other tracked channels in the week before |
| `follow_bursts` | 0.3 | Observed net-change intervals above the usual follow rate |
| `unexplained_jumps` | 0.3 | Sharp viewer changes with no raid or category change nearby |
| `spike_follow_lift` | 0.25 | Follows do not speed up when viewers spike (same per-spike comparison) |
| `chat_repetition` | 0.15 | Share of chat matching the most repeated non-command, non-utility message |
| `start_ramp` | 0.1 | Viewers arrive implausibly fast after going live |

Scoring version 9 groups shared causes conservatively: audience shape, weak
spike response and low engagement share an **audience** group; repetition and
account features share **chat**; net follower anomalies share **follows**. Each
group contributes its strongest weighted component. Groups combine by noisy-OR,
with each capped at 0.3 plus the strongest other group. A single group therefore
scores at most 30. These are dependency assumptions, not proof of independence.
Mechanism evidence indices are reported separately from the combined quotient.

**Observation coverage** replaces the public confidence wording. It uses actual
sampled viewer minutes, healthy chat minutes, follower sample coverage and
resolved account dates, with the existing three-hour duration scale. The JSON
`confidence` field remains for compatibility; stream scores also expose
`coverage` and `evidence`. Coverage is not detection accuracy. Insufficient
observations produce an explicit insufficient-evidence status. Changed count
readings, gaps, duplicate/delayed messages and account completion are reported
separately; a poll is not assumed to be an independent upstream refresh.

The chat baseline uses the same healthy paired raw minutes as target scoring,
with at least 100 viewers and 30 eligible minutes. It fits one past completed
stream per other channel in the prior seven days, by language (global fallback),
with category/family offsets shrunk by 10 pseudo-streams and capped at 1.5 SD.
Per-stream peer metrics are cached with their source timestamp; stale/reopened
streams are excluded. Historical rows without chat-health provenance do not
seed the new baseline.

Restrictions are applied to the affected episode windows. Public settings and
Shared Chat observations expire after 15 minutes. Known distribution or
collaboration context suppresses affected audience/follower evidence; account
cohorts are unavailable when their per-stream summary cannot isolate unaffected
minutes. Unknown ads, embeds, referrals and Drops remain unknown. Missing user
lookups never imply suspension and never raise account suspicion. Previously
known creation dates survive later lookup omissions.

A channel's quotient remains the coverage- and recency-weighted average of its
last ten streams, with a prior worth half a full-coverage stream at zero.
Scores from different versions should not be compared as if the scale were
calibrated. See [implementation coverage](docs/research-implementation.md) and
[private replay/evaluation](docs/research-workflow.md).

The weights are hand-set and uncalibrated. Bump `score.Version` when detector
logic changes.

## Layout

```
cmd/collector     discovery, polling, chat, follower sampling, scoring
cmd/api           read-only JSON API and dashboard
internal/helix    Helix client with a shared, prioritized rate-limit budget
internal/tracker  minute poller, stream sessions, tiers, follower sampler
internal/chat     IRC connection pool and per-minute aggregation
internal/detect   pure detector functions (tested on synthetic streams)
internal/score    loads streams, runs detectors, peer baseline, channel quotient
internal/store    Postgres/TimescaleDB access and migrations
web/static        dashboard (no build step)
```

## Pages and caching

Every dashboard page has its own URL: `/`, `/channel/{login}`, `/stream/{id}`,
`/category/{id}`, `/engagement`, `/about` and `/search`. The API renders
`web/static/index.html` as a template for each one, filling in the title,
description, canonical link, JSON-LD and a plain HTML summary, so crawlers and
link previews get the page's content without running the script. Search pages
and 404s are marked `noindex`. `/robots.txt` keeps crawlers out of `/api/`, and
`/sitemap.xml` lists every channel, categories from the last 90 days and streams
from the last 14 (rebuilt at most hourly). Old `#/` links are rewritten in the
browser.

Asset URLs carry `?v=` with a hash of all embedded files, and the API adds the
same version to the imports between modules. URLs with the current version are
cached for a year; anything else is revalidated. There's no version to bump by
hand.

## Running

Requires Docker. Create `.env` in the repository root:

```
TWITCH_CLIENT_ID=...
TWITCH_CLIENT_SECRET=...
POSTGRES_PASSWORD=...
API_DB_PASSWORD=...   # read-only role used by the dashboard API
```

The Twitch app needs no user authorization; it uses the client-credentials
flow. Then:

```sh
docker compose up -d --build
curl -s localhost:18090/healthz     # collector health
open http://localhost:18091/        # dashboard
```

Postgres listens on `127.0.0.1:55432`. Optional collector settings:
`TOP_N` (200), `END_GRACE_MINUTES` (4), `COLD_EVERY_MINUTES` (10),
`LOG_LEVEL=debug`, `SEED_LOGINS` (comma-separated channels to track before
they reach the top 200) `CHATTER_RETENTION_DAYS` (30; per-chatter rows are replaced by a summary),
`REPLAY_RETENTION_DAYS` (7; immutable score snapshots), and `UTILITY_BOT_IDS`
(comma-separated, operator-confirmed utility account IDs). Utility activity is
metadata, not proof that other accounts are human. Zero disables the respective
retention job. Export selected private research packets before expiry.

The API connects as the read-only `susq_api` role, which the collector enables
from `API_DB_PASSWORD`. It limits `/api/` requests to `RATE_PER_MINUTE` (120)
per client with bursts of `RATE_BURST` (60). Behind a reverse proxy, set
`TRUSTED_PROXIES` to its addresses so `X-Forwarded-For` is used.

The `backup` service writes a nightly `pg_dump -Fc` to `./backups`, keeping 7
days. On the homelab deployment, Dozor pulls a copy each night and alerts if
it is older than 36 hours.

### Tests

```sh
go test ./...
```

## Known limits

- Helix viewer counts are cached for several minutes, so spikes shorter than
  that are blurred or missed.
- Ad breaks, front-page placement, embeds and chat restrictions change these
  numbers for innocent reasons. Observed raids, category changes, chat modes and Shared Chat are considered;
  unknown distribution remains a confounder.
- A third party can view-bot a channel to get it penalized.
- Category families are a fixed list of non-game categories; every other
  category counts as a game. A category that is mostly botted can still pull
  its own baseline down, up to the 1.5 SD cap.
- Shared accounts only sees message authors in tracked channels. Raid trains and
  friend groups also share chatters; the young-account filter reduces, but
  does not remove, that overlap.

## License

Original contributions in this repository are licensed under the GNU General
Public License, version 3 or (at your option) any later version
(`GPL-3.0-or-later`). See [LICENSE](LICENSE).

Third-party code, documentation, fonts, data, and other assets retain their own
licenses, copyright notices, and attribution. This license does not replace
those terms or grant rights to upstream material that its authors have not granted.
