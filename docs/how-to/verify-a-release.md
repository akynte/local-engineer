# Verify a release

Every release carries checksums, Sigstore signatures, SBOMs and SLSA
provenance. Verifying takes a minute and is worth doing for anything that will
read your source code.

## Binaries

```console
$ VERSION=v0.1.0
$ curl -fsSLO https://github.com/akynte/local-engineer/releases/download/$VERSION/checksums.txt
$ curl -fsSLO https://github.com/akynte/local-engineer/releases/download/$VERSION/le_Linux_x86_64.tar.gz
$ sha256sum --check --ignore-missing checksums.txt
```

Then verify the signature over the checksum file — this is what ties the
artifacts to the workflow that built them:

```console
$ curl -fsSLO https://github.com/akynte/local-engineer/releases/download/$VERSION/checksums.txt.sig
$ curl -fsSLO https://github.com/akynte/local-engineer/releases/download/$VERSION/checksums.txt.pem
$ cosign verify-blob \
    --certificate checksums.txt.pem \
    --signature checksums.txt.sig \
    --certificate-identity-regexp 'https://github.com/akynte/local-engineer/.github/workflows/release.yml@.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    checksums.txt
```

Keyless signing means there is no public key to trust: the certificate binds
the signature to a specific repository and workflow, which is the thing you
actually want to check.

## Container images

```console
$ IMAGE=ghcr.io/akynte/local-engineer:v0.1.0
$ cosign verify $IMAGE \
    --certificate-identity-regexp 'https://github.com/akynte/local-engineer/.github/workflows/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Build provenance:

```console
$ gh attestation verify oci://$IMAGE --repo akynte/local-engineer
```

Then pin by digest, not by tag — a tag can move:

```console
$ docker pull $IMAGE
$ docker inspect --format='{{index .RepoDigests 0}}' $IMAGE
```

## SBOM

```console
$ cosign download sbom $IMAGE > sbom.json
$ syft scan $IMAGE -o spdx-json > sbom.spdx.json
$ grype sbom:sbom.spdx.json
```

The image also carries its own manifest of what went into it:

```console
$ docker run --rm $IMAGE cat /opt/le/image-manifest.txt
```

## What verification does and does not tell you

It tells you the artifact came from this repository's release workflow and has
not been altered since. It does not tell you the code is correct or safe — for
that, read [SECURITY.md](https://github.com/akynte/local-engineer/blob/main/SECURITY.md) for the trust boundary, and
`le doctor` for what is actually in effect on your machine.

## Going public

This repository was developed private. Two workflows skip themselves while that
is true and resume on their own when it changes — CodeQL, because code scanning
on a private repository needs GitHub Advanced Security, and Scorecard, because
it cannot read commit history through an integration token there.

Nothing in the repository needs editing to switch. What does need doing, in the
GitHub settings, because none of it lives in a file:

- **Secret scanning** and **push protection** — both are unavailable on a
  private repository without Advanced Security and must be re-enabled after the
  switch. Until then, nothing is scanning pushes for credentials.
- **Private vulnerability reporting** — unavailable while private, and
  `SECURITY.md` tells people to use it. Re-enable it or that link is dead.
- **Code scanning** — confirm CodeQL results appear in the security tab on the
  first public run.
- **Branch ruleset on `main`** — block force pushes, restrict deletions, require
  the status checks.
- **Tag ruleset on `v*`** — protects the signed release path.

Dependabot alerts and version updates work while private and need nothing.
