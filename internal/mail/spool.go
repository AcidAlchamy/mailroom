package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func writeJSONAtomic(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Windows rename fails if the target exists.
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

// SendResult reports per-recipient outcomes. A full or unknown mailbox is DATA, not an
// error — callers must never see an exception they would be tempted to retry.
type SendResult struct {
	ID        string   `json:"id"`
	Hops      int      `json:"hops"`
	Delivered []string `json:"delivered"`
	Nacked    []Nack   `json:"nacked,omitempty"`
}

type Nack struct {
	Addr   string `json:"addr"`
	Reason string `json:"reason"`
}

// Send validates, persists, and fans out a message. Delivery is Maildir-style:
// write to tmp/, fsync, atomic rename into new/. No locks, no daemon, crash-safe.
func Send(from Identity, m *Message) (*SendResult, error) {
	if _, ok := Types[m.Type]; !ok {
		return nil, fmt.Errorf("unknown type %q (valid: %s)", m.Type, strings.Join(typeNames(), ", "))
	}
	if m.Priority == "" {
		m.Priority = "normal"
	}
	if !Priorities[m.Priority] {
		return nil, fmt.Errorf("unknown priority %q (valid: fyi, normal, now)", m.Priority)
	}
	if strings.TrimSpace(m.Subject) == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if len(m.To) == 0 {
		return nil, fmt.Errorf("at least one recipient is required (--to <role>)")
	}
	if len([]rune(m.Body)) > MaxBody {
		return nil, fmt.Errorf("body is %d chars; cap is %d — put the bulk in --ref, not the body",
			len([]rune(m.Body)), MaxBody)
	}
	if len([]rune(m.Subject)) > MaxSubject {
		return nil, fmt.Errorf("subject is %d chars; cap is %d", len([]rune(m.Subject)), MaxSubject)
	}
	if isPleasantry(m.Body) && m.Type != "ack" {
		return nil, fmt.Errorf("R5: content-free acknowledgement — use --type ack, or say nothing. Silence is a valid contribution")
	}
	if m.Hops > MaxHops {
		return nil, fmt.Errorf("R4: thread is frozen at %d hops — decide, hand off, or escalate", MaxHops)
	}
	if m.Hops > ArtifactAfter && m.Refs.Empty() {
		return nil, fmt.Errorf("R4: past hop %d a reply must carry an artifact (--ref file:line, a commit, or a PR)", ArtifactAfter)
	}
	if s := scanSecrets(m.Body + "\n" + m.Subject); s != "" {
		return nil, fmt.Errorf("refusing to send: body looks like it contains a secret (%s)", s)
	}

	m.MR = MR
	m.ID = NewID()
	m.TS = time.Now().UTC()
	m.Realm = Realm()
	m.Project = from.Project
	m.From = From{Addr: from.Addr(), Host: from.Host}
	m.Trust = "" // receiver stamps this; never the sender
	if m.Thread == "" {
		m.Thread = m.ID
	}

	if err := writeJSONAtomic(filepath.Join(msgsDir(m.Project), m.ID+".json"), m); err != nil {
		return nil, err
	}

	res := &SendResult{ID: m.ID, Hops: m.Hops}
	for _, to := range m.To {
		role := normalizeRole(to, m.Project)
		if role == from.Role {
			res.Nacked = append(res.Nacked, Nack{Addr: to, Reason: "cannot send to yourself"})
			continue
		}
		if _, err := LoadIdentity(m.Project, role); err != nil {
			res.Nacked = append(res.Nacked, Nack{Addr: to, Reason: "no such role enrolled in this project"})
			continue
		}
		if err := deliver(m.Project, role, m.ID); err != nil {
			res.Nacked = append(res.Nacked, Nack{Addr: to, Reason: err.Error()})
			continue
		}
		res.Delivered = append(res.Delivered, role)
	}
	return res, nil
}

// deliver writes an inbox pointer atomically: tmp/ -> fsync -> rename into new/.
// A reader can never observe a partial message.
func deliver(project, role, id string) error {
	base := inboxDir(project, role)
	for _, sub := range []string{"tmp", "new", "cur"} {
		if err := os.MkdirAll(filepath.Join(base, sub), 0o700); err != nil {
			return err
		}
	}
	tmp := filepath.Join(base, "tmp", id)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(id); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(base, "new", id))
}

