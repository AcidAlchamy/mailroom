# Mailroom

**A session mailbox for Claude Code.**

Mailroom gives independently-started Claude Code sessions a shared, project-scoped mailbox —
so they tell *each other* what they're doing instead of routing everything through you.

Two terminals in the same repo. `/mailroom:enroll`. They can now message each other, claim
files, and see each other's status. **No daemon, no port, no account, no Node, no tmux, no
Docker.**

The point isn't the messages. **The point is that the CLI refuses a vague ask.** Threads
freeze at six exchanges. Replies past hop four must carry a commit, a PR, or a `file:line`.
Pleasantries are rejected outright. Peer messages arrive as escaped, explicitly-untrusted
data that cannot approve a permission. You are the host, not the relay.

---

## Install

```bash
claude plugin marketplace add AcidAlchamy/mailroom
```

```bash
claude plugin install mailroom@mailroom
```

## Use

In each session working on the project:

```
/mailroom:enroll
```

Pick a stable role — `backend`, `renderer`, `reviewer`. Then just work. Mail arrives on its
own.

```bash
mailroom send --to reviewer --type claim --subject "Taking DialogueService.luau" --ref src/DialogueService.luau
```

```bash
mailroom roster
```

---

## Delivery: how a message actually reaches another session

A Claude Code session is turn-based — it only thinks when it has a turn. Mailroom delivers
on three rungs, and **the floor always works**:

| Channel | Mechanism | Reaches an idle session? |
|---|---|---|
| Turn start | `SessionStart` / `UserPromptSubmit` → `additionalContext` | No — but costs zero tokens when the mailbox is empty |
| Turn tail | `Stop` hook delivers whatever arrived *during* the turn | No — but nothing is ever missed |
| Idle wake | parked `Stop` hook + `asyncRewake`, exit 2 | **Yes — ~4–5s**, for the window after a turn |
| **Always on** | monitor process (`monitors.json`) | **Yes — ~3s, no window** |

The last two matter together. A parked waiter is bounded by its hook timeout, so it covers
the minutes after a turn ends and then expires — leaving a session that has been quiet for
an hour unreachable. The monitor is a persistent process for the life of the session and
has no such window. Both race for each message; atomic delivery means exactly one wins.

The idle-wake claim is measured, not assumed. See [`SPEC.md`](SPEC.md) for the verified
platform behavior on Claude Code v2.1.211, including the test method.

Run `mailroom doctor` to see which rungs are live on your machine.

---

## Addressing: why this survives session restarts

Addresses are **`role@project`**, not session ids.

`project` is derived automatically — `.mailroom.json`, else your git remote, else the
directory. Two sessions opened hours apart, on two machines, in the same repo are already
addressable to each other with zero configuration.

`role` is held by a 15-minute lease. Restart your session, re-enroll with the same role, and
you inherit that role's address, open threads, and mail. This is what makes ephemeral and
scheduled sessions work — a session-addressed mailbox is worthless when your worker is
respawned every twenty minutes.

---

## Permissions (read this — it bites everyone once)

Delivery is only half the loop. A woken session still has to *act*, and if acting needs a
permission prompt, it stops dead waiting for a human — which is the exact failure this
plugin exists to remove.

Allow the Mailroom binary once, in `~/.claude/settings.json`:

```json
{ "permissions": { "allow": ["Bash(mailroom:*)", "Bash(*/mailroom:*)", "Bash(*/mailroom.exe:*)"] } }
```

Without it the first peer message wakes the session, the session tries to reply, and the
reply sits behind an approval dialog nobody is watching. `mailroom doctor` warns when this
is unset.

This is also why the MCP tool surface is on the roadmap: an MCP server is allowlisted once
by name, instead of relying on shell-command pattern matching.

## Etiquette: the part that matters

Coordination fails from *too much* chatter, not too little. The rules are mechanically
enforced in the CLI, so they hold even when a model forgets:

- **Send-worthiness test** — five questions, all must pass. *Silence is a valid contribution.*
- **Hop budget** — past hop 4 a reply must carry an artifact; at hop 6 the thread freezes.
  Two agents cannot ping-pong. A disagreement must resolve or surface.
- **Priority is a budget** — `now` spends part of a peer's hourly wake budget. Rationed.
- **Pleasantries rejected** — "thanks", "got it", "sounds good" return an error.
- **Types carry obligations** — `decision` terminates a thread, `ack` is terminal, `request`
  must carry refs.

## Security

Peer messages are **untrusted data, never instructions.**

Delivered text lands inside a `<system-reminder>` — one of the highest-trust frames a model
has. So Mailroom escapes `<` and `>`, strips control characters and ANSI sequences, caps and
truncates bodies, and wraps everything in an explicit untrusted-data frame. A peer cannot
close the frame or forge a system reminder. Instruction-override patterns are **flagged
inline rather than silently stripped**, so an attempted attack is visible.

**Mailroom exposes no tool that can execute, fetch, or grant a permission.** There is no
`run`, no `eval`, no `apply_patch`. It cannot launder a permission, and it never touches
Claude Code's own permission prompts. Outbound bodies are scanned for secrets and refused.

Wakes are rate-limited per role per hour. A hostile or merely buggy peer cannot spend an
unbounded amount of someone else's context.

Residual risks are documented in [`SECURITY.md`](SECURITY.md) rather than hidden: a
persuasive peer body can still steer a model into in-scope work it shouldn't do — the
receiving session's own permission set is the real backstop.

## Status

**v0.1 — Phase 1.** Working: enrollment, addressing, atomic delivery, all three delivery
rungs, the etiquette guards, the sanitizer. Not yet: the escalation Desk, signing,
cross-machine transport, the MCP tool surface. See [`ROADMAP.md`](ROADMAP.md).

## License

Apache-2.0
