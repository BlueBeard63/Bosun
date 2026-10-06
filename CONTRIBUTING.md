# Contributing to Bosun

## Repository layout

The repository holds several Go modules:

| Module | Path | Tag format |
|---|---|---|
| `github.com/bluebeard63/bosun` (core, `mw`, `openapi`, most `modules/*`) | `.` | `vX.Y.Z` |
| `github.com/bluebeard63/bosun/cmd/bosun` (CLI) | `cmd/bosun` | `cmd/bosun/vX.Y.Z` |
| Broker, storage and tracing drivers | `modules/eventamqpmod`, `eventnatsmod`, `eventredismod`, `stores3mod`, `traceotelmod` | `modules/<name>/vX.Y.Z` |

The nested modules require the root module by its released version. That is why releases follow a fixed order (see [Releasing](#releasing)).

## Branching

- `master` holds released code.
- Each release is developed on an integration branch named after the version, for example `v0.7.0`.
- Each issue gets a branch cut from the integration branch, named `<type>/<issue>-<slug>`, for example `feat/8-app-middleware` or `fix/42-path-metadata`.
- Open feature PRs against the integration branch with `Refs #N` in the body, and squash-merge them.
- When the release is ready, open one PR from the integration branch into `master` with `Closes #N` for every issue, and merge it with a merge commit.

## Running the checks locally

CI runs these checks; you can run the same ones yourself before opening a PR:

```sh
gofmt -l $(git ls-files '*.go' | grep -v /testdata/)    # must print nothing
go vet ./... && go test -race ./...                      # root module
(cd cmd/bosun && go generate .)                          # after editing docs/
```

To test the nested modules against your local changes rather than the published root, use a Go workspace. `go.work` is gitignored.

```sh
go work init . ./cmd/bosun ./modules/eventamqpmod ./modules/eventnatsmod \
  ./modules/eventredismod ./modules/stores3mod ./modules/traceotelmod
(cd modules/eventnatsmod && go test ./...)
```

The broker and storage tests are skipped unless a server is configured:

```sh
docker run -d -p 5672:5672 rabbitmq:3
docker run -d -p 4222:4222 nats:2
docker run -d -p 6379:6379 redis:7
docker run -d -p 9000:9000 -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin minio/minio server /data

export BOSUN_AMQP_URL=amqp://guest:guest@localhost:5672/
export BOSUN_NATS_URL=nats://localhost:4222
export BOSUN_REDIS_ADDR=localhost:6379
export BOSUN_S3_ENDPOINT=localhost:9000 BOSUN_S3_KEY=minioadmin BOSUN_S3_SECRET=minioadmin BOSUN_S3_BUCKET=bosun-dev
```

## Continuous integration

`.github/workflows/ci.yml` runs on every pull request into `master` or a `v*` integration branch, on pushes to `master`, and on manual dispatch. It has four jobs:

| Job | What it checks |
|---|---|
| **Root module** | `go mod verify`, `go vet` and `go test -race` on Go 1.22 (the minimum in `go.mod`) and on the latest stable Go. |
| **Formatting, tidiness and generated docs** | `gofmt`, `go mod tidy` for every module, and that `cmd/bosun/internal/docsite/content` matches `docs/` after `go generate`. |
| **Nested modules** | `go vet` and `go test -race` for `cmd/bosun` and each driver module, linked to the PR's root module through a Go workspace, so changes that break a nested module fail before release. |
| **Integration** | The RabbitMQ, NATS, Redis and S3 (MinIO) tests against real services. |

## Releasing

`scripts/release.sh` releases every module at one version. It **does nothing unless you pass `--execute`**. Without it, it runs every check and prints the commands it would run.

### Before you release

1. Merge the integration branch into `master` and check that CI passed on the merge.
2. Make sure `CHANGELOG.md` has a section for the version, for example `## [0.7.0] - 2026-10-06`. The script warns if it's missing.
3. Check out an up-to-date, clean `master`.

### Run it

```sh
scripts/release.sh v0.7.0              # dry run: checks + plan
scripts/release.sh v0.7.0 --execute    # release
```

The script:

1. **Checks** the branch, a clean tree, that `HEAD` matches `origin/master`, and that none of the tags exist locally or on the remote.
2. **Tests** the root module, and each nested module against this checkout.
3. **Tags and pushes the root module** as `vX.Y.Z`.
4. **Bumps the nested modules.** For each one it runs `go get github.com/bluebeard63/bosun@vX.Y.Z` and `go mod tidy`, then tests it against the real tag. It also updates `fallbackBosunVersion` in the CLI scaffold, commits everything as `chore: bump nested modules to bosun vX.Y.Z`, and pushes the commit.
5. **Tags the nested modules** on the bump commit (`cmd/bosun/vX.Y.Z` and `modules/<name>/vX.Y.Z`) and pushes the tags.

The new root version is fetched directly from GitHub (`GOPRIVATE=github.com/bluebeard63/*` unless you've set `GOPRIVATE` yourself), so the module proxy's delay never applies.

### If a step fails

Fix the cause and resume from the step that failed, rather than starting over:

```sh
scripts/release.sh v0.7.0 --execute --from-step bump         # after the root tag was pushed
scripts/release.sh v0.7.0 --execute --from-step tag-modules  # after the bump commit was pushed
```

If `master` is protected against direct pushes, the bump step's push will fail. Push the bump commit through a PR, pull it, then resume with `--from-step tag-modules`.

### After the release

```sh
gh release create v0.7.0 --title v0.7.0 --notes "$(scripts/release.sh v0.7.0 --print-notes)"
GOPRIVATE=github.com/bluebeard63/* go install github.com/bluebeard63/bosun/cmd/bosun@v0.7.0
bosun --version   # v0.7.0
```