func LoadMessage(project, id string) (Message, error) {
	var m Message
	b, err := os.ReadFile(filepath.Join(msgsDir(project), id+".json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	m.Trust = "agent" // stamped here, by the receiver
	return m, nil
}

func listIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids) // ids are time-sortable
	return ids, nil
}

// Peek returns undelivered messages WITHOUT consuming them.
func Peek(project, role string) ([]Message, error) {
	ids, err := listIDs(filepath.Join(inboxDir(project, role), "new"))
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, id := range ids {
		if m, err := LoadMessage(project, id); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// Fetch returns undelivered messages and marks them delivered (new/ -> cur/).
func Fetch(project, role string) ([]Message, error) {
	base := inboxDir(project, role)
	ids, err := listIDs(filepath.Join(base, "new"))
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, id := range ids {
		m, err := LoadMessage(project, id)
		if err != nil {
			continue
		}
		dst := filepath.Join(base, "cur", id)
		_ = os.Remove(dst)
		if err := os.Rename(filepath.Join(base, "new", id), dst); err != nil {
			continue // another reader won the race; skip rather than double-deliver
		}
		out = append(out, m)
	}
	return out, nil
}

// History returns every message this role has already been delivered.
func History(project, role string, limit int) ([]Message, error) {
	ids, err := listIDs(filepath.Join(inboxDir(project, role), "cur"))
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	var out []Message
	for _, id := range ids {
		if m, err := LoadMessage(project, id); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// ---- wake budget ----
//
// Every induced turn costs tokens. A hostile or merely buggy peer must not be able to
// spend an unbounded amount of someone else's context. This is a hard requirement, not
// a nicety — Phase 0 proved wakes are cheap to trigger and repeatable.

const DefaultWakesPerHour = 12

func WakesPerHour() int {
	if v := os.Getenv("MAILROOM_WAKES_PER_HOUR"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultWakesPerHour
}

// WakeAllowed reports whether a wake may be spent, and records it if so.
func WakeAllowed(project, role string) (bool, int) {
	budget := WakesPerHour()
	path := wakeLog(project, role)
	cutoff := time.Now().Add(-time.Hour)

	var kept []int64
	if b, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var ts int64
			if _, err := fmt.Sscanf(line, "%d", &ts); err == nil {
				if time.UnixMilli(ts).After(cutoff) {
					kept = append(kept, ts)
				}
			}
		}
	}
	if len(kept) >= budget {
		return false, 0
	}
	kept = append(kept, time.Now().UnixMilli())
	var sb strings.Builder
	for _, ts := range kept {
		fmt.Fprintf(&sb, "%d\n", ts)
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(sb.String()), 0o600)
	return true, budget - len(kept)
}

// ---- helpers ----

func typeNames() []string {
	var out []string
	for k := range Types {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeRole accepts "backend", "backend@project", or "project/backend".
func normalizeRole(to, project string) string {
	to = strings.TrimSpace(to)
	if i := strings.Index(to, "@"); i > 0 {
		return slug(to[:i])
	}
	if i := strings.LastIndex(to, "/"); i >= 0 {
		return slug(to[i+1:])
	}
	return slug(to)
}

var pleasantries = map[string]bool{
	"thanks": true, "thank you": true, "got it": true, "sounds good": true,
	"ok": true, "okay": true, "will do": true, "ack": true, "noted": true,
	"understood": true, "roger": true, "yep": true, "yes": true, "no problem": true,
}

func isPleasantry(body string) bool {
	s := strings.ToLower(strings.TrimSpace(body))
	s = strings.Trim(s, ".!? \t\n")
	return pleasantries[s]
}

var secretPatterns = map[string]string{
	"anthropic/openai key": `sk-[A-Za-z0-9_-]{16,}`,
	"github token":         `gh[pousr]_[A-Za-z0-9]{20,}`,
	"aws access key":       `AKIA[0-9A-Z]{16}`,
	"private key block":    `-----BEGIN [A-Z ]*PRIVATE KEY-----`,
	"slack token":          `xox[baprs]-[A-Za-z0-9-]{10,}`,
}

func scanSecrets(s string) string {
	for name, pat := range secretPatterns {
		if ok, _ := matchString(pat, s); ok {
			return name
		}
	}
	return ""
}
