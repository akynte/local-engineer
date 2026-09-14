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
that, read [SECURITY.md](../../SECURITY.md) for the trust boundary, and
`le doctor` for what is actually in effect on your machine.
