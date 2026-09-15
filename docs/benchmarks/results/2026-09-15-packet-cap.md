# Packet cap by needle test — no retrieval ceiling at any size this hardware can serve

A 35B MoE recalled a random access code at **every depth of every packet size
tested, from 8,000 to 64,028 tokens**, across two server configurations. The
context window was doubled from 32,768 to 65,536 specifically to look for the
point where retrieval degrades. **It was not found.** Forty-five probes, no
misses.

The result is therefore a negative one, and worth stating plainly: on this
machine the packet cap is not set by what the model can retrieve from. It is set
by what the hardware can serve.

## What was measured

Design v3 §8.3 says "the needle test sets the hard packet cap per model
profile". A window a model *accepts* and a window it *retrieves from* are
different sizes, and the gap between them is where §8.3's context-retrieval
misses come from: the needed slice was in the packet and the model did not use
it. Deriving `max_packet_tokens` as half the context window is a statement about
arithmetic, not about the model.

A random 16-character access code is hidden at five depths in a packet of
Go-shaped filler and asked for back. The cap is the largest size where **every**
depth is recalled, not where the average is good: a packet builder cannot choose
where in a packet the needed slice lands, so a size that works at the edges and
fails in the middle is a size that fails.

## Result

```
qwen3.6-35b-a3b
3.39 characters per token plus 56 tokens of fixed prompt overhead,
measured against this model's own tokenizer

  asked    sent (min..max)      drift   0%  25%  50%  75% 100%   window
   8000     8007..8011    11 tok 0.14%   .    .    .    .    .    32768
  16000    15999..16003    3 tok 0.02%   .    .    .    .    .    32768
  22000    22013..22016   16 tok 0.07%   .    .    .    .    .    32768
  26000    26007..26011   11 tok 0.04%   .    .    .    .    .    32768
  30000    30002..30007    7 tok 0.02%   .    .    .    .    .    32768
  32000    32020..32024    4 tok 0.01%   .    .    .    .    .  32768 / 65536
  44000    44009..44011   11 tok 0.03%   .    .    .    .    .    65536
  56000    55996..55997    4 tok 0.01%   .    .    .    .    .    65536
  64000    64025..64028   28 tok 0.04%   .    .    .    .    .    65536

45 of 45 probes recalled the code. None missed, none truncated, none errored.
```

`recommended_cap` and `largest_tested` are both **64,028 measured tokens**, and
the tool's verdict is the right one: *"No ceiling found up to 64028 measured
tokens. That is not a measured limit: try larger sizes before treating it as
one."*

The 32,000 row was run under both configurations and gave the same answer, which
is what makes the two halves comparable: `32020..32024` at one slot of 65,536
against `32020..32024` at two slots of 32,768.

## Why it stopped at 64,028

`llama-server` was restarted as `-c 65536 --parallel 1`, giving a single slot of
65,536 tokens. A probe must fit its prompt *and* its completion budget, so
64,028 + 256 = 64,284 is near everything a slot will hold.

Going further means more KV cache. This model keeps 40 layers × 2 KV heads × 256
dimensions for keys and values, which at `q8_0` is about **43.5 KB per token** —
65,536 tokens costs roughly 2.85 GB, and the card has 8,188 MiB total. 131,072
would need about 5.7 GB of KV plus the resident weights and compute buffers,
which is close enough to the limit to be an experiment rather than a
continuation.

**The model is not the constraint.** Its trained context is 262,144
(`qwen35moe.context_length`), so every size above was served well inside what it
was trained for, with `rope.freq_base` untouched and no scaling applied. Nothing
here is measuring a model pushed past its design window.

## What this does and does not establish

It establishes that up to 64,028 tokens this model retrieves a specific fact
from a packet of code regardless of where in the packet it sits. Anything the
retrieval layer can fit in the window, the model can find.

