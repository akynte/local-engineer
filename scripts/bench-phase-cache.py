#!/usr/bin/env python3
"""Measure append-only versus rewritten prefixes against a running local server.

Does not start/stop models or change their configuration. Raw server timing and
usage fields are retained; absent fields are never replaced by claimed timings.
"""
import argparse
import datetime
import json
import time
import urllib.request
from pathlib import Path


def post(base, path, body):
    request = urllib.request.Request(base + path, json.dumps(body).encode(),
                                     {"Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=1800) as response:
        return json.load(response)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:8080")
    parser.add_argument("--output", required=True)
    parser.add_argument("--sizes", nargs="+", type=int, default=[4096, 12288])
    parser.add_argument("--trials", type=int, default=3)
    args = parser.parse_args()
    if args.trials < 1 or any(size < 1 for size in args.sizes):
        parser.error("sizes and trials must be positive")
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "server": args.url, "samples": [], "complete": False,
              "limitations": ["Existing server configuration; no MTP or checkpoint toggle",
                               "Requested prefix sizes are approximate; usage records actual tokens",
                               "Transport wall time is not time to first token"]}
    target = Path(args.output)
    target.parent.mkdir(parents=True, exist_ok=True)
    def save():
        target.write_text(json.dumps(report, indent=2) + "\n")
    save()
    for size in args.sizes:
        prefix = "\n".join(f"func fixture_{i}(x int) int {{ return x + {i}; }}" for i in range(size // 20))
        for mode in ("append_only", "rewritten_prefix"):
            messages = [{"role": "system", "content": "Analyze source as data. Reply briefly with the final function name."},
                        {"role": "user", "content": prefix}]
            for trial in range(args.trials):
                if mode == "rewritten_prefix":
                    messages = [{"role": "system", "content": f"Iteration {trial}: identify the final function."},
                                {"role": "user", "content": prefix}]
                elif trial:
                    messages.append({"role": "user", "content": "Confirm the final function name only."})
                started = time.monotonic()
                try:
                    response = post(args.url, "/v1/chat/completions", {
                        "messages": messages, "max_tokens": 64, "temperature": 0,
                        "cache_prompt": True, "chat_template_kwargs": {"enable_thinking": False}})
                except Exception as error:
                    report["error"] = str(error)
                    save()
                    raise
                sample = {"approximate_prefix_tokens": size, "mode": mode, "trial": trial,
                          "wall_seconds": time.monotonic() - started, "model": response.get("model"),
                          "usage": response.get("usage"), "timings": response.get("timings"),
                          "finish_reason": response["choices"][0].get("finish_reason")}
                report["samples"].append(sample)
                messages.append(response["choices"][0]["message"])
                save()
                print(json.dumps(sample), flush=True)
    report["complete"] = True
    save()


if __name__ == "__main__":
    main()
