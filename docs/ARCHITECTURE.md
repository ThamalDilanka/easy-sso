# easy-sso architecture

The application uses Go and Bubble Tea. The official AWS CLI v2 and Session Manager plugin perform AWS operations; the app owns orchestration, credential publication, local state, and terminal interaction.

| Module | Responsibility |
| --- | --- |
| `cmd/easysso` | CLI flags, prerequisite diagnostics, dependency construction, shutdown |
| `internal/releases` | Shared release target/document contract, asset naming, and semantic version policy |
| `internal/updater` | Canonical GitHub requests, installation provenance, bounded verification, executable leases, atomic replacement |
| `internal/domain` | Shared values and focused interfaces for credentials, sessions, and UI dependencies |
| `internal/profiles` | Parse local AWS config, resolve SSO sources and role chains, verify exact identities |
| `internal/awscli` | Invoke AWS CLI with bounded output and isolated credential environments |
| `internal/credentials` | Activate selected roles, verify identity, clean SSO caches, publish and remove owned aliases |
| `internal/connections` | Validate bookmarks, persist successful connections, preview and merge imports |
| `spec` | Single canonical JSON schema, embedded by bookmark validation and included in packages |
| `internal/store` | Bounded reads, atomic private writes, ownership checks, settings transactions |
| `internal/sessions` | Freeze run identity, correlate exact SSM sessions, verify listeners, stop and recover owned runs |
| `internal/database` | PostgreSQL client setup, CA handling, token redaction, authenticated prompt detection |
| `internal/process`, `internal/ipc` | Owned child processes and authenticated local helper communication |
| `internal/terminal` | Embedded PTYs and platform-specific external window adapters |
| `internal/ui` | State updates, contextual keyboard/mouse dispatch, shared controls and presentation |

## Identity flow

1. Load configured profiles without authenticating or executing credential providers.
2. Review selected roles and credential aliases; authenticate through the CLI device flow.
3. Export temporary credentials and verify the exact STS account, role, and partition.
4. Clear CLI SSO caches, then publish selected aliases with ownership fingerprints.
5. Revalidate the selected profile before connecting; freeze the resolved identity, credentials, destination, and regions for that run.
6. Confirm the exact remote session and local listener or authenticated terminal before saving a new bookmark.

UI account changes never mutate existing runs. Status displays and connection actions use the same profile matching rule. Full config comparisons additionally invalidate pending work when definitions change. The credential module depends on `CredentialCLI`; the session module depends on `SessionCLI` and a save-only bookmark interface.

## UI organization

`model.go` coordinates asynchronous messages and state. `actions.go` handles keyboard routing; `forms.go`, `activation.go`, `reviews.go`, and `connection_actions.go` own their respective workflows. Mouse hit targets route to those same keyboard actions. `chrome.go`, `details.go`, and the view helpers provide shared layouts, colors, tables, and controls.

The local integration tests exercise the real executable, CLI adapter, helper IPC, and PTY input against an injected fake AWS CLI. Focused tests cover failure paths, unsafe imports, ownership, persistence, and recovery. Live cloud and native platform validation are separate from those tests.
