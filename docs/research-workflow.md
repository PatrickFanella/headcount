# Private replay and evaluation

Scoring version 9 implements the measurement corrections from the [research report](../reports/Twitch%20bot%20detection%20research.md). The live quotient remains an uncalibrated index. Calibration artifacts are offline candidates and do not change public scores.

## Collection and interpretation

The collector records whole-minute health, duplicate/delayed messages, total source delay, commands, configured utility activity and Twitch bot badges. IDs are deduplicated for ten minutes with a 200,000-entry bound. Messages arriving outside their source minute are counted as delayed, excluded from current activity, and invalidate that minute for scoring. Missing source timestamps use receive time. Whole-minute health requires a current confirmed join and a continuously receiving connection. Unobserved zero activity is never manufactured.

Active-minute counts and four within-minute message-gap bins (under 1 second, 1–5, 5–15, 15+) are folded into existing per-stream author rows. They do not reveal video playback or watch duration. Timing across minute boundaries is deliberately not inferred. Commands and operator-confirmed utility text are excluded from the repeated-text numerator. Badge absence does not imply humanity; no vendor bot list is imported.

Successful `Get Users` omissions mean unavailable for an unknown reason. Retries back off from one hour to at most 24 hours. Found accounts refresh after seven days. Background refresh is bounded to 100 due IDs per ten-second pass and accounts referenced in the last week; normal new-author lookups share the existing low-priority budget. Transport/API failures are requeued without creating omission evidence. Immutable creation dates survive later omissions. Historical observations retain what was returned at lookup time.

All observed authors remain in account denominators. Pending lookups, available creation dates and unavailable responses are separate counts and can overlap: a previously dated account can become unavailable. Age-based scoring requires at least 80% creation-date coverage; this is a conservative operational guard, not a calibrated cutoff. Legacy summaries without their true denominator remain unknown. Configured utility activity does not contribute to young-account or shared-account evidence.

