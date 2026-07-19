# Architecture — github-issue-finder

Target design for the issue-finder. This is both the tool that drives contribution
decisions **and** the Go-rigor artifact for the SRE plan (clean architecture +
tests + CI = interview material). Written to be understood before any refactor
ships — read it end to end, then migrate in the order at the bottom.

---

## 1. What it does (one line)

Given a set of standing target repos, fetch open issues, put each through **four
hard gates**, score the survivors, rank them, and deliver the top picks to a sink
(Telegram / email / file / stdout). **It never posts to GitHub.** Commenting is a
human action; the tool only surfaces and scores.

---

## 2. The pipeline (the domain)

```
 config ─► Source ─► Gate chain ─► Score ─► Rank ─► Sink
           (fetch)   (reject)      (0..1)   (sort)  (notify/store)
              │          │            │       │        │
         GitHub API   4 gates     FitScore  claim≥65  Telegram/email/file
         + timeline   + question  weights   engage    (auto_post=false)
         + ratelimit  veto                  45–64
```

Each stage is a small, single-purpose unit. The two stages with all the judgment
(**Gate**, **Score**) are pure functions over domain types — no network, no I/O —
so they are trivially unit-testable (see `qualified_question_test.go`).

### The four hard gates (a reject means *don't even score it*)
1. **Real** — actual bug/feature with substance, not noise. Excludes `invalid`,
   `duplicate`, `wontfix`, and now **question/support content** (`looksLikeQuestion`,
   content-based — a good label set no longer rescues a support thread).
2. **Accepted** — maintainer signal: `confirmed` / `triage/accepted` / `help wanted`
   / priority label, or a maintainer comment endorsing it.
3. **Available** — no assignee **AND** no open linked PR **AND** no claim comments.
   > ⚠️ Current weakness: availability only checks `issue.PullRequestLinks == nil`,
   > which detects whether the *issue is itself a PR* — not whether a PR is linked
   > to it. The real check is the **timeline API** cross-referenced events (an open
   > linked PR = taken). This is the single highest-value fix; it's what caught
   > etcd #20773 (4 open linked PRs) in manual scans.
4. **Fit** — matches skill/domain (Go operator, k8s autoscaling, SLO/alerting,
   observability) and is locally reproducible.

### Scoring (survivors only)
`FitScore = ProjectScore × IssueScore` after the gates. Thresholds: **≥65 claim,
45–64 engage, <45 drop.** Weights live in config, not code. Full model in
`../../github/sre-master-plan/07-scoring-model.md`.

---

## 3. Target package layout

Idiomatic Go: a thin `cmd/`, all logic under `internal/`, exportable reuse in `pkg/`.

```
github-issue-finder/
├── cmd/
│   └── finder/
│       └── main.go            # ~40 lines: load config, build engine, run. Nothing else.
├── internal/
│   ├── config/                # load + validate config.yaml + env; the auto_post=false invariant
│   ├── model/                 # Issue, Project, Score, Verdict, GateResult — no behavior, no imports
│   ├── source/                # GitHub adapter: search, IssueListByRepo, timeline (linked PRs), RateLimiter
│   ├── gate/                  # one file per gate, all implement Gate; a Chain runs them and records reasons
│   ├── score/                 # FitScore + sub-scorers; pure functions over model types
│   ├── rank/                  # sort + threshold buckets (claim/engage/drop)
│   ├── sink/                  # telegram.go, email.go, file.go, stdout.go — all implement Sink
│   └── engine/                # orchestrates the pipeline; the cron and CLI both call engine.Run
├── pkg/                        # (optional) bits slogen / the operator might reuse
└── ARCHITECTURE.md             # this file
```

Everything currently at the repo root (`main.go` 122KB, `smart_comment.go`,
`enhanced_scorer.go`, `qualified_issue_finder.go`, …) collapses into the boxes
above. The ~15 `*.py` scripts (`add_email.py`, `fix_main.py`, `assign_*`, …) are
one-off scaffolding — **delete them**; anything worth keeping becomes a Go
subcommand or a Makefile target.

---

## 4. Ports & adapters (why the interfaces)

The domain (`gate`, `score`, `rank`) must not import GitHub or Telegram. It talks
to **ports** — small interfaces — and the outside world plugs in **adapters**.

