---
name: escalate
description: Escalate a decision to the human through the Mailroom Desk when it genuinely cannot be resolved by agents. Use when you are blocked on something only a person can decide - money, scope, product taste, credentials, or anything irreversible - or when you are about to ask the user an open-ended question about ongoing agent work. Triggers - escalate, need a decision, blocked on the user, ask the owner, needs human input, product call, only they can decide.
---

# Escalating to the human

The human is the host, not the help desk. Before you even consider this:

1. **Ask the peer.** Most blocks are another agent's call, not the human's. Use
   `mailroom send --type question` or `--type blocked`.
2. **Check the repo.** If the answer is in code, git history, or a PR, it is not an escalation.
3. **Decide it yourself** if it is reversible and inside your remit. Record it with
   `--type decision` so peers can see the call and the reasoning.

Escalate only for what **only a person** can settle: money, scope, product taste,
credentials, physical-world actions, or anything irreversible.

## The contract

> An escalation is actionable **if and only if** the human can resolve it in ONE ACTION,
> without reading the thread, without asking a clarifying question, and silence has a
> defined consequence.

The CLI enforces this. A vague ask is **refused** and never reaches anyone.

```bash
mailroom escalate \
  --verdict "One sentence: what is blocked and why agents cannot resolve it" \
  --action "Approve or reject: raise the tier-3 quest reward from 250 to 400 coins" \
  --option "a:Keep the cap at 250:quest spec needs a rewrite, ~1 day" \
  --option "b:Raise the cap to 400:one economy migration, touches save data" \
  --default "a:2h" \
  --tried "asked backend@ — owns Economy.luau but says it is a product call" \
  --evidence "src/Economy.luau:120-160" --evidence "PR #241" \
  --blocking \
  --cost-of-delay "quest builder demo blocked" \
  --blast-radius "one migration on persistent save data, reversible for 7 days"
```

## The rules, and why

- **`--action` must be an instruction, not a question.** It cannot end in `?` and cannot
  open with *what / should / how / thoughts / wdyt / let me know / your call*. It must start
  with a decision verb: approve, reject, choose, confirm, set, provide, authorize, merge…
- **At least two `--option`s, each with a consequence.** One option is not a decision. The
  consequence is what lets the human choose without reading anything else.
- **`--tried` is required.** An escalation with nothing tried is a research task. Go do it.
- **`--evidence` is required.** A file:line, PR, commit, or test output.
- **`--default` is required** — the load-bearing field. It turns your escalation from a
  blocker into a countdown. When it expires the default applies automatically, you are
  notified, and work continues. **Never assume the human will answer.** Choose the safest
  reversible option as the default.
- **`--blocking` also needs `--cost-of-delay` and `--blast-radius`.** If nothing stops
  moving, it is not blocking — and only one blocking ask per project per 30 minutes.
- **Three open items per agent, maximum.** If you are at the cap, consolidate. Drowning is
  not a reason to page a human five times.

## If you are refused

The refusal lists each failing field with a hint. **Rewrite; do not retry the same ask, and
do not route around it by messaging the user directly.** If you cannot state two options and
a safe default, you have not finished thinking — that thinking is your job, not theirs.

## After escalating

Say so in one line, then **keep working on everything not blocked by it**. The answer will
arrive as a `now`-priority `decision` message and will wake you.
