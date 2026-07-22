package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The Desk is the only channel from agents to the human, and it is deliberately hard to
// use. Everything in this file exists to enforce one sentence:
//
//	An escalation is actionable if and only if the human can resolve it in ONE ACTION,
//	without reading the thread, without asking a clarifying question, and silence has a
//	defined consequence.
//
// Agents cannot message the human directly. They call Escalate, which validates against
// that rule and returns field-level rejections. A vague ask never reaches a person.

type Ask struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Project  string    `json:"project"`
	From     string    `json:"from"`
	Verdict  string    `json:"verdict"`
	Action   string    `json:"action"`
	Options  []Option  `json:"options"`
	Default  Fallback  `json:"default_if_silent"`
	Tried    []string  `json:"tried"`
	Evidence []string  `json:"evidence"`

	Blocking    bool   `json:"blocking"`
	CostOfDelay string `json:"cost_of_delay,omitempty"`
	BlastRadius string `json:"blast_radius,omitempty"`

	Deadline time.Time `json:"deadline"`
	State    string    `json:"state"` // open | answered | defaulted | closed
	Answer   string    `json:"answer,omitempty"`
	AnswerBy string    `json:"answer_by,omitempty"` // owner | default
	ClosedAt time.Time `json:"closed_at,omitempty"`
}

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

type Fallback struct {
	Choice string `json:"choice"`
	After  string `json:"after"` // Go duration, e.g. "2h"
}

// Reject is a field-level validation failure. The hint tells the agent how to rewrite,
// so the refusal teaches rather than merely blocks.
type Reject struct {
	Field string `json:"field"`
	Rule  string `json:"rule"`
	Hint  string `json:"hint"`
}

func (r Reject) String() string { return fmt.Sprintf("%s: %s\n    → %s", r.Field, r.Rule, r.Hint) }

// Caps. An agent that is drowning must consolidate, not page a human five times.
const (
	MaxOpenPerAgent   = 3
	BlockingCooldown  = 30 * time.Minute
	MaxActionLen      = 200
	DefaultDeadlineHr = 2
)

// A question dressed as an instruction is still a question. These are the openings that
// mean "I have not done the thinking yet."
var nonActionable = regexp.MustCompile(`(?i)^(what|which|how|why|should|shall|would|could|can|do you|did you|any thoughts|thoughts|wdyt|let me know|please advise|advise|help|need input|input needed|your call|up to you|not sure)\b`)

// The action must start with a verb that names a decision the human can take.
var imperativeVerbs = map[string]bool{
	"approve": true, "reject": true, "choose": true, "pick": true, "decide": true,
	"confirm": true, "deny": true, "authorize": true, "authorise": true, "grant": true,
	"revoke": true, "provide": true, "supply": true, "set": true, "cap": true,
	"raise": true, "lower": true, "increase": true, "decrease": true, "merge": true,
	"revert": true, "delete": true, "remove": true, "rename": true, "buy": true,
	"pay": true, "enable": true, "disable": true, "allow": true, "block": true,
	"ship": true, "hold": true, "cancel": true, "schedule": true, "assign": true,
	"sign": true, "rotate": true, "replace": true, "restore": true, "publish": true,
	"unpublish": true, "accept": true, "decline": true, "select": true, "keep": true,
	"drop": true, "split": true, "rollback": true, "roll": true, "prioritize": true,
	"prioritise": true, "defer": true, "escalate": true, "fund": true, "extend": true,
}

