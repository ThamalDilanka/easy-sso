package connections

import (
	"errors"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

// DocumentDBEndpoint reports whether host is an Amazon DocumentDB endpoint.
// It drives display, defaults and hints only, never a security decision.
func DocumentDBEndpoint(host string) bool {
	h := strings.ToLower(host)
	for _, suffix := range []string{".docdb.amazonaws.com", ".docdb.amazonaws.com.cn", ".docdb-elastic.amazonaws.com"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// DocumentDBElastic is a best-effort guess: elastic clusters can also use a
// .docdb.amazonaws.com host, which this reports as false.
func DocumentDBElastic(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ".docdb-elastic.amazonaws.com")
}

// DocumentDBMode refuses login modes that cannot work on DocumentDB. It is
// kept out of Validate so a bookmark already on disk never blocks startup.
func DocumentDBMode(c domain.Connection) error {
	if c.IAMDatabase() && DocumentDBEndpoint(c.Endpoint) {
		return errors.New("PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel")
	}
	if c.DocDBIAM() {
		if !DocumentDBEndpoint(c.Endpoint) {
			return errors.New("DocumentDB IAM login needs a DocumentDB endpoint; set Auth to tunnel")
		}
		if DocumentDBElastic(c.Endpoint) {
			return errors.New("Elastic clusters do not support IAM login; set Auth to tunnel")
		}
	}
	return nil
}
