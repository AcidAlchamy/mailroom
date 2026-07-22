// Command mailroom is a project-scoped mailbox for independently-started Claude Code
// sessions, so they coordinate with each other instead of routing everything through
// the human.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AcidAlchamy/mailroom/internal/mail"
)

const usage = `mailroom — a session mailbox for Claude Code

  mailroom enroll --role <name> [--note <what you are doing>]
  mailroom send --to <role> --type <type> --subject <s> [--body <b>] [--ref <r>] [--priority <p>]
  mailroom inbox [--json]          digest of undelivered mail (does not consume)
  mailroom read [<id>...]          full bodies; marks delivered
  mailroom status <state> [--note] update presence without sending mail
  mailroom roster [--json]         who is live in this project
  mailroom whoami
  mailroom doctor                  which delivery rungs are live on this machine
  mailroom hook <event>            internal: called by Claude Code hooks

Types:     note question request status claim release decision blocked handoff ack nack
Priority:  fyi (digest only) | normal (next turn) | now (may spend a wake)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "enroll":
		err = cmdEnroll(args)
	case "send":
		err = cmdSend(args)
	case "inbox":
		err = cmdInbox(args)
	case "read":
		err = cmdRead(args)
	case "status":
		err = cmdStatus(args)
	case "roster":
		err = cmdRoster(args)
	case "whoami":
		err = cmdWhoami(args)
	case "doctor":
		err = cmdDoctor(args)
	case "hook":
		os.Exit(cmdHook(args))
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mailroom: %v\n", err)
		os.Exit(1)
	}
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	role := fs.String("role", "", "stable role name, e.g. backend, renderer, reviewer")
	note := fs.String("note", "", "one line: what you are working on")
	display := fs.String("display", "", "optional human-friendly label")
	project := fs.String("project", "", "override the derived project key")
	_ = fs.Parse(args)

	if *role == "" && fs.NArg() > 0 {
		*role = fs.Arg(0)
	}
	if *role == "" {
		return fmt.Errorf("--role is required (e.g. --role backend)")
	}
	cwd, _ := os.Getwd()
	pid := *project
	if pid == "" {
		pid = mail.ProjectID(cwd)
	}

	id, prev, err := mail.Enroll(pid, *role, *display, *note)
	if err != nil {
		return err
	}
	fmt.Printf("enrolled as %s\n", id.Short())

	if prev.SessionID != "" && prev.SessionID != id.SessionID && prev.Live() {
		fmt.Printf("note: took over an active lease from session %s on %s\n",
			short(prev.SessionID), prev.Host)
	}
	peers, _ := mail.Roster(pid)
	var live []string
	for _, p := range peers {
		if p.Role != id.Role && p.Live() {
			live = append(live, p.Short())
		}
	}
	if len(live) > 0 {
		fmt.Printf("live peers: %s\n", strings.Join(live, ", "))
	} else {
		fmt.Println("no other peers live yet — enroll a second session to talk to")
		// Almost always a working-directory mismatch, not an empty mailbox.
		if n := mail.Neighbours(pid); len(n) > 0 {
			fmt.Println("\nheads up: live agents exist in other projects on this machine —")
			for proj, peers := range n {
				var names []string
				for _, p := range peers {
					names = append(names, p.Role)
				}
				fmt.Printf("  %-24s %s\n", proj, strings.Join(names, ", "))
			}
			fmt.Printf("\nThis session resolved to %q from %s.\n", pid, cwd)
			fmt.Println("If you meant to join one of those, the other session was started from a")
			fmt.Println("different directory. Re-run from the same repo, or: enroll --project <name>")
		}
	}
	if pending, _ := mail.Peek(pid, id.Role); len(pending) > 0 {
		fmt.Printf("%d message(s) waiting:\n%s", len(pending), mail.Digest(pending))
	}
	return nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	var to multiFlag
	var refs multiFlag
	fs.Var(&to, "to", "recipient role (repeatable)")
	fs.Var(&refs, "ref", "artifact: file:line, commit, PR, or URL (repeatable)")
	typ := fs.String("type", "note", "message type")
	subject := fs.String("subject", "", "one line, <=120 chars")
	body := fs.String("body", "", "<=4096 chars; put bulk in --ref")
	priority := fs.String("priority", "normal", "fyi | normal | now")
	thread := fs.String("thread", "", "thread id to continue")
	replyTo := fs.String("in-reply-to", "", "message id being answered")
	asJSON := fs.Bool("json", false, "machine-readable result")
	project := fs.String("project", "", "which project to send from (if enrolled in several)")
	_ = fs.Parse(args)

	me, err := mail.Whoami(*project)
	if err != nil {
		return err
	}
	m := &mail.Message{
		To: to, Type: *typ, Subject: *subject, Body: *body,
		Priority: *priority, Thread: *thread, InReplyTo: *replyTo,
	}
	for _, r := range refs {
		m.Refs.Files = append(m.Refs.Files, r)
	}
	if *replyTo != "" {
		if parent, err := mail.LoadMessage(me.Project, *replyTo); err == nil {
			m.Hops = parent.Hops + 1
			if m.Thread == "" {
				m.Thread = parent.Thread
			}
		}
	}

	res, err := mail.Send(me, m)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(res.Delivered) > 0 {
		fmt.Printf("sent %s to %s (hop %d)\n", short(res.ID), strings.Join(res.Delivered, ", "), res.Hops)
	}
	for _, n := range res.Nacked {
		fmt.Printf("not delivered to %s: %s\n", n.Addr, n.Reason)
	}
	if len(res.Delivered) == 0 && len(res.Nacked) > 0 {
		return fmt.Errorf("no recipients reachable")
	}
	return nil
}

func cmdInbox(args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "machine-readable")
	project := fs.String("project", "", "which project (if enrolled in several)")
	_ = fs.Parse(args)

	me, err := mail.Whoami(*project)
	if err != nil {
		return err
	}
	msgs, err := mail.Peek(me.Project, me.Role)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(msgs, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(msgs) == 0 {
		fmt.Printf("%s: inbox empty\n", me.Short())
		return nil
	}
	fmt.Printf("%s: %d new\n%s", me.Short(), len(msgs), mail.Digest(msgs))
	return nil
}

func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	project := fs.String("project", "", "which project (if enrolled in several)")
	_ = fs.Parse(args)
	args = fs.Args()
	me, err := mail.Whoami(*project)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		msgs, err := mail.Fetch(me.Project, me.Role)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			fmt.Println("nothing new")
			return nil
		}
		fmt.Print(mail.Render(msgs, me.Short()))
		return nil
	}
	var msgs []mail.Message
	for _, id := range args {
		m, err := mail.LoadMessage(me.Project, id)
		if err != nil {
			return fmt.Errorf("no message %s", id)
		}
		msgs = append(msgs, m)
	}
	fmt.Print(mail.Render(msgs, me.Short()))
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	note := fs.String("note", "", "one line on what you are doing")
	project := fs.String("project", "", "which project (if enrolled in several)")
	_ = fs.Parse(args)

	me, err := mail.Whoami(*project)
	if err != nil {
		return err
	}
	state := "active"
	if fs.NArg() > 0 {
		state = fs.Arg(0)
	}
	if err := mail.Touch(me.Project, me.Role, state, *note); err != nil {
		return err
	}
	fmt.Printf("%s: %s %s\n", me.Short(), state, *note)
	return nil
}

func cmdRoster(args []string) error {
	fs := flag.NewFlagSet("roster", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "machine-readable")
	projFlag := fs.String("project", "", "which project (if enrolled in several)")
	_ = fs.Parse(args)

	cwd, _ := os.Getwd()
	project := mail.ProjectID(cwd)
	if *projFlag != "" {
		project = *projFlag
	}
	if me, err := mail.Whoami(*projFlag); err == nil {
		project = me.Project
	}
	peers, err := mail.Roster(project)
	if err != nil {
		return err
	}
	if *asJSON {
		b, _ := json.MarshalIndent(peers, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(peers) == 0 {
		fmt.Printf("project %q: nobody enrolled yet\n", project)
		return nil
	}
	fmt.Printf("project %q:\n", project)
	for _, p := range peers {
		state := "offline"
		if p.Live() {
			state = "live"
		}
		pending, _ := mail.Peek(project, p.Role)
		line := fmt.Sprintf("  %-24s %-8s %s  last seen %s",
			p.Short(), state, p.Host, humanAge(p.LastSeen))
		if len(pending) > 0 {
			line += fmt.Sprintf("  (%d unread)", len(pending))
		}
		if p.Note != "" {
			line += "\n      " + p.Note
		}
		fmt.Println(line)
	}
	return nil
}

func cmdWhoami(args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	project := fs.String("project", "", "which project (if enrolled in several)")
	_ = fs.Parse(args)
	me, err := mail.Whoami(*project)
	if err != nil {
		return err
	}
	fmt.Printf("%s\naddr:    %s\nsession: %s\nhost:    %s\nroot:    %s\n",
		me.Short(), me.Addr(), short(me.SessionID), me.Host, mail.Root())
	return nil
}

func cmdDoctor(args []string) error {
	cwd, _ := os.Getwd()
	fmt.Printf("root:     %s\n", mail.Root())
	fmt.Printf("realm:    %s\n", mail.Realm())
	fmt.Printf("project:  %s\n", mail.ProjectID(cwd))
	fmt.Printf("session:  %s\n", orNone(mail.SessionID()))
	fmt.Printf("wakes/hr: %d\n", mail.WakesPerHour())

	if me, err := mail.Whoami(""); err == nil {
		fmt.Printf("enrolled: yes, as %s\n", me.Short())
		pending, _ := mail.Peek(me.Project, me.Role)
		fmt.Printf("unread:   %d\n", len(pending))
	} else {
		fmt.Printf("enrolled: no (%v)\n", err)
	}

	fmt.Println("\ndelivery rungs (verified on Claude Code v2.1.211):")
	fmt.Println("  SessionStart/UserPromptSubmit additionalContext  floor, always on")
	fmt.Println("  Stop turn-tail delivery                          always on")
	fmt.Println("  Stop parked waiter + asyncRewake (idle wake)     on, ~4-5s")
	return nil
}

func cmdHook(args []string) int {
	if len(args) == 0 {
		return 0
	}
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	park := fs.Int("park", 0, "seconds to long-poll for mail after the turn ends")
	_ = fs.Parse(args[1:])

	in := mail.ReadHookInput(os.Stdin)

	// Hooks must never break a session. Any panic exits 0 and stays silent.
	defer func() {
		if r := recover(); r != nil {
			os.Exit(0)
		}
	}()

	switch args[0] {
	case "session-start":
		return mail.HookSessionStart(in)
	case "prompt":
		return mail.HookPrompt(in)
	case "stop":
		return mail.HookStop(in, time.Duration(*park)*time.Second)
	case "depart":
		return mail.HookDepart(in)
	}
	return 0
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