// Validate applies the escalation contract. It returns every failure at once, so an agent
// fixes the ask in one rewrite instead of discovering problems one at a time.
func (a *Ask) Validate() []Reject {
	var out []Reject

	if strings.TrimSpace(a.Verdict) == "" {
		out = append(out, Reject{"verdict", "required",
			"One sentence: what is blocked, and why agents cannot resolve it themselves."})
	}

	action := strings.TrimSpace(a.Action)
	switch {
	case action == "":
		out = append(out, Reject{"action", "required",
			`An imperative instruction, e.g. "Approve or reject: raise the tier-3 reward from 250 to 400."`})
	case strings.HasSuffix(action, "?"):
		out = append(out, Reject{"action", "must not be a question",
			`Rewrite as an instruction with options. Not "what do you think about the cap?" but "Approve or reject: raise the cap to 400."`})
	case nonActionable.MatchString(action):
		out = append(out, Reject{"action", "opens like a question, not a decision",
			`Drop the "what/should/thoughts/let me know" opener. State the decision: "Choose a or b: ..."`})
	case len(action) > MaxActionLen:
		out = append(out, Reject{"action", fmt.Sprintf("longer than %d chars", MaxActionLen),
			"The action is the one line the human reads. Move detail into options and evidence."})
	default:
		first := strings.ToLower(strings.Trim(strings.Fields(action)[0], ".,:;!\"'"))
		if !imperativeVerbs[first] {
			out = append(out, Reject{"action", fmt.Sprintf("does not start with a decision verb (got %q)", first),
				"Start with approve, reject, choose, confirm, set, provide, authorize, merge, revert…"})
		}
	}

	if len(a.Options) < 2 {
		out = append(out, Reject{"options", "at least 2 required",
			"If there is only one option, it is not a decision — do it, or explain what blocks it."})
	}
	seen := map[string]bool{}
	for i, o := range a.Options {
		if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
			out = append(out, Reject{fmt.Sprintf("options[%d]", i), "id and label required",
				`Use --option "a:Cap at 250:quest spec needs a rewrite, 1 day".`})
			continue
		}
		if seen[o.ID] {
			out = append(out, Reject{fmt.Sprintf("options[%d]", i), "duplicate id", "Option ids must be unique."})
		}
		seen[o.ID] = true
		if strings.TrimSpace(o.Consequence) == "" {
			out = append(out, Reject{fmt.Sprintf("options[%d]", i), "consequence required",
				"Say what happens if this option is chosen. That is what makes the choice one action."})
		}
	}

	if len(a.Tried) == 0 {
		out = append(out, Reject{"tried", "required",
			"An escalation with nothing tried is a research task, not an ask. Ask the peer first."})
	}
	if len(a.Evidence) == 0 {
		out = append(out, Reject{"evidence", "required",
			"Cite a file:line, a PR, a commit, or test output the human can look at."})
	}

	// The load-bearing field: it turns every escalation from a blocker into a countdown.
	if strings.TrimSpace(a.Default.Choice) == "" || strings.TrimSpace(a.Default.After) == "" {
		out = append(out, Reject{"default_if_silent", "required",
			`What happens if the human never answers? e.g. --default "a:2h". Silence must never stall the work.`})
	} else {
		if _, err := time.ParseDuration(a.Default.After); err != nil {
			out = append(out, Reject{"default_if_silent.after", "not a duration",
				`Use a Go duration: "30m", "2h", "24h".`})
		}
		if len(a.Options) > 0 && !seen[a.Default.Choice] {
			out = append(out, Reject{"default_if_silent.choice", "does not match an option id",
				"The fallback must be one of the options you offered."})
		}
	}

	if a.Blocking {
		if strings.TrimSpace(a.CostOfDelay) == "" {
			out = append(out, Reject{"cost_of_delay", "required when blocking",
				"What stops moving while this waits? If nothing, it is not blocking."})
		}
		if strings.TrimSpace(a.BlastRadius) == "" {
			out = append(out, Reject{"blast_radius", "required when blocking",
				"What does the decision touch, and is it reversible?"})
		}
	}
	return out
}

// ---- storage ----

func deskDir(project, state string) string {
	return filepath.Join(ProjectDir(project), "desk", state)
}

func (a Ask) path() string {
	state := "open"
	if a.State != "open" && a.State != "" {
		state = "closed"
	}
	return filepath.Join(deskDir(a.Project, state), a.ID+".json")
}

