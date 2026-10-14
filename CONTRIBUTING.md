# Contributing to easy-sso

Use Go 1.27.2 or newer, Python 3, and Make. Start with `make build`, `make test`, and `make check`. Tests inject a fake AWS CLI and use private temporary homes; AWS credentials are not required. Linux integration tests drive the real executable through a PTY. Install `psql` to run the local PostgreSQL TLS fixture; that test otherwise skips.

`make crossbuild` checks compilation for the supported build targets. `make package VERSION=0.1.0 COMMIT=<commit>` creates versioned archives and exact checksums under ignored `bin/release/`, plus candidate channel manifests under `bin/channels/`. Packaging uses only repository documents and built binaries. Cross-compilation does not prove native support.

## Code changes

Keep behavior behind small interfaces and put policy in the module that owns it. The UI renders results and dispatches actions; it must not implement AWS credential resolution or process ownership. Reuse the existing identity, persistence, details-table, and control-rendering helpers. Keyboard actions and mouse controls must invoke the same handlers.

Keep account, role, partition, source profile, resource region, and relay region distinct. A display label is never proof of identity. Established runs keep their frozen credentials and destination. Recovered metadata is never proof that a local process is owned.

Use `gofmt`. Add behavioral tests for changed identity, lifecycle, persistence, or interaction rules. Keep tests that protect failure and cleanup behavior; avoid duplicate fixtures or assertions that only repeat implementation structure. See [architecture](docs/ARCHITECTURE.md).

## Public examples

Use `testdata/aws-config.ini` for profile examples. It deliberately uses reserved example domains and synthetic account IDs. Do not replace it with your AWS config. Use `example.com`, `example.invalid`, or clearly marked example endpoints and the synthetic account IDs accepted by `tools/check-public.py`.

Never commit credentials, SSO cache files, diagnostic dumps, personal paths, company account topology, or real terminal recordings. `make check` scans tracked and new nonignored files for common accidental disclosures. This is a focused guard, not a substitute for reviewing the diff and scanning Git history before publication.

## Pull requests

Explain the user-visible behavior, why it changed, and how you verified it. Note any platform or live AWS checks still needed. Use your Git hosting provider's private email setting if you do not want a personal address in public commit metadata.

## Release changes

Keep `internal/releases/contract.json` authoritative for targets and bundled files. Packaging and the updater must agree on exact contents, names and embedded version metadata. Exercise actual packages with `EASY_SSO_RELEASE_DIR="$PWD/bin/release" go test ./internal/updater -run TestPackagedRelease -count=1` after packaging. Add failure-path tests for changed updater policy, bounded HTTP behavior, verification, and replacement. The draft workflow must remain separate from read-only CI; see [Distribution](docs/DISTRIBUTION.md) for release operations and remaining publication gates.
