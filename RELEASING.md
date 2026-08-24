# Releasing

A release is a tag. Pushing `vX.Y.Z` to `main` triggers
`.github/workflows/release.yaml`, which does everything else.

## Before tagging

1. **CI is green on `main`.** The release workflow runs its own `validate` job,
   but it is faster to find a failure before six parallel jobs start.

2. **`CHANGELOG.md` has a section for this version.** Move the contents of
   `[Unreleased]` under a new `## [X.Y.Z] - YYYY-MM-DD` heading, leave
   `[Unreleased]` empty, and update the two link definitions at the bottom.

   The GitHub Release body is auto-generated from commit messages
   (`generate_release_notes: true`), so the CHANGELOG is the only place the
   release is described in the project's own words. It is worth writing.

3. **Breaking changes are called out.** Anything that makes an existing
   deployment fail on upgrade belongs under a `### Breaking` heading with the
   migration step spelled out. The Context0 to Kora rename is the worked
   example: environment variables, module path, and Postgres role and database
   all moved, and `scripts/migrate_rename.sh` exists because a find-and-replace
   could not do it.

## Rehearsing without releasing

The workflow can be dispatched manually, which runs everything except the
publishing steps:

```bash
gh workflow run release.yaml --ref main -f version=0.0.0-dryrun
```

Images build for both platforms but are not pushed, the chart is packaged but
not pushed, the SDK is built, and the GitHub Release job is skipped entirely.

This exists because the release path was otherwise unreachable except by
tagging. Four of this repo's actions appear only in this workflow, so a major
bump to any of them was unverifiable until a release used it -- and the only
way to retry a failed release is another tag.

Run it after changing anything in `release.yaml`.

## Tagging

```bash
git switch main && git pull
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

Annotated tags (`-a`) rather than lightweight ones: the workflow reads
`GITHUB_REF_NAME`, which works either way, but an annotated tag records who cut
the release and when.

## What the workflow publishes

Six jobs, fanning out from `validate`:

| Job | Publishes |
|---|---|
| `validate` | nothing; runs tests and a build check first |
| `docker` | three images to `ghcr.io/<owner>/kora-{server,postgres,web}` |
| `binaries` | `linux/darwin` × `amd64/arm64` binaries as release assets |
| `helm` | the chart to `oci://ghcr.io/<owner>/charts` |
| `python-sdk` | an sdist and wheel **as a build artifact only** |
| `release` | the GitHub Release, once all of the above succeed |

Two things to know about that table:

**The Python SDK does not reach PyPI.** The publish step is commented out
pending a PyPI token. Until it is uncommented, `pip install kora` will not
find a release; the wheel is downloadable from the workflow run.

**Binaries are version-stamped at link time** with
`-X internal/config.DefaultVersion=$VERSION`. The linker's `-X` flag fails
silently against a symbol that does not exist, and this workflow spent its
whole life pointing at one that had never been declared, so every binary it
built reported `0.1.0-dev`. `TestDefaultVersionIsLinkerStampable` now guards
the two properties `-X` needs. If you change how the version is exposed, run
that test.

## Verifying a release

The workflow succeeding is not the same as the artifacts being usable.

```bash
# The image runs and reports the version you tagged, not 0.1.0-dev.
# Note the image tag has no leading "v": metadata-action's {{version}}
# strips it, so v0.2.0 publishes as :0.2.0.
docker run --rm ghcr.io/narayanasabari/kora-server:0.2.0 2>&1 | head -1

# The chart resolves and renders.
helm template kora oci://ghcr.io/narayanasabari/charts/kora --version 0.2.0 \
  --set postgres.password=x --set auth.apiKeys=ctx0_x >/dev/null && echo ok
```

A binary reporting `0.1.0-dev` means stamping regressed.

## Versioning

[Semantic Versioning](https://semver.org/spec/v2.0.0.html). While the project
is pre-1.0, breaking changes go in a minor bump and must be documented under
`### Breaking`.

The AGE graph name is the one identifier that does **not** follow the version:
it stays `context0` across the rename because it is also the Postgres schema
holding every deployment's data. See `GraphName` in `internal/graph/age.go`.
