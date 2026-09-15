# Fetch a dependency

A task cannot reach the network. Verification runs with `GOPROXY=off`, and a
task's Landlock ruleset grants the inference endpoint and assigned test ports
and nothing else. So when a change genuinely needs a new dependency, you fetch
it — between tasks, with the `go.mod` diff in front of you — through the
provisioning lane of design v3 §6.1.

This page is how.

## Turn the proxy on

It is off by default, because a machine whose premise is that it has no egress
should not acquire some from a shipped config file.

```yaml
# /data/config/le.yaml
egress:
  enabled: true
  deps_port: 7780
  docs_port: 7781
  allowlist:
    rules:
      - host: proxy.golang.org
        lanes: [deps]
        why: the Go module proxy
      - host: sum.golang.org
        lanes: [deps]
        why: the checksum database
```

Restart the supervisor. `le doctor` will now say what is in effect:

```
[ok  ] egress proxy (§6.1)   enabled: deps on 127.0.0.1:7780, docs on 127.0.0.1:7781, 2 allowlist rule(s)
```

`offline: true` and `egress.enabled: true` together are an error, not a
precedence rule. Offline mode has no route out, so the lanes cannot exist, and
silently winning either way would leave you believing something untrue about
the machine.

## Fetch

```console
$ cd /work/my-project
$ le deps sync
```

That runs `go mod download all` inside the lane. The lane is confined: it may
write its working directory and the provisioning cache, and it may dial the
proxy and nothing else — not the inference port, not a test port.

For a package manager the lane does not know about:

```console
$ le deps run -- npm ci --omit=dev
```

Still confined, still allowlisted. The lane is the boundary, not the command.

## When it is refused

```
egress refused: registry.example.com is not in the deps lane's allowlist;
add it to egress.allowlist in le.yaml with a reason
```

The proxy did its job. Decide whether you wanted that host, and if you did, add
it:

```yaml
      - host: registry.example.com
        lanes: [deps]
        why: the internal module mirror for this team
```

The `why` is required. A rule that cannot say why it exists is one nobody can
judge in six months, which is the same requirement policy rules carry.

Every decision is logged by the supervisor — `egress allowed` and
`egress refused`, with the host, the lane and the reason. Check there first when
a fetch fails in a way that looks like DNS.

## What the allowlist will and will not accept

| Pattern | Meaning |
|---|---|
| `proxy.golang.org` | Exactly that host. Not subdomains, not the parent. |
| `*.golang.org` | Subdomains. **Not** the apex `golang.org`. |
| `*` | Refused. An allowlist that allows everything should be an absent proxy. |
| `https://host/path` | Refused. A host is expected, not a URL. |
| `foo.*.com` | Refused. A wildcard must be a leading label. |

Only ports 80 and 443 are reachable. That is not configurable: widening it
turns a host allowlist into a general tunnel, and that should not be one line
of YAML away.

`le doctor` warns when any rule is a wildcard — not because wildcards are
wrong, but because they are the entries most likely to be broader than you
meant.

## Why this is not a task flag

§6.1 says the proxy is "never for a task sandbox". A flag is a thing that gets
set, so there isn't one: the only function that builds a sandbox spec
containing the proxy port takes a `proxy.Lane`, and the task runner has no way
to construct one. The separation is in the type, not in a convention.

Two different mechanisms keep it that way, and neither is asked to do the
other's job:

- **The sandbox bounds who can ask.** A task's ruleset never includes the proxy
  port.
- **The allowlist bounds where the lane can reach.** An allowlist is not an
  access control.

## The documentation lane

`le docs` is the same machinery pointed at documentation hosts, in a separate
lane with a separate listener and a separate slice of the allowlist — so a
documentation host cannot be used to fetch code.

```console
$ le docs fetch https://pkg.go.dev/net/http
```

## See also

- [The isolation model](../explanation/isolation-model.md) — where this sits in
  the three layers.
- [Run fully offline](run-offline.md) — the opposite configuration.
- [Configuration reference](../reference/configuration.md#leyaml) — every field.
