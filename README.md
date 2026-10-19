# sus quotient

Tracks Twitch channels that reach the global top 200 and scores each stream for
patterns that view-bots, follow-bots and chat-bots produce. A high quotient
means the data looks botted. It does not prove the streamer bought bots.

## What it collects

Once a channel enters the top 200 it is tracked from then on.

| Data | Source | Cadence |
|---|---|---|
| Top 200 live channels | Helix `Get Streams` | every minute |
| Viewer count, title, category for every tracked channel | Helix `Get Streams` (100 IDs per call) | every minute; cold tier every 10 minutes |
| Follower total | Helix `Get Channel Followers` (total only, app token) | live hot: 1 min, live tracked: 2 min, offline: hourly |
| Chat: messages, unique chatters, new chatters, repetition, bans | anonymous IRC | per minute, while live |
| Raids, chat modes (sub-only, emote-only, followers-only) | IRC notices | as they happen |
| Chatter account creation dates | Helix `Get Users` | once per new chatter |

Raw message text is never stored. Repetition is measured in memory per minute.

Helix refreshes `viewer_count` and follower totals only every few minutes, so
repeated values are treated as "no new reading" and interpolated.

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
| `follower_purge` | 0.4 | Large follower drops (Twitch removing fake follows) |
| `chatter_accounts` | 0.4 | High share of chatters with accounts under 30 days old, or suspended/deleted |
| `shared_accounts` | 0.4 | Chatters with accounts under 90 days old, or suspended, who also chatted in two or more other tracked channels in the week before |
| `follow_bursts` | 0.3 | Minutes with far more follows than the stream's normal rate |
| `unexplained_jumps` | 0.3 | Sharp viewer changes with no raid or category change nearby |
| `spike_follow_lift` | 0.25 | Follows do not speed up when viewers spike (same per-spike comparison) |
| `chat_repetition` | 0.15 | Share of chat that is one repeated message |
| `start_ramp` | 0.1 | Viewers arrive implausibly fast after going live |

They combine as a weighted noisy-OR: `quotient = 100 × (1 − Π(1 − eᵢ))` with
`eᵢ = wᵢ·sᵢ`. Detectors are grouped into families that read the same evidence
(viewer shape, spike response, chat, accounts, follows). The strongest
family's combined evidence is capped at 0.3 plus the strongest evidence from
another family, so one line of evidence gives at most 30. Spike-based
detectors are unavailable when the median change between Helix updates
exceeds 5%, as for channels whose count jumps randomly on every refresh. Confidence is the share of total weight that
could be evaluated, scaled by observed minutes (full at 3 hours). A channel's
quotient is the confidence- and recency-weighted average of its last ten
streams, with a prior worth half a full-confidence stream at 0.

The chat baseline is a log-log line per language (global when a language has
fewer than 15 peer streams in 7 days). Each stream's expectation is then shifted
by offsets for its category family (social, creative, gambling, sports, games)
and its main category. Offsets are shrunk towards zero by 10 pseudo-streams,
fitted without outliers, and capped at 1.5 SD in total.

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
they reach the top 200) and `CHATTER_RETENTION_DAYS` (30; per-chatter rows of
older streams are replaced by a summary).

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
  numbers for innocent reasons. Raids, category changes and sub-only or
  emote-only chat are accounted for; the rest are not.
- A third party can view-bot a channel to get it penalized.
- Category families are a fixed list of non-game categories; every other
  category counts as a game. A category that is mostly botted can still pull
  its own baseline down, up to the 1.5 SD cap.
- Shared accounts only sees chatters in tracked channels. Raid trains and
  friend groups also share chatters; the young-account filter reduces, but
  does not remove, that overlap.
