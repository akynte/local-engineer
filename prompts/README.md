# Prompts

§1.2 gives prompts their own directory, and the reason is review rather than
tidiness. A prompt is behaviour: it decides what the model does when the
objective is ambiguous, whether it verifies before claiming completion, and how
it reads a failure. Behaviour that lives inside a string literal in a `.go` file
gets changed without review, because a diff to a Go file reads as code.

These files are embedded into the binary at build time, so there is no runtime
path to load an arbitrary prompt: a prompt is part of the build, not a
configuration knob a task could reach.

| File | Used by |
|---|---|
| [`engine-system.md`](engine-system.md) | the editing engine's system turn |

## The shape these are written in

Short and concrete. A long prompt of exhortations costs prefill on every step
and does not make a small model more careful; naming the loop and the stopping
condition does. §8.2's cache-aware layout also depends on the system turn being
*stable* — it is the prefix the prompt cache reuses, so text that varies per
task belongs in the user turn, never here.

Two rules follow from that, and both are tested:

- **No interpolation.** A prompt with a task-specific value in it breaks the
  cache prefix for every subsequent step.
- **No exhortation.** "Be careful", "think step by step" and "you are an expert"
  are tokens spent on every call for no measured effect. If a behaviour matters,
  name the action that produces it.
