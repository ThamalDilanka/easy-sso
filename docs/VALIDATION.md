# Release validation

Run from a clean checkout:

```sh
make test
make check
make package
EASY_SSO_RELEASE_DIR="$PWD/bin/release" go test ./internal/updater -run TestPackagedRelease -count=1
python3 tools/check-public.py --history
```

The suite covers profile resolution, cleanup-before-publication, credential ownership, bookmark validation/import, subprocess isolation, exact session correlation, listener ownership, recovery, UI keyboard/mouse behavior, and embedded PTYs. AWS-dependent scenarios use an injected fake CLI. The PostgreSQL protocol fixture uses a local TLS server and `psql`; it skips when `psql` is absent.

## Live and native checks

Before advertising production or platform support, record the OS, architecture, terminal, AWS CLI/plugin versions, and results for these checks using a disposable environment:

- Direct SSO activation and a chained role: verify destination identity, expiration, selected alias publication, cache cleanup, cancellation, and failure handling.
- EC2 shells: actual SSM input, resize, paste, interactive programs, multiple tabs, external windows, targeted stop, and remote termination status.
- Database tunnels: relay access, independent DB/relay regions, loopback-only listeners, local port conflicts, exact ownership, and cleanup after network loss.
- PostgreSQL IAM: correct database/user permissions, token generation, TLS hostname verification, CA failure, rejected authentication, and reuse of only owned tunnels.
- Recovery: application restart, expired original credentials, helper loss, PID reuse, unavailable metadata permissions, and explicit recheck/stop.
- Native platforms: Windows ACLs and process jobs; WSL distribution/user and browser interop; macOS PTYs and Terminal.app; Linux desktop window launch.
- Public distribution: archives contain only the intended binary, MIT license, schema, and public documentation; checksums match; no development captures or private fixtures are included.

Automated tests and cross-compilation do not establish live AWS authorization, native terminal behavior, or performance guarantees. The GitHub release workflow creates a draft for inspection before deliberate publication. Release signing/attestations and hosted package-manager repositories remain unconfigured.

## Distribution validation

Updater unit tests cover semantic ordering and development/prerelease policy, exact platform selection, paginated release selection, no releases, offline/unavailable/rate-limited responses, token-stripping redirects, payload bounds, manifest/checksum rejection, unsafe archives, manager guidance, and failed replacement preserving the old executable. Unix tests cover shared app/helper leases, exclusive updater leases, staging cleanup, preserved permissions, and stale inode rejection. The optional packaged-release gate consumes all five real archives, verifies their embedded version/platform metadata, and runs download/verification/replacement against a temporary local installation with fake HTTP.

Linux behavior is locally testable. macOS locking/rename and Windows manual installation, Scoop metadata, executable locks, and uninstall behavior need native validation; cross-compilation alone does not qualify them. Run Homebrew formula audit/install tests and Scoop install/upgrade tests against published accessible assets before hosting those channels. Record a draft workflow run and inspect its six assets before publication. A local dry run does not create a version tag, publish a release, or create a channel repository.

The distribution dry run on Linux amd64 covers the race-enabled Go suite, Python packaging/draft tests, static and public-history checks, all five cross-builds, and the real-package updater gate (including temporary Linux replacement, version reporting, and startup). Archive reproducibility, checksum-to-channel-manifest consistency, executable modes, and draft-only upload selection are covered by offline packaging tests. The dry-run version `0.1.0-rc.1` is an unpublished fixture, not a release version.

GitHub CI runs the same checks on every push to `main`, including all five packages and the updater gate. Version-tag release runs repeat them and retain their results in GitHub Actions.
