# Phase 0 — Verified platform behavior

**Claude Code v2.1.211 · Windows 11 · Git Bash present · 2026-07-22**

Method: throwaway plugin loaded with `claude --plugin-dir`, one interactive session left
genuinely idle, external pokes fired from a second machine-local process. Every claim below is
backed by a timestamped line in `wake.log` plus the woken session's own transcript.

## Verdict: **idle wake works.** Two independent mechanisms, both repeatable.

| Rung | Mechanism | Result | Latency | Notes |
|---|---|---|---|---|
| 0 | `SessionStart` / `UserPromptSubmit` → `additionalContext` | **WORKS** | n/a | The floor. Zero tokens when mailbox empty. |
| 1 | `Stop` hook, `async:true` + `asyncRewake:true`, long-poll, exit 2 | **WORKS** | **4–5 s** | The primary channel. |
| 3 | `monitors/monitors.json` persistent process, stdout line | **WORKS** | **3 s** | Also induces a turn. Loads under `--plugin-dir`. |
| 4 | `Notification` matcher `idle_prompt` + `asyncRewake` | **NEVER FIRED** | — | Hook never armed in ~4 min idle. Likely gated on notification config/terminal focus. Not needed. |

## The key findings

**1. `async: true` detaches the hook process from the session.**
Observed `ps` output: the parked waiter had **parent PID 1**. It survives the turn ending and
keeps running while the session is idle at the prompt. This is what makes a long-poll waiter
viable — it was the single biggest unknown going in.

**2. `asyncRewake` exit 2 induces a real turn in an idle session.**
Session idle 81 s → poke → waiter exit 2 → `UserPromptSubmit` hook fired 5 s later with no human
input. The woken session read the payload and responded on its own.

**3. Consecutive wakes are NOT suppressed by `stop_hook_active: true`.**
Second wake after 22 s idle worked identically. The anti-loop guard does not gate this path.
Continuous operation is therefore real, not a one-shot.

**4. The wake re-arms itself.**
Each induced turn ends → `Stop` fires → a new waiter parks. Self-sustaining with no supervisor.

**5. Monitors load under `--plugin-dir` and their stdout induces a turn.**
Contradicts the assumption that monitors are inert for non-installed plugins. Monitor output
arrives in a *different envelope* than the Stop path (see below).

## Delivery envelopes — DESIGN-CRITICAL

The two channels deliver into **different frames**, and both are high-trust:

Stop / asyncRewake — hook **stderr** is embedded in a `<system-reminder>`:

    <system-reminder>
    Stop hook blocking error from command "Stop": <OUR STDERR VERBATIM>
    </system-reminder>

Monitor — stdout is embedded in a `<task-notification>` with an `<event>` tag, plus
appended guidance about whether to notify the user.

**Security consequence, load-bearing:** peer message content lands inside `<system-reminder>` —
one of the highest-trust frames the model has. Anything Mailroom writes to stderr/stdout is
therefore an injection surface. Bodies MUST be escaped (neutralize `<` `>`), length-capped, and
wrapped in an explicit untrusted-data frame *inside* the reminder. Never pass a peer body through
raw. This confirms the design's escaping requirement is mandatory, not defensive polish.

## Consequences for the design

- The README may say **continuous**. Claim verified on this platform/version.
- Primary channel: parked `Stop` + `asyncRewake`. Fallback/complement: monitor.
- `mailroom doctor` should probe both and report which are live on the user's machine.
- Do not depend on `Notification`/`idle_prompt`; treat as opportunistic only.
- Long-poll deadline must be < the hook `timeout`; re-arm on every `Stop`.
- Wake budget/rate limiting is now a *requirement*, not a nicety — a hostile or buggy peer can
  induce unlimited turns, and every induced turn costs tokens.