It does **not** establish a retrieval ceiling, because none was reached at any
size the hardware could serve. Twice now the sweep has been extended
specifically to find one — from 30,007 to 32,024 by shrinking the completion
budget, then to 64,028 by doubling the window — and twice it has come back
clean.

It does not say `max_packet_tokens` should be raised. Raising it costs prefill
time on every step of every task, in exchange for a packet the retrieval layer
may have nothing to put in. That is a cost/benefit decision about retrieval, not
a conclusion from this measurement, and **no profile value was changed on the
strength of it.**

It also says nothing about recall over prose. The filler is code-shaped on
purpose — this measures what a packet of retrieved source does, and a model's
recall over English is not evidence about its recall over Go. Nor does a single
needle at a single depth resemble a task needing several scattered facts at
once; this is the easy version of the question, and it is the version §8.3 asks.

## How the sizes came to be trustworthy

The first three runs were wrong in the axis the answer is read off, and each
error was invisible until the numbers were looked at:

| Run | Asked | Sent | Error | Cause |
|---|---|---|---|---|
| 1 | 32,000 | 36,526 | 14% | assumed 4.0 characters per token |
| 2 | 8,000 | 8,465 | 5.8% at depth 0% only | filler density depended on where the needle sat |
| 3 | 8,000 | 8,244 | 3.1% at every depth | fixed prompt overhead folded into a per-character ratio |
| 4+ | 64,000 | 64,025 | 0.04% | — |

The third was diagnosable only because the second was fixed: once depth stopped
moving the number, the residual was flat across all five depths, which is the
signature of a constant rather than a scaling error. The sweep now calibrates
with two probe-shaped requests solved for slope and intercept — 3.39 characters
per token and 56 tokens of overhead, reproduced identically across three
separate runs and both server configurations — and every figure it reports is
the provider's own count rather than the size requested.

Two further defects were fixed because they would have produced a *false
ceiling*, which is the one error this test exists not to make:

- A probe refused for exceeding the window was being recorded as a recall
  failure. The model was never asked.
- An answer cut off at the completion budget was being recorded as a miss. That
  would report a ceiling at whatever size the model first decided to be wordy.

## Disclosure

| | |
|---|---|
| CPU | 13th Gen Intel Core i7-13620H, 16 threads |
| RAM | 61 GiB |
| GPU | NVIDIA GeForce RTX 4060 Laptop, 8188 MiB, driver 595.84 |
| Kernel | 7.0.0-31-generic |
| Model | `Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf`, sha256 `707a55a8a4397ecde44de0c499d3e68c1ad1d240d1da65826b4949d1043f4450` |
| Trained context | 262,144 tokens; no rope scaling applied |
| Server (runs 1–2) | `llama-server -c 65536 --parallel 2` → one slot of 32,768 |
| Server (run 3) | `llama-server -c 65536 --parallel 1` → one slot of 65,536 |
| Common flags | `-ngl 999 --n-cpu-moe 999 -fa on -ctk q8_0 -ctv q8_0 -b 2048 -ub 512 -t 8 -tb 16 --jinja` |
| Profile | `measured-7gb-gpu-61gb-ram` — `max_packet_tokens: 16384`, `reserved_output_tokens: 8192`, unchanged by this measurement |
| Commit | `df9e385` |
| Raw | [`2026-09-15-packet-cap.json`](2026-09-15-packet-cap.json) (8k–30k) and [`2026-09-15-packet-cap-65k.json`](2026-09-15-packet-cap-65k.json) (32k–64k), every probe with the answer it returned |

The KV cache is quantised to `q8_0` for both keys and values. That is part of
the configuration measured; a run with an unquantised cache is a different
measurement, and one that would not have fit 65,536 tokens on this card.

## Reproducing

```console
$ le models needle --sizes 32000,44000,56000,64000 --json
```

Add `--write` to save the measured cap into the active profile. It refuses when
the provider reports no token counts — the number is then the size asked for
rather than the size sent — and clamps to
`context_tokens - reserved_output_tokens`, because a packet that leaves no room
for the answer is not a packet the profile can send.
