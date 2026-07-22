---
name: desk
description: Show the human's Mailroom decision queue - escalations from agents that need a human answer. Use when the user says /mailroom:desk, asks what needs their input, what agents are waiting on, or what decisions are pending.
user-invocable: true
disable-model-invocation: true
---

# The Desk

**This is the human's console. You cannot open it on your own — only the user can.**

```bash
mailroom desk
```

Shows every open escalation: the decision, the options with their consequences, what was
already tried, the evidence, and what happens if the user says nothing.

## Answer

```bash
mailroom desk answer <id> <option-id>
```

The asking agent is notified immediately as a `now`-priority decision, which wakes it even
if it has been idle for hours. It carries on without the user having to relay anything.

## Defer

```bash
mailroom desk defer <id> 4h
```

Pushes the countdown out without answering.

## Nothing rots

Every escalation carries a `default_if_silent`. When the countdown runs out, Mailroom
applies that default, closes the item, and tells the asker. **The user being asleep, busy,
or simply uninterested never stalls a pipeline.** Opening the desk also sweeps expired
countdowns first, so the list is always current.

## When presenting this to the user

Lead with blocking items. For each one give the action, the options and their consequences,
and the deadline — in that order. Do not editorialise or add your own recommendation unless
asked; the agent already stated the trade-offs and the user is deciding, not consulting.

If the desk is clear, say so in one line and stop.
