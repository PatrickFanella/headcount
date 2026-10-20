# Empirical bot detection and validation for Headcount

Research checked 2026-10-19. This is a research proposal, not a change to detector logic or a claim that the proposed features are validated on current Twitch.

## 1. What has research actually demonstrated?

### Takeaway
The strongest directly relevant Twitch work supports studying coordinated groups and distributions of activity. It does not validate our current weights, estimate today's prevalence, or establish that a suspicious stream's owner commissioned the activity.

### Cited Findings
- **SHERLOCK, ASONAM 2019: Twitch chat, not silent viewers.** Researchers collected 439,000 messages from 690 streams in August–October 2018; manually annotated 183 streams, finding 24 chatbotted and 159 apparently genuine. Stream classification was evaluated with five-fold cross-validation. The end-to-end bot-class result was **97.0% precision and 94.4% recall**; do not round this into “97% accurate at detecting viewbotting.” Features included message counts, inter-message delay, active time windows, delay entropy, and subscriber indicators. Stage II used seed clustering and label propagation. Synthetic mixtures tested additional attack configurations; they were not independent field confirmation. Labels were human judgments. The work predates current generative chat systems and current Twitch measurement. — [Author paper](https://nshah.net/publications/SHERLOCK.ASONAM.19.pdf), [institutional author copy](https://cdn.iiit.ac.in/cdn/precog.iiit.ac.in/pubs/Characterizing_and_Detecting_Livestreaming_Chatbots-ASONAM19.pdf), [Snap research page](https://research.snap.com/publications/characterizing-and-detecting-livestreaming-chatbots.html).
- SHERLOCK's repository offers two anonymized sample logs and code, asks readers to contact authors for the complete dataset, and contains historical Python 2-era installation instructions. Availability of source does not imply a complete, ready-to-run modern benchmark. — [Author repository](https://github.com/shreya-03/Sherlock).
- **FLOCK, WWW 2017: undisclosed platform, private viewing records.** Its May 2016 dataset covers eight hours, 92,044 broadcasts and 16,280,308 views, with individual view start/end times and client IPs. It models joint start-time/stay-time distributions and isolates coordinated groups. The reported >98% broadcast precision and >90% precision/recall on sizable synthetic attacks are scoped to that workload and experimental setup. The platform is undisclosed: presenting this as a Twitch public-API benchmark is incorrect. Individual view sessions, dwell times and IPs are unavailable to Headcount. — [Author paper](https://nshah.net/publications/FLOCK.WWW.17.pdf), [full paper on arXiv](https://arxiv.org/html/1610.01096).
- **Human activity can look repetitive.** A Twitch study using 2014 logs found that overload coincided with shorter, more repetitive messages and lower per-user output. It manually inspected a stratified sample of 256 users, distinguishing bots, humans, copy-pasters, non-English and ambiguous cases. Average inter-message time alone did not discriminate well in that sample; text compressibility helped. The authors explicitly lacked a holdout set for evaluating their bot-filter precision. This is evidence for confounding and cautious features, not a transferable bot threshold. A public replication subset has five-minute message/user counts, without text or usernames. — [Nematzadeh et al., 2019](https://pmc.ncbi.nlm.nih.gov/articles/PMC6837236/), [aggregate dataset](https://doi.org/10.5281/zenodo.1182793).
- **Hate-raid research shows how to preserve attack context.** Han et al. collected 244,738,672 messages from 9,664 selected channels during September 2–16, 2021. A community list supplied 516 matching seed accounts; 95%-similar message matching expanded this to 1,583 accounts. Raid windows joined bot messages within two minutes. The sample selected channels with at least 100 average viewers and repeated broadcasts; it is not representative platform prevalence. Seed-list membership and text expansion are operational labels, not independent truth. — [Author paper, Methods](https://kumarde.com/papers/hateraids.pdf).
- **Generalization failures are empirically documented on Twitter.** LOBO trained a classifier with >97% ordinary accuracy and showed failures on unseen bot classes. Hays et al. found that shallow rules nearly matched sophisticated methods on many Twitter benchmarks, while cross-dataset generalization was weak; collection and labeling artifacts explained much of the apparent success. These are warnings about validation design, not Twitch performance estimates. — [LOBO](https://arxiv.org/abs/1809.09684), [Hays et al., WWW 2023](https://arxiv.org/html/2301.07015).
- A separate Twitter study found that strong cross-validation did not ensure performance on unseen datasets and explicitly selected using held-out domains. BotPercent likewise studies calibrated *community-level* Twitter estimates. Its task differs from estimating a stream's invisible viewer population. — [Yang et al., AAAI 2020](https://ojs.aaai.org/index.php/AAAI/article/download/5460/5316), [BotPercent, EMNLP 2023](https://aclanthology.org/2023.findings-emnlp.954/).

### Inferences
- Borrow the coordination hypothesis and study design; do not borrow published accuracy, account-age cutoffs, subscriber exemptions, or bot-prevalence estimates.
- A view-count trajectory can be compatible with many mixtures of humans and bots. Public aggregate data alone does not identify that mixture. Keep “suspicious pattern,” “automated chat,” “suspected viewer inflation” and “owner involvement” separate.
- Label harassment or follow attacks against a channel as events affecting a victim. Their presence is not evidence that its owner purchased engagement.

### Gaps
- No independently labeled, representative, current Twitch benchmark of silent viewbotting using Headcount's exact public-data inputs was established in this review.
- No verified accuracy for IP geography, account-name style, old-account innocence, or a fixed chat/view ratio was established. Missing user lookups cannot independently establish account suspension or its reason.
- Modern LLM-written chat detection was not validated by these older studies. These findings do not justify an “AI text detector” feature.

## 2. What can Headcount measure now, and what collection would help?

### Takeaway
The largest useful improvement is to examine cohorts and episodes rather than add isolated anomaly scores. Several cohort features are feasible from current rows; fine-grained timing and message similarity require new aggregates.

### Cited Findings
- Local inspection: Headcount tracks entrants into the global top 200, stores minute channel totals, and scores 12 heuristic detectors. Weights are hand-set; family-capped noisy-OR constrains amplification but is not a learned joint probability model. Confidence is evaluated detector weight multiplied by duration coverage. — [README](../../README.md), [Combine and weights](../../internal/detect/detectors.go).
- `stream_chatters` retains per-stream message totals and first/last observed minute. Despite the `ChatterMinute` name, minute rows are folded into per-stream rows, so individual activity-window occupancy and inter-message gaps cannot be recovered. Rows are eventually summarized/pruned. — [RecordChat](../../internal/store/chat.go), [retention](../../internal/store/retention.go).
- Existing minute totals include distinct texts, top text count, emote-only messages, new chatters, first messages, bans and timeouts. Only current-minute text is held in memory. Shared Chat messages sourced from another room are filtered. — [collector](../../internal/chat/collector.go), [stored fields](../../internal/store/chat.go).
- Synchrony currently tests onset matches with a binomial approximation, corrects across partners with Bonferroni, and discounts widespread synchronization. Its null treats match opportunities approximately uniformly; the detector's comment about a rotating bot service is a hypothesis, not independent evidence of service operation. — [Synchrony](../../internal/detect/sync.go).

### Inferences
**Priority order, proposed rather than validated:**

1. **Create an adjudicated evaluation set and replay harness before changing weights.** Preserve input snapshots, evidence dates, detector outputs and version. This is more valuable than increasing the count of correlated heuristics.
2. **Add explanations and abstention for unsupported conditions.** Distinguish data coverage from label certainty. Show viewer inflation evidence separately from chat/follow attacks and authorized utility bots. Return insufficient evidence for cold-tier gaps, collection failures and novel contexts.
3. **Test cohort structure using existing rows.** Measure concentration of account creation dates within narrow cohorts, first/last chat arrival clustering, message-count distributions, and shared-account communities. Compare to matched channels and to established regular viewers. Account age alone stays weak: old or compromised accounts can participate; human promotion can recruit many new accounts. Chat first/last times measure posting, not viewer arrival/departure.
4. **Validate synchrony and spike-response nulls.** Replay real nonattack windows and timestamp-shifted partners while preserving each series' serial structure, observation gaps and API update cadence. Keep wall-clock/event-matched controls because platform events and esports schedules synchronize humans. Estimate false alarms at the *whole pipeline* level, including repeated rescoring and best-partner selection. Bonferroni cannot repair a wrong underlying null.
5. **Collect compact behavioral summaries prospectively.** Keep privacy-preserving per-user active-window counts/bitsets, inter-message-gap histograms and dispersion, burst statistics, command ratios, and authorized-bot metadata in short-lived aggregates. Calculate normalized exact/approximate repetition in memory, optionally retaining only derived counts/sketches. This supports later study without storing raw chat.

**Possible with current storage:**
- Per-stream account creation cohort concentration and lookup coverage; messages-per-account inequality and quantiles; first/last posting span distributions; age-conditioned cross-channel overlap beyond “appeared in at least two others.” Use a degree-corrected comparison: prolific humans and analytics bots naturally appear in many channels.
- Channel-level repetition persistence from minute summaries, first-message/new-chatter proportions, emote-only and moderation response. None should be a decisive independent detector. Many derived features share the same episode or audience.
- Within-channel comparisons against earlier comparable broadcasts, including stream elapsed time and category/language. Cross-channel peers alone may confuse a community's normal lurking culture with abuse.
- Empirical spike response: lagged changes in unique chatters, first chatters and follows, with matched nonspike intervals. Separate transient spikes, persistent steps and ordinary gradual growth. Follower sampling/cache behavior must be replayed too.

**Requires new collection:**
- User active-minute occupancy, timing entropy, simultaneous posting cohorts and fine-grained synchrony; the existing first/last/count triple does not encode those.
- In-memory normalized template/near-duplicate statistics across windows or channels; current top-text counts do not reveal whether the same template recurs later. Sketches/hashes can still reveal text matches and need a retention/security design.
- Subscriber/moderator/known-authorized-bot roles, and contextual markers for ads, front-page exposure, embeds, Drops or special promotions when legitimately observable. Record provenance and “unknown”; do not infer that an unobserved confound did not happen.
- A small, consented human-review corpus may need contextual VOD/chat evidence that aggregates cannot supply. Give it explicit purpose and retention instead of silently changing the project's no-raw-chat policy.

**Unavailable from this approach:**
- Viewer IP, country, ASN, residential/datacenter classification, individual playback dwell time, device fingerprints, referral source and authenticated viewing sessions. Do not infer these from chatter identity or rename posting spans as watch time.

### Gaps
- Feasibility of role/context collection and exact API permissions should be checked against current Twitch documentation by the main report. This research does not authorize extra collection.
- Whether account-cohort or graph features improve precision is unknown until evaluated on contemporary labeled streams and their benign controls.

## 3. How should we validate and calibrate the calculation?

### Takeaway
Keep the current quotient as an evidence ranking until its meaning is tested. A number from 0–100 does not become a bot probability or percent-botted estimate by changing its label.

### Cited Findings
- Precision–recall analysis exposes false positives under class imbalance more clearly than relying on ROC curves alone. — [Saito and Rehmsmeier, 2015](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0118432).
- Calibration means agreement between predicted probabilities and observed event frequencies. CORP reliability diagrams use isotonic regression and offer uncertainty quantification; arbitrary bins can make apparent calibration unstable. — [Dimitriadis, Gneiting and Jordan, PNAS 2021](https://www.pnas.org/doi/10.1073/pnas.2016191118).
- Post-processing calibration can help evaluated classifiers, but calibration is a distinct property from ordinary classification accuracy; Guo et al.'s temperature-scaling evidence concerns trained image/document models, not an automatic recipe for hand-weighted Twitch heuristics. — [Guo et al., ICML 2017](https://proceedings.mlr.press/v70/guo17a.html).

### Inferences
**Proposed labeling and sampling study:**
- Define the target first: one stream episode with evidence consistent with inauthentic viewer inflation, automated engagement chat, or a follow/harassment attack. Keep authorized automation, ordinary human activity and indeterminate as separate annotations. Do not train “owner bought bots” without independent ownership evidence.
- Maintain two samples: (a) a probability sample from the actual tracked-channel/stream population, including low scores, for deployable precision/calibration and (b) an enriched challenge set with suspected incidents, rare mechanisms and hard benign cases for discovery. Record inclusion probabilities; do not calculate prevalence or calibrated probability from an unweighted balanced case-control sample.
- Stratify across score bands, viewer sizes, languages, categories, stream durations and collection quality. Randomly sample within strata. Include previously unflagged streams so false negatives can be found. Sample naturally quiet streams, major raids, esports/co-streams, music/24-hour streams, chat restrictions, giveaways, Drops, embedded/front-page exposure where documented, collective copypasta, fresh-account community arrivals and authorized utility bots.
- Have at least two reviewers independently examine evidence without seeing the quotient or its detector decisions. Record source provenance, attack start/end, mechanism confidence, contextual explanations and disagreement. Adjudicate disagreements; preserve indeterminate labels. A moderation action or missing account lookup is evidence to review, not a universal bot label.
- Expand until uncertainty around the operational precision target is acceptably narrow in the main population and material strata. The needed number depends on prevalence, clustering, ambiguity and the chosen target; this review supplies no sample-size guarantee. Report reviewed counts, positive counts and confidence intervals explicitly.

**One complete holdout/calibration protocol:**
1. Freeze a collector/scorer version and collect a dated corpus. Split by channel first, grouping suspected connected campaigns/account networks where possible; reserve later dates as a prospective test. Keep all snapshots of a stream in one split. Minute-level random splitting leaks the same audience and episode into training and test.
2. Use development channels/dates to design features and compare current noisy-OR against simple alternatives, such as regularized logistic regression or a monotonic additive score. Estimate baselines and normalization using training/past data only. Persist what was knowable at scoring time; do not use later account disappearance or future peer windows.
3. Tune on separate validation groups; run detector/family ablations. Ask whether an added detector improves precision at a useful recall or review budget, particularly on benign controls. Include unseen-language/category/campaign stress tests; mark unsupported strata rather than borrowing certainty from a large overall mean.
4. Fit any probability mapping on a separate calibration set, using the deployment distribution or justified sampling weights. Compare a simple logistic mapping with isotonic calibration only when enough labels support it. Calibrate each explicitly defined endpoint; probability of *automated chat* is not percentage of *viewers* that are fake.
5. Choose alert thresholds and an abstention rule on validation/calibration data, accounting for the cost of falsely accusing a channel. Set a minimum evidential/coverage requirement. Preserve an “unknown” state for conflicting signals, unavailable context and out-of-distribution cases.
6. Lock everything and evaluate once on unseen channels in the later time block. Report precision, recall, PR curve/average precision, false alerts per channel-stream and per monitored hour/day, alert persistence, review workload, and subgroup results. For probability outputs include Brier/log loss and a reliability plot with uncertainty. Bootstrap/resample at channel or campaign level, not individual minutes, when estimating intervals.
7. After release, audit a fresh random sample and false-positive appeals regularly; track inputs, coverage, score distributions and calibration by date/category/language. Revalidate after Twitch counting/API changes or detector changes. A distribution shift is a reason to inspect and potentially abstain; it does not prove a new bot wave.

**Important distinctions for the UI and report:**
- Current confidence = data availability/duration; label it coverage or evidence completeness unless a separate predictive uncertainty model is validated.
- Quotient = heuristic evidence rank; no defensible current mapping to “80% chance of bots,” “80% botted viewers,” or “80% chance owner cheated.”
- Low quotient with poor observation is inconclusive. High quotient with coherent evidence merits review. A calibrated probability would still depend on the population, date range, label definition and observation regime used to validate it.
- Top-200 entry is a selection mechanism. Results can describe tracked entrants and their subsequent streams; they cannot estimate all-Twitch bot prevalence without a broader representative sampling frame. Current cold-tier collection is score-dependent missingness: those streams need separate evaluation or recollection before fair sensitivity comparisons.

### Gaps
- No adjudicated contemporary Headcount labels or real-world error rates were available in this assignment. Synthetic detector tests verify implementation against chosen scenarios, not predictive accuracy, calibration or innocence of unflagged channels.
- Human review may establish plausible automation but still leave silent-viewer truth and ownership unresolved. Report performance against the reviewed operational labels and their uncertainty, not absolute ground truth.