```go
// port: where issues come from
type Source interface {
    ListOpenIssues(ctx context.Context, repo Repo) ([]model.Issue, error)
    LinkedPRs(ctx context.Context, repo Repo, number int) ([]model.PR, error) // timeline API
}

// port: one qualification gate; the chain is just []Gate
type Gate interface {
    Name() string
    Check(model.Issue) (pass bool, reason string)
}

// port: where results go
type Sink interface {
    Deliver(ctx context.Context, picks []model.ScoredIssue) error
}
```

Payoff, concretely:
- **Testable** — fake `Source` returns canned issues; assert gate/score output with
  zero network (the whole point).
- **Swappable** — GitHub → GitLab is a new adapter, domain untouched. Telegram →
  Slack, same.
- **Composable gates** — add a gate (question veto, linked-PR, staleness) by
  appending to the slice; each records *why* it rejected, so scans are explainable.
- **One place for the safety invariant** — "never auto-post" lives in the `sink`
  boundary + `config`, not sprinkled across 122KB.

This hexagonal shape is the part worth being able to whiteboard in an interview.

---

## 5. Known issues to fix (priority order)

1. ~~**Availability gate uses the wrong signal**~~ — ✅ **fixed**:
   `linkedPRBlocksAvailability` now uses the timeline API as a **hard gate**, run
   only on issues that pass the cheap gates + score threshold. Blocks on an **open**
   linked PR (taken) *and* on a **recently closed, unmerged** linked PR (stalled /
   contested — a human should review before re-claiming; confirmed with one light
   `PullRequests.Get` since the timeline's typed Issue lacks merge status). Fails
   open on API error. (The old `scoreNoOpenPR` soft score is superseded; left as a
   harmless constant, to be removed in the `score/` extraction.)
   - **Known residual blind spot** (documented, needs human review, not auto-caught):
     a PR that links only *two hops* away — e.g. kedacore/keda **#7691**, whose only
     direct link #7713 is closed, while the canonical fix **#7700** references #7691
     only via branch name + a maintainer "duplicate of #7713" comment. Neither the
     timeline nor a PR-search-by-number surfaces #7700 (its title/body never mention
     7691). The recent-stalled-PR rule flags #7691 for review via #7713, which is the
     right outcome, but the general two-hop case remains a manual check.
2. **Question/support veto** — ✅ added (`looksLikeQuestion`, content-based, tested).
   A "help wanted" label no longer rescues a support thread.
3. **Monolith** — split `main.go` per §3.
4. **Python cruft** — delete the `*.py` scaffolding.
5. **No CI** — add `go test ./... && go vet ./...` on push (§7).

---

## 6. Invariants (never violate)

- **Never posts to GitHub.** `auto_comment: false` is enforced in config load; the
  scan/cron path has no GitHub write adapter wired at all.
- **Max one unanswered comment per org** — a policy the *human* follows; the tool
  surfaces, the human posts.
- Standing targets and scoring weights are **config, not code**.

---

## 7. Testing & CI

- Gate + score = pure → table-driven unit tests (pattern already in
  `qualified_question_test.go`). Aim: every gate has a truth table.
- Source/sink adapters → thin, tested against fakes or recorded fixtures.
- CI: `go vet ./... && go test ./... && go build ./...` on every push. Add a
  `Makefile` target `make check` that runs all three.

---

## 8. Migration order (each step ships green, low-risk)

1. Add CI (`make check`) against the current tree — establishes the safety net.
2. Extract `model/` (pure types) — no behavior change.
3. Extract `source/` behind the `Source` port + **fix the linked-PR check**.
4. Extract `gate/` as a `Chain` of `Gate` — move `looksLikeQuestion`, label gates,
   availability, fit in; one truth-table test each.
5. Extract `score/` + `rank/`.
6. Extract `sink/`; assert the no-post invariant with a test.
7. Shrink `main.go` → `cmd/finder/main.go`; delete the `*.py` scripts.
8. Tag `v2.1.0`.

---

## 9. How this feeds the bigger plan

- **Interview power** — a clean hexagonal Go service with a real domain (gated,
  scored pipeline), tests, and CI is a concrete artifact to walk through.
- **Reuse** — `pkg/` bits (rate limiter, GitHub timeline client) are portable into
  slogen or the operator work.
- It is the **warm-up** in `06-first-90-days.md` (M2). Doing the refactor *is* the
  Go-rigor milestone, not a detour from it.
