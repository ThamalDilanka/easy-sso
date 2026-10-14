// Package spec embeds the public bookmark contract used by validation and distribution.
package spec

import _ "embed"

// ConnectionsSchema is the canonical versioned bookmark JSON schema.
//
//go:embed connections.schema.json
var ConnectionsSchema []byte
