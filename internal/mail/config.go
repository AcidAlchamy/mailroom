package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultRealm is the trust domain used when the user has not joined another.
// Single-machine users never see it.
const DefaultRealm = "local"

// Root returns the mailroom data directory. Override with MAILROOM_ROOT.
func Root() string {
	if r := os.Getenv("MAILROOM_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mailroom"
	}
	return filepath.Join(home, ".mailroom")
}

// Realm returns the active trust domain.
func Realm() string {
	if r := os.Getenv("MAILROOM_REALM"); r != "" {
		return r
	}
	return DefaultRealm
}

// SessionID is the Claude Code session this process was invoked from.
// Hooks receive it on stdin; Bash-invoked CLI calls read it from the environment.
func SessionID() string {
	for _, k := range []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

var exePath string

// ExePath is the absolute path to this binary, quoted if it contains spaces.
// Plugin bin/ is not on PATH, so every command we suggest to a model must be
// fully qualified or the model will guess and fail.
func ExePath() string {
	if exePath != "" {
		return exePath
	}
	p, err := os.Executable()
	if err != nil || p == "" {
		exePath = "mailroom"
		return exePath
	}
	p = filepath.ToSlash(p)
	if strings.ContainsAny(p, " \t") {
		p = `"` + p + `"`
	}
	exePath = p
	return exePath
}

func Host() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return strings.ToLower(h)
}

var slugRe = regexp.MustCompile(`[^a-z0-9._-]+`)

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ProjectID derives the routing key for a working directory.
//
// Order: .mailroom.json "project" -> git remote origin basename -> dir basename + path hash.
// Two sessions opened hours apart, on different machines, in the same repo resolve to the
// same key with zero configuration. That is the whole point.
func ProjectID(dir string) string {
	if v := os.Getenv("MAILROOM_PROJECT"); v != "" {
		return slug(v)
	}
	if p := projectFromFile(dir); p != "" {
		return p
	}
	if p := projectFromGit(dir); p != "" {
		return p
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.ToSlash(abs))))
	return fmt.Sprintf("%s-%s", slug(filepath.Base(abs)), hex.EncodeToString(sum[:])[:6])
}

type projectFile struct {
	Project string `json:"project"`
	Realm   string `json:"realm"`
}