// Escalate validates and files an ask. Nothing is written when validation fails.
func Escalate(from Identity, a *Ask) ([]Reject, error) {
	a.Project = from.Project
	a.From = from.Role
	if rejects := a.Validate(); len(rejects) > 0 {
		return rejects, nil
	}

	open, err := OpenAsks(a.Project)
	if err != nil {
		return nil, err
	}
	mine := 0
	for _, o := range open {
		if o.From == a.From {
			mine++
		}
	}
	if mine >= MaxOpenPerAgent {
		return []Reject{{"_cap", fmt.Sprintf("you already have %d open desk items", mine),
			"Consolidate them into one decision, or close one first. Paging a human five times is not urgency, it is noise."}}, nil
	}
	if a.Blocking {
		for _, o := range open {
			if o.Blocking && time.Since(o.TS) < BlockingCooldown {
				return []Reject{{"blocking", "another blocking item was raised recently",
					fmt.Sprintf("One blocking ask per project per %s. Fold this into %s, or file it non-blocking.",
						BlockingCooldown, o.ID)}}, nil
			}
		}
	}

	a.ID = "ask_" + NewID()[:16]
	a.TS = time.Now().UTC()
	a.State = "open"
	d, _ := time.ParseDuration(a.Default.After)
	a.Deadline = a.TS.Add(d)

	if err := writeJSONAtomic(a.path(), a); err != nil {
		return nil, err
	}
	notify(*a, "opened")
	return nil, nil
}

