# verdantflare-station-artifact

Go implementation of Station Artifact. Product and interface specifications are
maintained in the verdantflare-design workspace under docs/design/station/ and
docs/design/contracts/.

Implemented: S3 standard controlled proxy (`Put`, `Get`, `Stat`, direct `Delete`),
immutable local/S3 content verification (`Size + SHA-256`), PostgreSQL version/upload
mappings, `write_id` idempotency, protected HTTP transport, and the
`station-artifact migrate|serve` entry point. Artifact read/write tools register
through etcd and integrate unilaterally with Studio Gateway and the six business
domains (`Project`, `World`, `Video`, `Image`, `Speech`, `Music`) without reverse
business callbacks or unreviewed soft-delete lifecycle locks.

Serving defaults to the S3 backend. Set `ARTIFACT_STORAGE_BACKEND=local` and
`ARTIFACT_CONTENT_ROOT` only for local development, tests, or an explicitly
offline deployment. S3 deployments require the `ARTIFACT_S3_*` settings described
in the central content-version design; credentials are injected as secrets. The
`serve` command checks that the configured bucket exists before opening its HTTP
listener; bucket creation is an explicit deployment step.

Build the production Linux container with the repository `Dockerfile`; it
produces a static `linux/amd64` binary and runs as the distroless non-root user.
For a local binary, use `go build -o <output-path> ./cmd/station-artifact`.
The release-only GitHub Actions workflow publishes `artifact-v0.2.6` to the
configured `wod/verdantflare-station` registry repository. It refuses existing
tags and reuses GHA build cache; run the local checks below before promotion.
Configuration and
internal HTTP contracts are maintained in the central design workspace:
`docs/design/station/05-station-artifact-design.md`,
`docs/design/station/details/05-station-artifact-design.content-versions.md` and
`docs/design/station/details/05-station-artifact-design.mcp-transfer.md`.
Serving requires explicit migrations and `ARTIFACT_SERVICE_TOKEN` plus tenant
headers (`X-Organization-Id`, `X-User-Id`, `X-Request-Id`).

Local checks (Go 1.26 or later):

```sh
go test ./...
go vet ./...
```

Tests use temporary directories and synthetic text/binary payloads. No production
credentials or media are required. Production Linux storage must support hard
links and directory fsync; Windows tests cover file flush and backend close/reopen,
not power-loss durability.

PostgreSQL integration tests require ARTIFACT_TEST_DATABASE_URL pointing to a
local disposable PostgreSQL instance with database-create permission. Tests use
unique database names and remove them afterwards; do not point this at production.
Without that variable, database, HTTP and lifecycle integration tests are explicitly
skipped. ARTIFACT_CONTRACT_EVIDENCE_DIR optionally saves synthetic HTTP test
payloads for validation against the central v2 schemas; it is test-only.
