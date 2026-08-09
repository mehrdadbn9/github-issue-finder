package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/go-github/v58/github"
)

// ContributionConvention describes how a given repo expects contributors to
// claim an issue. Repos differ: some allow self-assign, some require a
// maintainer/bot to assign (you comment "please assign me"), and the most
// common case is "open a PR that fixes it" (no assignment needed at all).
type ContributionConvention string

const (
	ConvSelfAssign ContributionConvention = "self_assign" // click Assign -> AddAssignees (needs write access)
	ConvProw       ContributionConvention = "prow"        // comment "/assign" - the bot assigns anyone
	ConvAskAssign  ContributionConvention = "ask_assign"  // comment asking maintainer to assign
	ConvOpenPR     ContributionConvention = "open_pr"     // most important: open a PR that solves it
	ConvAskThenPR  ContributionConvention = "ask_then_pr" // discuss first, PR only after a maintainer agrees
	ConvUnknown    ContributionConvention = "unknown"     // default -> ask_assign (safe)
)

// Default conventions for the standing scan targets. Anything not listed
// falls back to ConvUnknown (handled as ask_assign = safe).
// Org-wide entries use the "owner/*" form and apply to every repo in that org
// unless a more specific "owner/repo" entry overrides them.
//
// Conventions here are based on what these projects actually do in practice,
// not on guesses:
//   - Prow orgs (kubernetes, kubernetes-sigs, etcd-io, metal3-io) run tide/
//     prow-bot. Anyone can self-assign by commenting "/assign"; the GitHub
//     assignee API is closed to non-members. A member must still reply
//     "/ok-to-test" before CI runs on a first PR, and merge needs lgtm +
//     approve from OWNERS. Confirmed first hand: metal3-io/baremetal-operator
//     #3487 and etcd-io/etcd-operator #452 both landed on needs-ok-to-test.
//   - coredns and the VictoriaMetrics repos take a direct PR with no claim
//     step (coredns/coredns #8316 went straight to PR and was approved).
//   - argo-cd's AGENTS.md requires an APPROVED issue before a PR, so it is
//     discuss-first rather than ask-to-assign.
var defaultConventions = map[string]ContributionConvention{
	// Prow-governed orgs: "/assign" comment works for outside contributors.
	"kubernetes/*":      ConvProw,
	"kubernetes-sigs/*": ConvProw,
	"etcd-io/*":         ConvProw,
	"metal3-io/*":       ConvProw,

	// Direct PR, no claim needed.
	"VictoriaMetrics/VictoriaMetrics": ConvOpenPR,
	"VictoriaMetrics/operator":        ConvOpenPR,
	"coredns/coredns":                 ConvOpenPR,

	// Maintainer has to hand the issue out.
	"slok/sloth":                     ConvAskAssign,
	"strimzi/strimzi-kafka-operator": ConvAskAssign,
	"rabbitmq/cluster-operator":      ConvAskAssign,
	"prometheus/*":                   ConvAskAssign,
	"thanos-io/thanos":               ConvAskAssign,
	"kedacore/keda":                  ConvAskAssign,
	"cert-manager/*":                 ConvAskAssign,

	// Discuss first, PR only once a maintainer agrees.
	"argoproj/argo-cd":       ConvAskThenPR,
	"gravitational/teleport": ConvAskThenPR, // design-led, RFC culture
	"apache/skywalking":      ConvAskThenPR, // Apache projects discuss on the list first
	"tailscale/tailscale":    ConvAskThenPR, // CLA + they prefer agreeing the approach

	// Small/mid Go projects that just take a pull request.
	"containrrr/watchtower": ConvOpenPR, // marks issues "Status: Available"
	"valyala/fasthttp":      ConvOpenPR,
	"urfave/cli":            ConvOpenPR,
	"derailed/k9s":          ConvOpenPR,
	"ahmetb/kubectx":        ConvOpenPR,
	"grafana/loki":          ConvOpenPR,
	"jaegertracing/jaeger":  ConvOpenPR,
	"bazelbuild/bazel":      ConvOpenPR,
}

