# Your turn was cut off

This is the same session you were already working in: everything you completed
before this message still stands, and nothing from the turn that was cut off ran.

What happened: your last turn reached the model's output-token limit before it
finished, so whatever it was writing never completed — a tool call with empty
arguments, or a reply that stops mid-sentence, is what that looks like from the
runtime's side. The runtime reports a run like that as failed (there is no
completed turn to record), which is why you are being asked again; it retries
this only a bounded number of times, so another turn that gets cut off ends the
delegation for good.

Continue from where your last completed turn left off.

- Do not start over, and do not redo work a previous turn already finished: the
  edit, command output or file it produced is still there.
- Keep every turn small: one tool call per turn, and a payload of roughly 6000
  characters / 150 lines at most. Write a file in pieces — create it with the
  first piece, then extend it — instead of emitting the whole file at once or
  pasting one through a shell heredoc.
- Keep the reasoning before each call brief; the thinking tokens come out of the
  same turn budget as the tool call that follows them.

## Agent

{{AGENT}}

## Workspace

{{WORKSPACE}}