Chat settings and Shared Chat use app-token reads under the low-priority request budget, at most four channel attempts per five-second pass with a ten-minute refresh target. Large tracked populations or exhausted budget can delay observations; polled observations expire after 15 minutes in scoring. IRC modes can persist across continuous healthy coverage, and must be confirmed again after a collection gap. Phone/email verification and other unexposed settings remain unknown. The implementation follows the [official endpoint contracts](https://dev.twitch.tv/docs/api/reference/#get-chat-settings) and [Shared Chat contract](https://dev.twitch.tv/docs/api/reference/#get-shared-chat-session); it does not assume a precise upstream count-refresh cadence.

## Frozen evidence

Each scoring pass reads inputs through one read-only repeatable-read database transaction. It refreshes the stream reference inside that transaction. The snapshot includes raw aggregate series, event observations, exact past peer inputs, account counts, compact behavior metrics, shared-account results and synchrony partners. It contains no raw message text, viewer identities, credentials or per-author IDs. Channel IDs and aggregate audience/account information still make research packets private.

The score and its immutable input/output snapshot are saved together. Reusing the same stream/time/version identity with different evidence fails. Peer metrics use the same healthy raw paired-minute calculation as target engagement; source timestamps prevent reuse after streams grow or reopen. Baselines use one completed past stream per other channel, avoiding self-inclusion and repeated-channel dominance. Creation-cohort/message/activity comparisons use matched language/category/audience peers and up to ten comparable earlier broadcasts from the same channel. They are descriptive experimental metrics, with reference counts, not additional score weights.

The public API role cannot read `score_snapshots` or `user_observations`. The public stream response exposes only the current score, quality metadata, aggregate diagnostics and the three mechanism indices. Existing `confidence` JSON fields are retained for compatibility; the UI calls them observation coverage. A mechanism index is its strongest weighted detector, before the overall single-group cap, and is not a probability.

`CHATTER_RETENTION_DAYS` defaults to 30 and governs author rows and lookup-history retention. New summaries preserve true totals, creation-date resolution and pending counts. `REPLAY_RETENTION_DAYS` defaults to 7 and governs snapshots, which can be much larger. Zero disables the respective pruning. Cached scalar peer/behavior metrics remain with stream records. Measure database growth before lengthening snapshot retention. Export selected packets before expiry; exports are not automatically pruned.

## Export and replay

Build the tool with `go build -o /tmp/headcount-research ./cmd/research`. It also ships in the existing container image because the Dockerfile builds all commands. `DATABASE_URL` is used only by export and the explicit context-writing command. Keep it in the existing environment/secret store.

```sh
mkdir -p research_exports/pool
/tmp/headcount-research export -stream 123456 -out research_exports/pool/stream-123456.json
/tmp/headcount-research replay -in research_exports/pool/stream-123456.json -out research_exports/replayed.json
/tmp/headcount-research replay -in research_exports/pool/stream-123456.json -omit audience -out research_exports/without-audience.json
/tmp/headcount-research review -in research_exports/pool/stream-123456.json -out research_exports/blinded.json
```

Use `-at RFC3339Nano` on export to select an exact stored observation rather than the latest. JSON packets carry a SHA-256 digest of canonical input. This detects accidental changes after export; it is not an authenticated provenance signature. Output files use mode 0600 and exclusive creation. They never overwrite a packet or reviewed labels. Replay uses the current scorer version and records omitted components/groups; the stored original output remains in the exported packet.

Use the blinded packet for reviewers. It excludes the original quotient, detector strengths and score-derived shared-account result. Do not show reviewers scored exports, public rankings or the case-selection rationale before their first review.

## Owner-nominated reference channels

The [control roster](research-controls.json) records HasanAbi, MapleTakes, Central_Committee, willneff and carolinekwan as owner-nominated reference creators, with identities checked through Twitch's user endpoint. The owner confirmed `central_committee` as the intended account for the supplied `central_commmittee` handle. Identity lookup confirms an account exists, not that its traffic is authentic.

A trusted creator is a useful starting point for finding ordinary episodes. Third parties can direct bots at an unwilling creator, as [Twitch describes](https://blog.twitch.tv/en/2016/06/17/on-artificial-viewers-followers-and-chat-activity-317906f1c7ea/). The roster supplies no automatic negative label, score exemption or special weight. Review normal broadcasts and annotated raids, collaborations, restricted-chat periods and promotions independently of the quotient. Leave unexplained traffic indeterminate. Similar audiences and joint events can produce legitimate overlap.

```sh
/tmp/headcount-research control-cases -pool research_exports/pool -controls docs/research-controls.json -out research_exports/control-cases.json
```

This command selects one latest available snapshot per stream for resolved channel IDs and creates unreviewed challenge cases. It does not begin collection or contact Twitch. Add resolved logins to the existing `SEED_LOGINS` configuration when collecting below the discovery threshold; deployment and seed changes are separate operational actions. Export snapshots using the normal workflow. Nomination metadata stays out of blinded review packets, and two independent episode reviews are still required.

Use these cases to inspect false positives and test whether known ordinary events receive sensible explanations. Expand the representative sample across audience sizes, languages and categories. Keep related campaigns and channels out of other evaluation partitions. Independently supported positive incidents are also needed to estimate missed detection; low scores or familiar creators cannot establish that metric. Never derive ground-truth labels from the score being evaluated. Individual video-viewer identities and playback sessions remain unavailable through public data, so Headcount does not certify individual viewers as human.

## Representative sampling and labels

```sh
/tmp/headcount-research sample -pool research_exports/pool -n 50 -seed 17 -out research_exports/cases.json
# For a deliberately selected challenge example instead:
/tmp/headcount-research case -in research_exports/pool/stream-123456.json -out research_exports/challenge-case.json
```

Sampling selects one latest snapshot per stream without reading its score. It records the inclusion probability `n / unique_streams_in_pool`. The frame is the supplied monitored snapshot pool, not all Twitch. Keep a random sample including low scores separate from an enriched challenge collection. Deliberately selected examples default to `challenge: true`. Do not change their selection probability to pretend they were sampled randomly.

For each case, set a stable campaign group when connected operations are known and assign a language/category/coverage stratum if useful. The evaluator also reports automatic language, category, audience-size and coverage subgroups. Set `split` to `development`, `validation`, `calibration` or `test`. Splits must be forward in time, including the latest evidence-availability time, and cannot share channels, streams or known campaigns. All snapshots of a stream belong to the same group. Historical rescoring performed after a test period is not prospective training evidence.

Each entry has `reviews: []`. At least two distinct reviewers must independently add a review with all three target labels and nonempty source references:

```json
{
  "reviewer": "reviewer-one",
  "labels": {
    "audience": "indeterminate",
    "chat": "authorized",
    "follows": "ordinary"
  },
  "sources": ["incident or VOD reference and reviewed time interval"],
  "notes": "Describe the evidence and uncertainty."
}
```

Allowed labels are `consistent` (evidence consistent with manipulation for that target), `ordinary`, `authorized` and `indeterminate`. These are reviewed episode interpretations, not platform ground truth. Authorized automation is a negative for abusive automation; it does not certify that the account is human. Reviewers may leave invisible viewer composition unresolved. Disagreement stays unresolved unless an `adjudication` review supplies an explicit label and provenance. Retain both original reviews.

Current account/behavior inputs summarize a whole stream. The evaluation tool therefore evaluates whole observed-stream episodes and requires one snapshot per stream for accuracy metrics. Record narrower incident boundaries in review notes and use the separate chronological persistence command for repeated snapshots. Do not claim localized account-cohort accuracy from a whole-stream aggregate. Quiet music, large raids, co-streams, copypasta, restrictions, fresh-community arrivals, known promotions/Drops, utility bots and attacks on unwilling broadcasters belong in the challenge set.

## Evaluation and calibration candidates

```sh
/tmp/headcount-research evaluate -in research_exports/cases.json -split test -target chat -threshold 0.3 -coverage 0.1 -out research_exports/chat-test.json
/tmp/headcount-research evaluate -in research_exports/cases.json -split test -target chat -omit chatter_accounts -out research_exports/chat-ablation.json
/tmp/headcount-research persistence -pool research_exports/pool -cases research_exports/cases.json -target audience -out research_exports/persistence.json
/tmp/headcount-research calibrate -in research_exports/cases.json -target chat -out research_exports/chat-calibration.json
/tmp/headcount-research probability-evaluate -in research_exports/chat-calibration.json -cases research_exports/cases.json -out research_exports/chat-probability-test.json
```

Targets are `audience`, `chat` and `follows`. Evidence thresholds are on the uncalibrated 0–1 mechanism scale. Choose thresholds on development/validation data, freeze them, then evaluate on the later test partition. The software cannot make an already inspected test set unseen again.

Reports separate weighted representative metrics from unweighted challenge results. They include precision, recall, precision–recall points, abstention, reviewed/unreviewed counts, total alert workload, false alerts per reviewed stream/hour and subgroup results. Unknown labels are excluded from accuracy estimates; abstentions count against recall. Precision intervals resample connected channel/campaign groups 500 times and are omitted when support is inadequate. They do not establish validity of the reviewed label source. Monitor uncertainty and increase the sample until it is useful; no fixed sample size establishes representativeness.

Persistence replays snapshots chronologically, splits alerts at abstentions or gaps, and carries a state forward for at most 30 minutes. It reports alert episodes, reviewed false episodes, monitored hours and longest alert duration. It accepts whole-stream review labels, not minute-level inferred truth.

Isotonic calibration uses only reviewed representative calibration cases above the coverage floor, with both classes and at least two independent groups. Those checks are prerequisites, not a claim of adequate sample size. Its artifact records target, scorer version, training cases/channels/campaigns and latest evidence time. Probability evaluation rejects overlapping or nonfuture test cases and reports Brier score, clipped log loss and reliability bins. Any resulting probability concerns the reviewed label, never fake viewer share. No calibration artifact is loaded by the collector or dashboard.

The circular-shift synchrony statistic and the old interpolated-periodicity comparison are shadow diagnostics. Circular shifting preserves onset spacing but cannot preserve unknown partner gaps and all common events. Neither diagnostic gets a new live weight. Compare matched benign intervals and future campaigns before promoting a different null model or classifier.

## Rollout and rollback

Migration `007_research_evidence.sql` is additive. Before production rollout, take the existing logical backup, stop the old collector, apply the migration using the new collector, then run the matching API build. The column additions, user-cache backfill and indexes acquire database locks, so use a maintenance window appropriate to the table sizes. Keep the old image and backup.

Version 9 triggers rescoring of finished streams. Historical chat without health provenance and pruned legacy account summaries will have unavailable evidence, not invented coverage. New peer baselines build from newly scored healthy streams. Expect scores and availability to change; this is not an accuracy improvement established by tests.

For application rollback, stop the new collector/API and restore the old images; additive columns/tables can remain. Old collectors leave new health fields unknown, so do not mix their collection into a claimed version-9 healthy corpus. Restoring the database backup is a separate recovery action that would discard later observations and must be reviewed for that interval. Exported research packets remain independent of application rollback.

Production deployment, contemporary review labels, prospective accuracy, a fitted live probability model, raw-chat corpora, authenticated viewer telemetry and verified private referral/ad evidence are not supplied by this implementation.