// LoadConventions reads an optional CONTRIB_POLICY env (JSON map
// "owner/repo": "convention") merged over the built-in defaults. This lets
// you tune per-repo behavior without a code change.
func LoadConventions() map[string]ContributionConvention {
	m := map[string]ContributionConvention{}
	for k, v := range defaultConventions {
		m[k] = v
	}
	if raw := strings.TrimSpace(os.Getenv("CONTRIB_POLICY")); raw != "" {
		var extra map[string]ContributionConvention
		if err := json.Unmarshal([]byte(raw), &extra); err == nil {
			for k, v := range extra {
				m[k] = v
			}
		} else {
			GetLogger().Warn("[Policy] invalid CONTRIB_POLICY JSON: %v", err)
		}
	}
	return m
}

// conventionFor resolves the most specific rule: an exact "owner/repo" entry
// wins over an "owner/*" org rule, which wins over the safe default.
func conventionFor(m map[string]ContributionConvention, owner, repo string) ContributionConvention {
	if c, ok := m[owner+"/"+repo]; ok {
		return c
	}
	if c, ok := m[owner+"/*"]; ok {
		return c
	}
	return ConvUnknown
}

// recoLevel maps a 0..1.5 FitScore to a human recommendation level.
func recoLevel(score float64) string {
	switch {
	case score >= 1.0:
		return "Strong claim"
	case score >= 0.85:
		return "High fit"
	case score >= 0.7:
		return "Worth it"
	case score >= 0.5:
		return "Review"
	default:
		return "Low priority"
	}
}

// recoLabel is the headline, and it has to agree with the next step printed
// underneath it. Score alone does not decide whether an issue is claimable:
// kubernetes#141216 scored 1.04 and went out reading "Strong claim" directly
// above "Claiming now is noise; watch it instead". The step is the stronger
// signal, so when it says do not claim yet, the headline says so too.
func recoLabel(score float64, step NextStep) string {
	switch step {
	case StepTriage:
		return "Watch (untriaged)"
	case StepRepro:
		return "Reproduce first"
	case StepDiscuss:
		return "Discuss first"
	default:
		return recoLevel(score)
	}
}

// encodeCallback encodes action + target so the poller can act on a press.
// Format: "assign:owner/repo/num" | "ask:owner/repo/num" | "pr:owner/repo/num".
func encodeCallback(action, owner, repo string, number int) string {
	return fmt.Sprintf("%s:%s/%s/%d", action, owner, repo, number)
}

// sendIssueWithButtons sends one issue with an inline keyboard whose buttons
// reflect THIS repo's contribution convention + your trust level there.
func sendIssueWithButtons(f *IssueFinder, issue Issue) error {
	if f.bot == nil {
		return fmt.Errorf("telegram bot not initialized")
	}
	owner, repo := issue.Project.Org, issue.Project.Name
	conv := ConvUnknown
	if f.contribPolicies != nil {
		conv = conventionFor(f.contribPolicies, owner, repo)
	}

	labelsText := ""
	if len(issue.Labels) > 0 {
		labelsText = fmt.Sprintf("\nLabels: %s", strings.Join(issue.Labels, ", "))
	}

	// Trust-based guidance from the existing ContributionPolicy engine.
	trustNote := ""
	contributeFirst := false
	if f.contribPolicy != nil {
		rec := f.contribPolicy.GetRecommendedAction(owner, repo, &github.Issue{
			Number: github.Int(issue.Number),
			Title:  github.String(issue.Title),
		})
		trustNote = "\nTrust: " + rec
		contributeFirst = f.contribPolicy.GetTrustLevel(owner, repo) == TrustNone
	}

	action := decideStep(conv, issue, contributeFirst)

	convNote := "\nHow to claim: " + stepHelp(action, conv)

	msg := fmt.Sprintf(
		"%s · <b>%s</b> (score %.2f)\n%s\n%s/%s (%d★)%s\nRecommendation: %s%s%s\n",
		recoLabel(issue.Score, action),
		html.EscapeString(truncateString(issue.Title, 80)),
		issue.Score,
		issue.URL,
		owner, repo, issue.Project.Stars,
		html.EscapeString(labelsText),
		recoLabel(issue.Score, action),
		trustNote,
		convNote,
	)

	// One button, but the RIGHT one: chosen from the repo's convention AND the
	// state this particular issue is in.
	label, cbAction := stepButton(action)
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label,
				encodeCallback(cbAction, owner, repo, issue.Number)),
		),
	}

	// Send to ALL configured chat IDs (private chat + group)
	chatIDs := f.config.TelegramChatIDs
	if len(chatIDs) == 0 && f.config.TelegramChatID != 0 {
		chatIDs = []int64{f.config.TelegramChatID}
	}
	if len(chatIDs) == 0 {
		return fmt.Errorf("no telegram chat IDs configured")
	}

	var lastErr error
	for _, chatID := range chatIDs {
		tgMsg := tgbotapi.NewMessage(chatID, msg)
		tgMsg.ParseMode = "HTML"
		tgMsg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)

		if _, err := f.bot.Send(tgMsg); err != nil {
			GetLogger().Error("Telegram send error to chat %d: %v", chatID, err)
			lastErr = err
		}
	}

	return lastErr
}

