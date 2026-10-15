# Security and data handling

## What easy-sso stores

Temporary role credentials are published to explicitly managed aliases in the configured AWS shared credentials file. App state contains ownership fingerprints, account/profile definitions, connection bookmarks, settings, and session metadata. It does not store SSO tokens, database IAM tokens, or terminal transcripts. A paused role's credentials section is held only in the running process's memory and is dropped on exit; it is never written to app state or logs.

Unix state directories require current-user ownership and private permissions; credentials require private files. Windows storage uses a protected current-user DACL. Symlink paths are rejected. These controls do not isolate the application from other processes running as the same user.

Bookmark exports contain no authentication material, but they do contain infrastructure metadata such as account IDs, endpoints, roles, and usernames. Review them before sharing. Diagnostic output likewise contains local paths and profile definitions.

## Authentication and sessions

Activation verifies the resolved account and role before publication. Cleanup uses the official AWS CLI's SSO logout operation, affecting the current CLI environment's cached SSO sessions. A failed cleanup blocks credential publication. Local alias removal does not revoke previously copied credentials or established AWS sessions.

Connection processes receive frozen temporary credentials through a controlled environment. They do not fall back to ambient AWS profiles. PostgreSQL IAM tokens are passed through the child environment, with client history disabled and token redaction in rendered output. TLS verifies the actual database hostname through the local tunnel.

Recovery verifies the saved remote session and original identity; it never kills a process merely because its PID appears in saved metadata. If remote termination cannot be confirmed, the UI reports that separately from local process cleanup.

## Reporting an issue

Use the repository's private vulnerability reporting feature when available. If it is unavailable, ask the maintainer for a private reporting channel without including sensitive details in a public issue. Share a minimal synthetic reproduction; do not attach real AWS credentials, SSO codes, raw diagnostics, or company connection exports.