func projectFromFile(dir string) string {
	d := dir
	for i := 0; i < 24; i++ {
		b, err := os.ReadFile(filepath.Join(d, ".mailroom.json"))
		if err == nil {
			var pf projectFile
			if json.Unmarshal(b, &pf) == nil && pf.Project != "" {
				return slug(pf.Project)
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return ""
}

func projectFromGit(dir string) string {
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	if url == "" {
		return ""
	}
	url = strings.TrimSuffix(url, ".git")
	url = strings.TrimSuffix(url, "/")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return slug(url)
}

// ---- paths ----

func RealmDir() string { return filepath.Join(Root(), "realms", Realm()) }

func ProjectDir(project string) string {
	return filepath.Join(RealmDir(), "projects", project)
}

func msgsDir(project string) string   { return filepath.Join(ProjectDir(project), "msgs") }
func agentsDir(project string) string { return filepath.Join(ProjectDir(project), "agents") }
func inboxDir(project, role string) string {
	return filepath.Join(ProjectDir(project), "inbox", role)
}
func wakeLog(project, role string) string {
	return filepath.Join(ProjectDir(project), "wake", role+".jsonl")
}
func sessionIndex(sid string) string {
	return filepath.Join(Root(), "sessions", sid+".json")
}

// ---- identity ----

// Identity is one enrolled session holding one role in one project.
type Identity struct {
	Project    string    `json:"project"`
	Role       string    `json:"role"`
	Display    string    `json:"display,omitempty"`
	SessionID  string    `json:"session_id"`
	Host       string    `json:"host"`
	PID        int       `json:"pid"`
	Cwd        string    `json:"cwd"`
	EnrolledAt time.Time `json:"enrolled_at"`
	LastSeen   time.Time `json:"last_seen"`
	State      string    `json:"state"` // active | offline
	Note       string    `json:"note,omitempty"`
}

// Addr is the wire address: "<project>/<role>".
func (i Identity) Addr() string { return i.Project + "/" + i.Role }

// Short is the display form: "<role>@<project>".
func (i Identity) Short() string { return i.Role + "@" + i.Project }

// LeaseTTL is how long a role registration stays live without activity.
const LeaseTTL = 15 * time.Minute

func (i Identity) Live() bool {
	return i.State != "offline" && time.Since(i.LastSeen) < LeaseTTL
}

// Enroll registers this session as the holder of a role, taking over a stale lease
// if one exists. Idempotent per (project, role, session).
func Enroll(project, role, display, note string) (Identity, Identity, error) {
	role = slug(role)
	if role == "" {
		return Identity{}, Identity{}, fmt.Errorf("role must be a non-empty name like 'backend' or 'reviewer'")
	}
	var prev Identity
	if old, err := LoadIdentity(project, role); err == nil {
		prev = old
	}

	cwd, _ := os.Getwd()
	id := Identity{
		Project:    project,
		Role:       role,
		Display:    display,
		SessionID:  SessionID(),
		Host:       Host(),
		PID:        os.Getpid(),
		Cwd:        cwd,
		EnrolledAt: time.Now().UTC(),
		LastSeen:   time.Now().UTC(),
		State:      "active",
		Note:       note,
	}
	if prev.Role == role && prev.SessionID == id.SessionID && !prev.EnrolledAt.IsZero() {
		id.EnrolledAt = prev.EnrolledAt
	}

	if err := writeJSONAtomic(filepath.Join(agentsDir(project), role+".json"), id); err != nil {
		return Identity{}, prev, err
	}
	recordEnrollment(id.SessionID, project, role)
	for _, sub := range []string{"tmp", "new", "cur"} {
		_ = os.MkdirAll(filepath.Join(inboxDir(project, role), sub), 0o700)
	}
	return id, prev, nil
}

func LoadIdentity(project, role string) (Identity, error) {
	var id Identity
	b, err := os.ReadFile(filepath.Join(agentsDir(project), role+".json"))
	if err != nil {
		return id, err
	}
	err = json.Unmarshal(b, &id)
	return id, err
}

// sessionRec tracks every project this session is enrolled in. A session legitimately
// holds more than one address — one per project it is working on.
type sessionRec struct {
	Current     enrollRef   `json:"current"`
	Enrollments []enrollRef `json:"enrollments"`
}

type enrollRef struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	Realm   string `json:"realm"`
}

func loadSession(sid string) sessionRec {
	var rec sessionRec
	b, err := os.ReadFile(sessionIndex(sid))
	if err != nil {
		return rec
	}
	if json.Unmarshal(b, &rec) == nil && rec.Current.Project != "" {
		return rec
	}
	// Tolerate the flat v0 format so an in-flight session keeps working across upgrades.
	var flat enrollRef
	if json.Unmarshal(b, &flat) == nil && flat.Project != "" {
		return sessionRec{Current: flat, Enrollments: []enrollRef{flat}}
	}
	return rec
}

func recordEnrollment(sid, project, role string) {
	if sid == "" {
		return
	}
	rec := loadSession(sid)
	ref := enrollRef{Project: project, Role: role, Realm: Realm()}
	found := false
	for i, e := range rec.Enrollments {
		if e.Project == project {
			rec.Enrollments[i] = ref
			found = true
			break
		}
	}
	if !found {
		rec.Enrollments = append(rec.Enrollments, ref)
	}
	rec.Current = ref
	_ = writeJSONAtomic(sessionIndex(sid), rec)
}

// Whoami resolves the calling session's identity. Pass a project to select among
// multiple enrollments; empty selects the most recent.
func Whoami(project string) (Identity, error) {
	if role := os.Getenv("MAILROOM_ROLE"); role != "" {
		p := project
		if p == "" {
			cwd, _ := os.Getwd()
			p = ProjectID(cwd)
		}
		return LoadIdentity(p, slug(role))
	}
	sid := SessionID()
	if sid == "" {
		return Identity{}, fmt.Errorf("no session id: set MAILROOM_ROLE or run inside Claude Code")
	}
	rec := loadSession(sid)
	if rec.Current.Project == "" {
		return Identity{}, fmt.Errorf("this session is not enrolled — run /mailroom:enroll first")
	}
	if project == "" {
		return LoadIdentity(rec.Current.Project, rec.Current.Role)
	}
	for _, e := range rec.Enrollments {
		if e.Project == project {
			return LoadIdentity(e.Project, e.Role)
		}
	}
	var have []string
	for _, e := range rec.Enrollments {
		have = append(have, e.Project)
	}
	return Identity{}, fmt.Errorf("not enrolled in project %q (enrolled in: %s)",
		project, strings.Join(have, ", "))
}

// Touch renews the role lease and records liveness.
func Touch(project, role, state, note string) error {
	id, err := LoadIdentity(project, role)
	if err != nil {
		return err
	}
	id.LastSeen = time.Now().UTC()
	if state != "" {
		id.State = state
	}
	if note != "" {
		id.Note = note
	}
	return writeJSONAtomic(filepath.Join(agentsDir(project), role+".json"), id)
}

// Neighbours returns live agents in OTHER projects in this realm.
//
// The most likely first-run failure is two sessions started from different working
// directories: both enroll successfully, into different projects, and then sit staring at
// an empty roster wondering why the other one is invisible. Project isolation is correct
// behaviour, so we do not guess — we surface it and let the human choose.
func Neighbours(exclude string) map[string][]Identity {
	out := map[string][]Identity{}
	entries, err := os.ReadDir(filepath.Join(RealmDir(), "projects"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == exclude {
			continue
		}
		peers, err := Roster(e.Name())
		if err != nil {
			continue
		}
		for _, p := range peers {
			if p.Live() {
				out[e.Name()] = append(out[e.Name()], p)
			}
		}
	}
	return out
}

// Roster lists every agent registered in a project, live or not.
func Roster(project string) ([]Identity, error) {
	entries, err := os.ReadDir(agentsDir(project))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Identity
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id, err := LoadIdentity(project, strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// PermissionAllowed reports whether the user has allowlisted the mailroom binary.
//
// This is not paranoia about settings hygiene: delivery is only half the loop. A session
// woken by a peer still has to act, and an unapproved Bash command stalls on a dialog
// nobody is watching — reintroducing the human as a blocking dependency, which is the
// exact failure this plugin exists to remove.
func PermissionAllowed() (bool, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "~/.claude/settings.json"
	}
	path := filepath.Join(home, ".claude", "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return false, path
	}
	var cfg struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return false, path
	}
	for _, rule := range cfg.Permissions.Allow {
		if strings.Contains(rule, "mailroom") {
			return true, path
		}
	}
	return false, path
}