// NextStep is the single concrete next step offered for one issue. It is
// derived from the repo convention AND the state of the issue, because those
// are different questions: "how does this project hand out work" versus "is
// this particular issue ready to be worked on at all".
type NextStep string

const (
	StepProwAssign NextStep = "prow"    // comment /assign, bot assigns
	StepSelfAssign NextStep = "assign"  // direct API assign (write access)
	StepAsk        NextStep = "ask"     // humanized request to a maintainer
	StepPR         NextStep = "pr"      // just open the PR
	StepRepro      NextStep = "repro"   // reproduce before claiming anything
	StepTriage     NextStep = "triage"  // not triaged yet, do not claim
	StepDiscuss    NextStep = "discuss" // design/RFC, needs agreement first
)

func lower(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, strings.ToLower(s))
	}
	return out
}

func hasAnyOf(labels []string, want ...string) bool {
	for _, l := range labels {
		for _, w := range want {
			if strings.Contains(l, w) {
				return true
			}
		}
	}
	return false
}

// decideStep picks the one action that makes sense. Order matters: issue
// state overrides repo convention, because claiming an untriaged or
// unreproduced issue is exactly the kind of noise maintainers complain about.
func decideStep(conv ContributionConvention, issue Issue, contributeFirst bool) NextStep {
	labels := lower(issue.Labels)
	title := strings.ToLower(issue.Title)

	// Explicitly claimable beats everything - the project is asking for help.
	claimable := hasAnyOf(labels, "good first issue", "help wanted", "status: available")

	if !claimable {
		// Not triaged: no maintainer has agreed this is real or wanted yet.
		if hasAnyOf(labels, "needs-triage", "needs-kind", "needs-priority",
			"triage/needs-information", "needs/triage", "status/triage") {
			return StepTriage
		}
		// Maintainer is waiting on a reproduction.
		if hasAnyOf(labels, "pending repro", "needs-repro", "needs more info",
			"cannot reproduce", "needs-information") {
			return StepRepro
		}
		// Design work: a PR without agreement gets closed.
		if hasAnyOf(labels, "design", "proposal", "rfc", "kind/design") ||
			strings.Contains(title, "(design)") || strings.Contains(title, "[design]") ||
			strings.Contains(title, "proposal:") || strings.Contains(title, "rfc:") {
			return StepDiscuss
		}
	}

	switch conv {
	case ConvProw:
		return StepProwAssign
	case ConvSelfAssign:
		return StepSelfAssign
	case ConvOpenPR:
		return StepPR
	case ConvAskThenPR:
		return StepAsk
	}
	// Unknown repo, but the project has explicitly advertised the issue as
	// claimable ("Status: Available", "help wanted"). Asking permission to do
	// something already offered is the pointless comment maintainers dislike.
	if claimable && conv == ConvUnknown {
		return StepPR
	}

	// ask_assign / unknown: at zero trust the policy says contribute before
	// commenting, so the PR is the better first move.
	if contributeFirst {
		return StepPR
	}
	return StepAsk
}

// stepButton returns the button label and the callback verb.
func stepButton(a NextStep) (string, string) {
	switch a {
	case StepProwAssign:
		return "🤖 /assign (Prow)", "prow"
	case StepSelfAssign:
		return "✅ Assign to me", "assign"
	case StepAsk:
		return "💬 Ask maintainer", "ask"
	case StepRepro:
		return "🔬 Reproduce first", "repro"
	case StepTriage:
		return "👀 Wait for triage", "triage"
	case StepDiscuss:
		return "🧩 Discuss the design", "discuss"
	default:
		return "🛠️ I'll open a PR", "pr"
	}
}

