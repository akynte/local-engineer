# Choose a hardware profile

A **profile** holds every value that depends on your machine and your model:
context window, packet cap, reserved output, memory budgets, thread counts,
offload layers, sampling defaults, thinking policy, and how many tasks may run
at once.

None of these are hardcoded anywhere. The shipped profiles are *starting
points*, not measurements, and `le doctor` warns until you replace them with
your own.

## See what ships

<!-- test:run -->
```console
$ le config profiles
  apple-silicon-unified            shipped  …
  cpu-only-32gb-ram                shipped  …
  cpu-only-64gb-ram                shipped  …
  cuda-16gb                        shipped  …
  cuda-24gb                        shipped  …
  external-inference               shipped  …
* reference-8gb-cuda-64gb-ram      shipped  …
  remote-provider                  shipped  …
  resident-8gb-cuda                shipped  …

* is the active profile. Shipped profiles are starting points, not measurements:
  run `le models bench --write` to measure this machine.
```

The `*` marks the active one, and the second column says whether it is a
shipped starting point or one you measured.

| Profile | For |
|---|---|
| `reference-8gb-cuda-64gb-ram` | 8 GB CUDA GPU, large host RAM, MoE model with CPU expert offload |
| `resident-8gb-cuda` | A 9B-class dense model held entirely in 8 GB of VRAM |
| `cuda-16gb` | 16 GB CUDA: a 14B-class dense model resident with a 32k window |
| `cuda-24gb` | 24 GB CUDA: a 32B-class dense model resident with a 64k window |
| `apple-silicon-unified` | Apple Silicon, Metal offload, unified memory |
| `cpu-only-32gb-ram` | No GPU: a small dense model and much smaller packets |
| `cpu-only-64gb-ram` | No GPU, plenty of RAM: a sparse MoE model, prefill-bound |
| `external-inference` | Inference runs elsewhere |
| `remote-provider` | A hosted model, offline lanes disabled |

Two things are worth knowing before picking by VRAM alone.

**Unified memory is not VRAM.** On Apple Silicon there is no separate pool to
run out of; the limit is the machine's memory and the share the OS will wire,
which is why `apple-silicon-unified` sets a RAM budget and no VRAM one.

**Sparse and dense models fail differently.** A MoE model activates a few
billion parameters per token, so it decodes on hardware that could never run a
dense model of the same size — but prefill touches every expert across a batch,
so it stays slow. That is why the CPU MoE profile caps packets well below its
context window: the window is what fits, the cap is what finishes.

## Measure your own machine

```console
$ le models bench
benchmarking local (llamacpp)…
  iteration 1 of 3
  …
────────────────────────────────────────────────────────
model              your-model
prefill            412.6 tok/s
decode             28.4 tok/s
first token        820 ms (p50), 1140 ms (p95)
prompt cache reuse 96.4%
peak VRAM          7104 MB of 8188 MB
peak RAM           2210 MB of 65536 MB
────────────────────────────────────────────────────────

Proposed profile "measured-7gb-gpu-64gb-ram" (pass --write to save it):
  context_tokens        32768
  max_packet_tokens     16384
  reserved_output       8192
  max_concurrent_tasks  1
```

Then save it and make it active:

```console
$ le models bench --write
wrote /data/config/profiles/measured-7gb-gpu-64gb-ram.yaml
Set `profile: measured-7gb-gpu-64gb-ram` in le.yaml to make it active.
```

## What the benchmark measures, and what it estimates

Measured directly: decode throughput, time to first token, peak VRAM during the
run, prompt-cache reuse, and the model name the provider reports.

**Apportioned, not measured directly:** the split between prefill and decode
rate. Without token-level timings, the two rates are derived from per-request
totals. The generated profile says so in its own description, so nobody reads
those two numbers as more precise than they are.

Prompt-cache reuse is worth watching. The benchmark sends a byte-identical
prefix on every iteration, exactly as a real packet is laid out — stable
content first, varying content last. A low reuse number means something in
your setup is invalidating the cache.

## When a profile does not fit

```console
$ le doctor
[warn] profile fits host   profile needs 9000 MB VRAM, host has 8188 MB
                           Choose a smaller profile, or re-run `le models bench` on this machine.
```

Concurrency is derived conservatively: a second concurrent task is only allowed
when two copies of the measured peak still leave 15% VRAM headroom.

## Tuning by hand

Profiles are plain YAML. The values that matter most:

| Field | Effect |
|---|---|
| `context_tokens` | The model's window. Must match what the server was started with. |
| `max_packet_tokens` | The hard cap on retrieved context per step. |
| `reserved_output_tokens` | Room left for the answer. `max_packet_tokens + reserved_output_tokens` must not exceed `context_tokens`, and validation enforces it. |
| `max_concurrent_tasks` | Admission control against measured peak memory. |
| `runtime.extra_args` | Passed to `llama-server` — for example `--n-cpu-moe 999` to keep MoE expert tensors on the CPU, which is what makes an 8 GB card viable for a 30B-class sparse model. |

Raising `max_packet_tokens` is the usual first instinct when retrieval misses
something. It is usually the wrong fix: the miss is more often a retrieval
problem than a budget problem, and a bigger packet costs prefill time on every
step. Check what the retrieval actually returned first:

```console
$ le graph search "the symbol you expected" --json
```
