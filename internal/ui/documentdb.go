package ui

import (
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

const documentDBLogin = "Tunnel only · your DocumentDB user stays in your client"

func isDocumentDB(c domain.Connection) bool {
	return c.Kind == "db" && connections.DocumentDBEndpoint(c.Endpoint)
}

func engineLabel(c domain.Connection) string {
	if isDocumentDB(c) {
		return "DocumentDB"
	}
	return "PostgreSQL / other"
}