func stepHelp(a NextStep, conv ContributionConvention) string {
	switch a {
	case StepTriage:
		return "not triaged yet - no maintainer has confirmed it. Claiming now is noise; watch it instead"
	case StepRepro:
		return "a maintainer is waiting on a reproduction. Reproduce it first, post the evidence, then claim"
	case StepDiscuss:
		return "design/proposal work - agree the approach first, a surprise PR gets closed"
	default:
		return conventionHelp(conv)
	}
}

func conventionHelp(c ContributionConvention) string {
	switch c {
	case ConvSelfAssign:
		return "self-assign allowed"
	case ConvProw:
		return "comment /assign (Prow assigns you); a member still has to reply /ok-to-test before CI runs on your first PR, and merge needs lgtm + approve"
	case ConvAskAssign:
		return "ask a maintainer/bot to assign you"
	case ConvOpenPR:
		return "open a PR that solves it (no assign needed, the PR is the claim)"
	case ConvAskThenPR:
		return "discuss first - this project wants an approved issue before a PR"
	default:
		return "ask a maintainer to assign you"
	}
}

// SendIssueAlertsWithButtons sends each found issue with convention-aware
// buttons and returns the issues that were actually delivered.
//
// Per-issue failures used to be logged and swallowed, and the caller then
// reported "Successfully sent Telegram alert for N issues" for the whole batch
// regardless - one cycle logged success for 3 issues while 2 of them had been
// rejected. That mattered once the alert path started recording deliveries,
// because a silently dropped issue would still be marked notified and never
// sent again.
func (f *IssueFinder) SendIssueAlertsWithButtons(issues []Issue) ([]Issue, error) {
	if f.bot == nil {
		return nil, fmt.Errorf("telegram bot not initialized")
	}
	GetLogger().Info("Sending %d issue alerts with Telegram buttons to %d chat(s)", len(issues), len(f.config.TelegramChatIDs))

	delivered := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if err := sendIssueWithButtons(f, issue); err != nil {
			GetLogger().Warn("Telegram alert error for %s#%d: %v", issue.Project.Name, issue.Number, err)
			continue
		}
		delivered = append(delivered, issue)
		time.Sleep(1 * time.Second)
	}

	if len(delivered) < len(issues) {
		GetLogger().Warn("Delivered %d of %d alerts; the rest stay unrecorded so they are retried next cycle",
			len(delivered), len(issues))
	}
	return delivered, nil
}

// StartCallbackPoller handles inline-button presses via Telegram polling
// (no inbound port required).
func (f *IssueFinder) StartCallbackPoller() {
	if f.bot == nil {
		GetLogger().Warn("[Telegram] callback poller not started (no bot)")
		return
	}
	GetLogger().Info("[Telegram] starting callback poller for Assign/Ask/PR actions")
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	ch := f.bot.GetUpdatesChan(u)
	for upd := range ch {
		if upd.CallbackQuery == nil {
			continue
		}
		f.handleCallback(upd.CallbackQuery)
	}
}

