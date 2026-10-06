# verdantflare-station-artifact

Go implementation of Station Artifact. Product and interface specifications are
maintained in the verdantflare-design workspace under docs/design/station/ and
docs/design/contracts/.

Implemented: immutable local content, PostgreSQL migrations, upload/commit
transactions, retention, protected HTTP transport, a fail-closed business
authorization client, and the `station-artifact migrate|serve` entry point.
Artifact read/write tools register through etcd and integrate with the Studio
Gateway, Project/World authorization and controlled content transfer. These paths
have local service validation; S3 storage, native producer adapters and production
deployment remain pending.

Serving defaults to the S3 backend. Set `ARTIFACT_STORAGE_BACKEND=local` and
`ARTIFACT_CONTENT_ROOT` only for local development, tests, or an explicitly
offline deployment. S3 deployments require the `ARTIFACT_S3_*` settings described
in the central content-version design; credentials are injected as secrets.

Build with `go build -o <output-path> ./cmd/station-artifact`. Configuration and
internal HTTP contracts are maintained in the central design workspace:
`docs/design/station/details/05-station-artifact-design.content-versions.md` and
`docs/design/station/details/05-station-artifact-design.mcp-transfer.md`.
Serving requires explicit migrations and separate service/authority credentials;
there is no default allow authorization mode.

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