func OpenAsks(project string) ([]Ask, error) {
	entries, err := os.ReadDir(deskDir(project, "open"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Ask
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(deskDir(project, "open"), e.Name()))
		if err != nil {
			continue
		}
		var a Ask
		if json.Unmarshal(b, &a) == nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out, nil
}

func LoadAsk(project, id string) (Ask, error) {
	var a Ask
	for _, state := range []string{"open", "closed"} {
		b, err := os.ReadFile(filepath.Join(deskDir(project, state), id+".json"))
		if err == nil {
			err = json.Unmarshal(b, &a)
			return a, err
		}
	}
	return a, fmt.Errorf("no desk item %s", id)
}

// Resolve closes an ask and tells the asker. by is "owner" or "default".
func Resolve(project, id, choice, by string) (Ask, error) {
	a, err := LoadAsk(project, id)
	if err != nil {
		return a, err
	}
	if a.State != "open" {
		return a, fmt.Errorf("%s is already %s", id, a.State)
	}
	valid := false
	for _, o := range a.Options {
		if o.ID == choice {
			valid = true
			break
		}
	}
	if !valid && by == "owner" {
		var ids []string
		for _, o := range a.Options {
			ids = append(ids, o.ID)
		}
		return a, fmt.Errorf("%q is not one of the offered options (%s)", choice, strings.Join(ids, ", "))
	}

	openPath := a.path()
	a.Answer = choice
	a.AnswerBy = by
	a.ClosedAt = time.Now().UTC()
	if by == "default" {
		a.State = "defaulted"
	} else {
		a.State = "answered"
	}
	if err := writeJSONAtomic(a.path(), a); err != nil {
		return a, err
	}
	_ = os.Remove(openPath)

	// Tell the asker on the normal channel, so the answer wakes them like any peer mail.
	if asker, err := LoadIdentity(project, a.From); err == nil {
		label := choice
		for _, o := range a.Options {
			if o.ID == choice {
				label = o.Label
				break
			}
		}
		verb := "the owner answered"
		if by == "default" {
			verb = "nobody answered in time, so your stated default applied"
		}
		sys := Identity{Project: project, Role: "desk", Host: Host()}
		m := &Message{
			To: []string{asker.Role}, Type: "decision", Priority: "now",
			Subject: "Desk: " + trunc(a.Action, 90),
			Body: fmt.Sprintf("%s: %s (%s).\n\nOriginal ask: %s\nResolved: %s",
				verb, label, choice, a.Action, a.ClosedAt.Format(time.RFC3339)),
		}
		_, _ = Send(sys, m)
	}
	if by == "default" {
		notify(a, "defaulted")
	}
	return a, nil
}

// SweepDeadlines applies the stated default to every ask whose countdown has run out.
// The human being asleep, driving, or simply uninterested must never stall a pipeline.
func SweepDeadlines(project string) []Ask {
	open, err := OpenAsks(project)
	if err != nil {
		return nil
	}
	var fired []Ask
	for _, a := range open {
		if time.Now().After(a.Deadline) {
			if resolved, err := Resolve(project, a.ID, a.Default.Choice, "default"); err == nil {
				fired = append(fired, resolved)
			}
		}
	}
	return fired
}

// Defer pushes a deadline out without answering.
func Defer(project, id string, d time.Duration) (Ask, error) {
	a, err := LoadAsk(project, id)
	if err != nil {
		return a, err
	}
	if a.State != "open" {
		return a, fmt.Errorf("%s is already %s", id, a.State)
	}
	a.Deadline = time.Now().UTC().Add(d)
	return a, writeJSONAtomic(a.path(), a)
}

// ---- outbound notification ----

type deskConfig struct {
	NotifyCmd  string   `json:"notify_cmd"`
	NotifyCmds []string `json:"notify_cmds"`
	// Which events buzz. Default: blocking asks and missed deadlines only.
	// Add "opened" to be told about every ask, blocking or not.
	NotifyOn []string `json:"notify_on"`
}

func (c deskConfig) cmds() []string {
	out := c.NotifyCmds
	if strings.TrimSpace(c.NotifyCmd) != "" {
		out = append(out, c.NotifyCmd)
	}
	return out
}

func (c deskConfig) wants(event string, blocking bool) bool {
	on := c.NotifyOn
	if len(on) == 0 {
		on = []string{"blocking", "defaulted"}
	}
	for _, e := range on {
		switch strings.TrimSpace(strings.ToLower(e)) {
		case "all", "opened":
			return true
		case "blocking":
			if event == "opened" && blocking {
				return true
			}
		case "defaulted":
			if event == "defaulted" {
				return true
			}
		}
	}
	return false
}

// notify fires the user's own notification command(s). For a human who is not at the
// terminal, "the ask reached me in four seconds" is worth more than any in-terminal
// delivery mechanism.
func notify(a Ask, event string) {
	b, err := os.ReadFile(filepath.Join(Root(), "config.json"))
	if err != nil {
		return
	}
	var cfg deskConfig
	if json.Unmarshal(b, &cfg) != nil {
		return
	}
	if !cfg.wants(event, a.Blocking) || len(cfg.cmds()) == 0 {
		return
	}

	title := fmt.Sprintf("Mailroom %s: %s", event, a.Project)
	body := a.Action
	if event == "defaulted" {
		body = fmt.Sprintf("Nobody answered in %s, applied default %q. %s",
			a.Default.After, a.Default.Choice, a.Action)
	}
	for _, raw := range cfg.cmds() {
		cmd := strings.ReplaceAll(raw, "{title}", shellSafe(title))
		cmd = strings.ReplaceAll(cmd, "{body}", shellSafe(body))
		cmd = strings.ReplaceAll(cmd, "{id}", shellSafe(a.ID))
		cmd = strings.ReplaceAll(cmd, "{project}", shellSafe(a.Project))
		cmd = strings.ReplaceAll(cmd, "{from}", shellSafe(a.From))

		c := exec.Command(shellName(), shellFlag(), cmd)
		c.Stdout, c.Stderr = nil, nil
		if c.Start() == nil {
			go func(p *exec.Cmd) { _ = p.Wait() }(c)
		}
	}
}

// shellSafe strips what a shell would treat as structure. The ask text originates from an
// agent, so it is untrusted input to a command line.
func shellSafe(s string) string {
	s = Sanitize(s, 300)
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '`', '$', '\\', '\n', '\r', ';', '|', '&', '<', '>', '(', ')', '\'':
			return ' '
		}
		return r
	}, s)
}

func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
