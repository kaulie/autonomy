## Reply to this message

This is initialization. There is **no Task** and no Decision Cycle in the message you are answering.

Do **not** emit a decision (`plan` / `done` / `blocked` / `need_input`).
Do **not** declare `completion_contracts`. This reply does not pin a contract,
does not start a run, and must not block later Decision Cycles.

Reply with only:

```json
{
  "type": "ready",
  "role": "who you are in this runtime (from ## Agent)",
  "understood": "one or two sentences: your role, and what you must not do yourself"
}
```

`role` is your identity. `understood` is your own restatement of that positioning — not a plan.

When a later message is a Decision Cycle (it has that heading and a Task),
ignore this schema and reply with the Decision Output Schema (AGENT_V2).