func (f *IssueFinder) handleCallback(cq *tgbotapi.CallbackQuery) {
	GetLogger().Info("[Telegram] callback received: data=%q from=%s", cq.Data, func() string {
		if cq.From != nil {
			if cq.From.UserName != "" {
				return "@" + cq.From.UserName
			}
			return fmt.Sprintf("id=%d", cq.From.ID)
		}
		return "unknown"
	}())
	data := cq.Data
	parts := strings.SplitN(data, ":", 2)
	if len(parts) != 2 {
		f.answerCallback(cq, "Invalid action")
		return
	}
	action := parts[0]
	segs := strings.Split(parts[1], "/")
	if len(segs) != 3 {
		f.answerCallback(cq, "Invalid target")
		return
	}
	owner, repo := segs[0], segs[1]
	var number int
	if _, err := fmt.Sscanf(segs[2], "%d", &number); err != nil {
		f.answerCallback(cq, "Invalid issue number")
		return
	}
	ctx := context.Background()

	switch action {
	case "assign":
		user, _, err := f.client.Users.Get(ctx, "")
		if err != nil || user == nil {
			f.answerCallback(cq, "Could not resolve your GitHub login")
			return
		}
		if f.assignmentMgr != nil && f.assignmentMgr.spamManager != nil {
			if ok, reason := f.assignmentMgr.spamManager.CanRequestAssignment(owner + "/" + repo); !ok {
				f.answerCallback(cq, "Assignment limit: "+reason)
				return
			}
		}
		_, _, err = f.client.Issues.AddAssignees(ctx, owner, repo, number, []string{user.GetLogin()})
		if err != nil {
			log.Printf("[Telegram] assign failed %s/%s#%d: %v", owner, repo, number, err)
			f.answerCallback(cq, "Assign failed: "+firstLine(err.Error()))
			return
		}
		if f.assignmentMgr != nil && f.assignmentMgr.spamManager != nil {
			_ = f.assignmentMgr.spamManager.RecordAssignmentRequest(owner+"/"+repo, &AssignmentRequest{IssueNumber: number, Status: AssignmentAssigned})
		}
		log.Printf("[Telegram] assigned %s/%s#%d to %s", owner, repo, number, user.GetLogin())
		f.answerCallback(cq, "✅ Assigned to you ("+user.GetLogin()+")")

	case "prow":
		// Prow orgs close the assignee API to outside contributors, but the
		// bot honours a "/assign" comment from anyone. That single command is
		// the whole claim - no prose, so it costs the tracker nothing.
		if f.assignmentMgr != nil && f.assignmentMgr.spamManager != nil {
			if ok, reason := f.assignmentMgr.spamManager.CanRequestAssignment(owner + "/" + repo); !ok {
				f.answerCallback(cq, "Assignment limit: "+reason)
				return
			}
		}
		_, _, err := f.client.Issues.CreateComment(ctx, owner, repo, number,
			&github.IssueComment{Body: github.String("/assign")})
		if err != nil {
			log.Printf("[Telegram] /assign failed %s/%s#%d: %v", owner, repo, number, err)
			f.answerCallback(cq, "/assign failed: "+firstLine(err.Error()))
			return
		}
		if f.assignmentMgr != nil && f.assignmentMgr.spamManager != nil {
			_ = f.assignmentMgr.spamManager.RecordAssignmentRequest(owner+"/"+repo,
				&AssignmentRequest{IssueNumber: number, Status: AssignmentAsked})
		}
		log.Printf("[Telegram] posted /assign on %s/%s#%d", owner, repo, number)
		f.answerCallback(cq, "🤖 Posted /assign - Prow will assign you")

	case "ask":
		candidate := &AssignmentCandidate{
			Issue: &github.Issue{
				Title:   github.String(owner + "/" + repo + "#" + segs[2]),
				Number:  github.Int(number),
				HTMLURL: github.String(fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)),
				State:   github.String("open"),
			},
			ProjectOrg:  owner,
			ProjectName: repo,
		}
		if f.assignmentMgr != nil {
			req, err := f.assignmentMgr.AskForAssignment(ctx, candidate)
			if err != nil {
				log.Printf("[Telegram] ask failed %s/%s#%d: %v", owner, repo, number, err)
				f.answerCallback(cq, "Ask failed: "+firstLine(err.Error()))
				return
			}
			_ = req
			f.answerCallback(cq, "💬 Politely asked the maintainer to assign you")
		} else {
			f.answerCallback(cq, "Assignment manager disabled")
		}

	case "pr":
		// Most important path: tell the user to open a PR that solves it.
		// No GitHub write is performed; we just confirm the intent and log it.
		log.Printf("[Telegram] PR intent for %s/%s#%d (user will open a PR)", owner, repo, number)
		f.answerCallback(cq, "🛠️ Great — open a PR that solves this; link it back to the issue")

	// The next three write nothing to GitHub on purpose: the whole point is
	// that this issue is not ready to be claimed yet.
	case "repro":
		log.Printf("[Telegram] repro intent for %s/%s#%d", owner, repo, number)
		f.answerCallback(cq, "🔬 Reproduce it locally first, then post the evidence")

	case "triage":
		log.Printf("[Telegram] triage watch for %s/%s#%d", owner, repo, number)
		f.answerCallback(cq, "👀 Untriaged — noted. Revisit once a maintainer labels it")

	case "discuss":
		log.Printf("[Telegram] discuss intent for %s/%s#%d", owner, repo, number)
		f.answerCallback(cq, "🧩 Agree the design in the issue before writing code")

	default:
		f.answerCallback(cq, "Unknown action")
	}
}

func (f *IssueFinder) answerCallback(cq *tgbotapi.CallbackQuery, text string) {
	ans := tgbotapi.NewCallback(cq.ID, text)
	if _, err := f.bot.Request(ans); err != nil {
		GetLogger().Error("[Telegram] failed to answer callback: %v", err)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
