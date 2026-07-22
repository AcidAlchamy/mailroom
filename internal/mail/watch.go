package mail

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Watch is the always-on delivery channel, run as a Claude Code monitor process.
//
// Why it exists: the parked Stop waiter only covers the window right after a turn ends
// (its deadline is bounded by the hook timeout). A session idle for hours has no waiter
// armed and is unreachable until the human types. A monitor is a persistent process for
// the life of the session, and Phase 0 verified that a line on its stdout induces a turn
// in ~3s. So: monitor = always-on, parked waiter = fast path. Between them, "continuous"
// is true for a session idle for minutes or for hours.
//
// EVERY LINE WRITTEN TO stdout BECOMES A NOTIFICATION. Diagnostics go to stderr, which
// the harness captures to a file without waking anything.
func Watch(w io.Writer, diag io.Writer, interval time.Duration) error {
	// Monitor stderr is captured by the harness to a path we cannot predict, so mirror
	// diagnostics somewhere the user (and `mailroom doctor`) can always find them.
	if f, err := os.OpenFile(filepath.Join(Root(), "watch.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer f.Close()
		diag = io.MultiWriter(diag, f)
	}
	fmt.Fprintf(diag, "\n[%s] mailroom watch: starting, interval=%s, root=%s\n",
		time.Now().Format(time.RFC3339), interval, Root())
	fmt.Fprintf(diag, "  session id visible to this process: %q\n", SessionID())
	fmt.Fprintf(diag, "  CLAUDE* env vars present: %v\n", claudeEnv())

	// Session identity may not be resolvable at spawn time (the monitor can start before
	// the session enrolls), so we re-resolve every tick rather than failing fast.
	warned := false
	for {
		delivered := deliverOnce(w, diag)
		if !delivered && !warned {
			if SessionID() == "" {
				fmt.Fprintf(diag, "mailroom watch: no session id in environment; "+
					"monitor cannot bind to a session. env(CLAUDE*)=%v\n", claudeEnv())
				warned = true
			}
		}
		time.Sleep(interval)
	}
}

func claudeEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLAUDE") {
			if i := strings.Index(e, "="); i > 0 {
				out = append(out, e[:i])
			}
		}
	}
	return out
}

// deliverOnce drains every project this session is enrolled in. Delivery races the
// parked Stop waiter, and that is fine: Fetch moves new/ -> cur/ with an atomic rename,
// so exactly one of them can win any given message.
func deliverOnce(w io.Writer, diag io.Writer) bool {
	sid := SessionID()
	if sid == "" {
		return false
	}
	rec := loadSession(sid)
	if rec.Current.Project == "" {
		return false
	}

	any := false
	for _, e := range rec.Enrollments {
		id, err := LoadIdentity(e.Project, e.Role)
		if err != nil {
			continue
		}
		// Countdowns must run whether or not a human is looking. Otherwise
		// default_if_silent only fires when the user opens the Desk, which is exactly
		// the dependency it exists to remove. Resolve() mails the asker, so the
		// notification arrives through the normal channel below.
		for _, fired := range SweepDeadlines(e.Project) {
			fmt.Fprintf(diag, "mailroom watch: %s expired, applied default %q\n", fired.ID, fired.Answer)
		}
		// A monitor tick is not activity; it must not renew the role lease, or a dead
		// session would hold its role forever and mail would pile up unread.
		pending, err := Peek(e.Project, e.Role)
		if err != nil || len(pending) == 0 {
			continue
		}
		if !wakeWorthy(pending) {
			continue // fyi waits for a turn boundary; it never interrupts
		}
		ok, remaining := WakeAllowed(e.Project, e.Role)
		if !ok {
			fmt.Fprintf(diag, "mailroom watch: wake budget exhausted for %s; holding %d message(s)\n",
				id.Short(), len(pending))
			continue
		}
		msgs, err := Fetch(e.Project, e.Role)
		if err != nil || len(msgs) == 0 {
			continue // the Stop waiter got there first
		}
		out := Render(msgs, id.Short())
		if remaining <= 2 {
			out += fmt.Sprintf("(mailroom: %d wake(s) left this hour)\n", remaining)
		}
		// One Write, so the harness batches it into a single notification rather than
		// one per line.
		fmt.Fprint(w, out)
		any = true
	}
	return any
}
