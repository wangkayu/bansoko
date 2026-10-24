# scripts/

Everything here is developer tooling. None of it ships in the image, and no
operator is expected to run anything in this directory — deployment scripts for
an installed instance live in `deployment/scripts/`.

The directory is large because most of the work is verification: well over a
third of the files exist to prove, rehearse, or observe something rather than to
build or publish it. That is deliberate. A check nobody has ever seen fail is
not evidence.

## Layers

The four directories answer "who runs this, and when":

| Directory | Invoked by | What lives there |
|---|---|---|
| `ci/` | `.github/workflows/ci.yml`, directly or through `ci-contract-boundary.sh` / `ci-unit-static.sh` | Every gate, every CI lane entry point, and the shared helpers those lanes call |
| `generators/` | The persistence freshness gate, the contract/observability checks, or the Docker build | Programs that emit a checked-in artifact from a single source |
| `verify/` | A person, on the disposable test VM | Lanes that need a real PostgreSQL, qBittorrent, browser or filesystem |
| `maintainer/` | A person, on their own machine, ad hoc | Build/host conveniences that no gate depends on |

`fact-copy/` and `observability-catalog/` are Go program directories
(`go run ./scripts/fact-copy`), not scripts, so they stay at this level.

## `ci/` — what CI runs

`ci-contract-boundary.sh` and `ci-unit-static.sh` are the two umbrella lanes;
the rest are either those lanes' stages or entry points in their own right
(`ci-frontend-quality.sh`, `ci-host-postgres.sh`, `ci-fixture-browser.sh`,
`ci-persistence-freshness.sh`, `ci-dockerhub-publish.sh`, `test-integration.sh`,
`check-vulns.sh`). `ci-provider-optin.sh` is the opposite: an explicit opt-in
lane CI deliberately never runs, because it reaches the public provider network.

A script belongs in `ci/` when a workflow step names it or an umbrella lane
sources it — including the helpers that only ever run inside a lane
(`require-disposable-db.sh`, `go-test-no-skips.sh`, `pg-shard-packages.sh`,
`real-effect-harness.sh`, `embedded-update-docker-fixture.sh`).

**Checks and gates.** `check-*` and `census*`. These read the tree and refuse a
retired shape: the OpenAPI/generated boundary, import direction, write-entry
inventory, the production compose surface, legacy names, workitem write access.
A few have a mutation test that copies the subject, breaks it and asserts the
refusal (`check-ci-shards-test.sh`, `check-production-compose-database-test.sh`,
`check-production-compose-fixture-test.sh`, `check-release-compose-test.py`,
`ci-release-dag-mutation-test.rb`). The rest are exercised by running them over
the real tree in `ci-contract-boundary.sh`.

**Release tooling.** `build-release.sh`, `check-release.sh`, `ci-release-*`,
`ci-test-release.py`. The DAG shape is pinned by `ci-release-dag-test.rb`, so
adding or reordering a release step is a deliberate change to that file, not an
incidental edit.

## `generators/` — artifacts derived from a source

The sqlc persistence layer (`generate-*-persistence.sh`, all driven by
`ci-persistence-freshness.sh` through a glob), the third-party and base-image
notices, the PG 17 component list, and the acquisition DTO golden vectors. The
persistence generators and `generate-dto-golden-vectors.py` take `--check`;
`generate-contracts.sh`, `generate-observability-catalog.sh` and
`generate-fact-copy.sh` live in `ci/` because they sit next to their `check-*`
counterparts — each is verified by regenerating into a scratch directory and
diffing the committed output, so a source edit without the regeneration fails
CI. `generate-third-party-notices.sh`, `generate-base-image-notices.sh` and
`generate-pg17-components.sh` are different: they run inside the Docker build
from the image's own contents, and the publish smoke greps the result.

## `verify/` — authoritative only on the test VM

`real-effect-*`, `filemaintenance-e2e.sh`, `backup-restore-proof.sh`,
`release-restore-lane.sh`, `soak-scale.sh`, `verify-worker-recovery-publication.sh`,
`embedded-*`, `vm-session.sh`. These need a real PostgreSQL, qBittorrent, a
browser or a filesystem and are authoritative only on the disposable test VM.
`.trellis/spec/shared/test-environment.md` is the contract for that machine;
several of these scripts are named in it.

## `maintainer/` — no gate depends on these

`build.sh`, `reproducible-build.sh`, `release-smoke.sh`,
`tmdb-connection-smoke.sh`, `make-repository-public.py`,
`ci-provider-optin.sh`, `transform-embedded-compose-test.py`. Ad-hoc
conveniences. If one of these starts running in a workflow, it moves to `ci/` —
except `ci-provider-optin.sh`, which is the documented way to reach the public
provider network and must never become a gate.

`make-repository-public.py` is the one script that acts on the repository's
visibility rather than on its contents. It is the only supported path to
publishing the source, and it refuses to do that in isolation from protecting
main.

## Rules

- A new `check-*` should come with a `*-test` that proves at least one refusal,
  the way the compose and CI-shard checks do. A check whose only observed
  outcome is `passed` is a comment.
- A generator needs a `--check` mode or a `check-*` counterpart that diffs its
  output, and a CI caller, or its artifact goes stale silently.
- A script derives the repository root from its own location, one level deeper
  than this file: `root="$(cd "$(dirname "$0")/../.." && pwd)"`.
- Nothing here is invokable by an installed instance. Operator-facing commands
  belong in `deployment/scripts/` and `README.md`.
