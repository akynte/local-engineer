# Packet cap by needle test — no retrieval ceiling below the context window

A 35B MoE recalled a random access code at every depth of every packet size
tested, from 8,000 to 30,007 tokens, on a machine whose per-slot context window
is 32,768. **No retrieval ceiling was found.** The measurement's own conclusion
is therefore a negative one: at this context size the packet cap is bounded by
the window, not by what the model can retrieve from, and
`max_packet_tokens: 16384` is well inside what the model demonstrably handles.

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

  asked    sent (min..max)          drift  depths 0% 25% 50% 75% 100%
   8000     8007..8011        11 tok  0.14%   .   .   .   .   .
  16000    15999..16003        3 tok  0.02%   .   .   .   .   .
  22000    22013..22016       16 tok  0.07%   .   .   .   .   .
  26000    26007..26011       11 tok  0.04%   .   .   .   .   .
  30000    30002..30007        7 tok  0.02%   .   .   .   .   .

25 of 25 probes recalled the code. No probe errored.
```

`recommended_cap` and `largest_tested` are both **30,007 measured tokens**,
which the tool reports as "no ceiling found up to 30,007 measured tokens — that
is not a measured limit". It is the right thing for it to say. Recall did not
fail anywhere the window allowed it to be tested.

## Why the sweep stopped at 30,000

`llama-server` was started with `-c 65536 --parallel 2`, so each slot gets
32,768 tokens. The probe reserves 2,048 of those for the answer, leaving about
30,720 that can be filled with haystack. 30,000 is the largest round size inside
that.

Testing past 32,768 needs the server restarted with `--parallel 1`, which would
also make the profile's `context_tokens: 32768` wrong — that field has to match
what the server was started with. That is a different measurement on a different
configuration, and it has not been run.

## What this does and does not establish

It establishes that at 32,768 tokens of served context this model is not the
binding constraint on packet size. Anything the retrieval layer can fit, the
model can find, at any depth.

It does **not** establish a retrieval ceiling, because none was reached. It does
not say `max_packet_tokens` should be raised: the cap that fits this profile is
`context_tokens - reserved_output_tokens` = 32768 − 8192 = **24,576**, and
raising 16,384 to that costs prefill time on every step of every task in
exchange for a packet the retrieval layer may not have anything to put in. That
is a cost/benefit decision about retrieval, not a conclusion from this
measurement, and no profile value was changed on the strength of it.

It also says nothing about recall over prose. The filler is code-shaped on
purpose — this measures what a packet of retrieved source does, and a model's
recall over English is not evidence about its recall over Go.

## How the sizes came to be trustworthy

The first three runs of this test were wrong in the axis the answer is read off,
and each error was invisible until the numbers were looked at:

| Run | Asked | Sent | Error | Cause |
|---|---|---|---|---|
| 1 | 32,000 | 36,526 | 14% | assumed 4.0 characters per token |
| 2 | 8,000 | 8,465 | 5.8% at depth 0% only | filler density depended on where the needle sat |
| 3 | 8,000 | 8,244 | 3.1% at every depth | fixed prompt overhead folded into a per-character ratio |
| 4 | 8,000 | 8,010 | 0.13% | — |

The third was diagnosable only because the second was fixed: once depth stopped
moving the number, the residual was flat across all five depths, which is the
signature of a constant rather than a scaling error. The sweep now calibrates
with two probe-shaped requests solved for slope and intercept, and every figure
it reports is the provider's own count rather than the size requested.

## Disclosure

| | |
|---|---|
| CPU | 13th Gen Intel Core i7-13620H, 16 threads |
| RAM | 61 GiB |
| GPU | NVIDIA GeForce RTX 4060 Laptop, 8188 MiB, driver 595.84 |
| Kernel | 7.0.0-31-generic |
| Model | `Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf`, sha256 `707a55a8a4397ecde44de0c499d3e68c1ad1d240d1da65826b4949d1043f4450` |
| Server | `llama-server -c 65536 --parallel 2 -ngl 999 --n-cpu-moe 999 -fa on -ctk q8_0 -ctv q8_0 -b 2048 -ub 512 -t 8 -tb 16 --jinja` |
| Profile | `measured-7gb-gpu-61gb-ram` — `context_tokens: 32768`, `max_packet_tokens: 16384`, `reserved_output_tokens: 8192` |
| Commit | `d7b3f09` |
| Raw | [`2026-09-15-packet-cap.json`](2026-09-15-packet-cap.json) — all 25 probes, with the answer each returned |

Note the KV cache is quantised to `q8_0` for both keys and values. That is part
of the configuration measured, and a run with an unquantised cache is a
different measurement.

## Reproducing

```console
$ le models needle --sizes 8000,16000,22000,26000,30000 --json
```

Add `--write` to save the measured cap into the active profile. It refuses when
the provider reports no token counts — the number is then the size asked for
rather than the size sent — and clamps to
`context_tokens - reserved_output_tokens`, because a packet that leaves no room
for the answer is not a packet the profile can send.
