# Research implementation

Branch: `feat/research-informed-scoring`. Source: [Twitch bot detection research](../reports/Twitch%20bot%20detection%20research.md).

The quotient remains an uncalibrated anomaly index. It does not estimate fake audience share or responsibility. Viewer, chat and follow evidence are reported separately.

## Work and acceptance

- [x] Account lifecycle: bounded omission retries, refreshes, immutable creation dates, historical observations, complete denominators and retention summaries. An omitted user is unavailable, never presumed suspended.
- [x] Collection: bounded message-ID deduplication, source-time delay accounting, explicit whole-minute health, command/utility/badge counts and compact per-author active-minute/gap summaries. No raw text or cross-minute text hashes retained.
- [x] Context: app-token chat settings and Shared Chat polling, episode restrictions, collaboration exclusions and expiring manual distribution context in replay annotations. Unknown referrals, Drops and ads stay unknown.
- [x] Scoring: conservative shared-cause grouping, separate mechanism summaries, explicit insufficient evidence, observed coverage, no interpolation across missing polls, interval-based follower changes, raw matched engagement, past-only target-excluded channel-balanced peers.
- [x] Research features: account cohorts, author message/activity distributions and degree-aware overlap diagnostics saved without new scoring weights.
- [x] Replay/evaluation: immutable versioned score snapshots with exact inputs and provenance; offline replay and ablations; blinded review packets, reviewer/adjudication labels, grouped forward-time splits, precision/recall/PR, review workload, subgroup reports, grouped uncertainty and calibration diagnostics. No claim of real-world validation without real labels.
- [x] Verification: regression tests for each correction, database migration/integration checks when available, full Go tests/vet/race, dashboard syntax and behavior checks, documented rollout/rollback and remaining empirical work.

## External evidence still needed

Reviewed contemporary episodes and a later held-out period are needed to establish accuracy or fit a probability model. Platform-private viewer sessions/IP/device/referral data are unavailable. Broadcaster-authorized ad/referral evidence and consented raw-chat corpora are not collected by this branch. Synthetic fixtures establish software behavior only.

## Validation log

Completed on the new branch. No production deployment performed.

- Full `go test ./...`, including tests against an isolated TimescaleDB/PostgreSQL 17 server: passed.
- `go test -race ./...` with the integration database enabled: passed.
- `go vet ./...` and syntax checks for app.js, kit.js and viz.js: passed.
- Fresh-database migrations, repeated migration application, account retry/retention, private API grants, idempotent chat writes, sourced context, snapshot collision handling and repeatable reads: passed.
- End-to-end scoring persisted a version 9 packet, replayed it exactly, then retained that result after later database observations changed.
- Collaborative-browser stream view and API endpoint checks: passed; no invalid SVG coordinates or console errors observed.
- Read-only live Twitch preflight: app-token stream/settings/Shared Chat requests succeeded; anonymous IRC received ROOMSTATE and messages. No raw text persisted.

The workstation's inherited GOROOT points one directory above Homebrew's libexec. Checks used command-local `GOROOT=/opt/homebrew/opt/go/libexec`; host configuration was not changed. Test database/container and temporary UI harness are removed at handoff.

The research CLI supports whole observed-stream episode labels. Finer account-cohort episodes cannot be reconstructed from existing per-stream summaries. Circular-shift and cohort comparisons remain diagnostics rather than newly weighted detectors. Isotonic calibration is an offline candidate with independent-test checks; no model is promoted to live scoring without reviewed evidence.

## Owner-nominated controls follow-up

Added a versioned creator roster with verified public Twitch IDs, unresolved-handle tracking, and a `control-cases` command. Nominations produce unreviewed challenge cases and cannot change a score or supply a class label. Nomination metadata is excluded from blinded packets. Regression tests cover those boundaries.
