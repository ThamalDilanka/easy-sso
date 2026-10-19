package awscli

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"strings"
	"testing"
)

func TestDatabaseTokenSignsRemoteDestinationWithFrozenRole(t *testing.T) {
	c := fakeClient(t, `
[ "$AWS_ACCESS_KEY_ID" = ASIATESTKEY ] || exit 2
[ "$AWS_SECRET_ACCESS_KEY" = TESTSECRET ] || exit 2
[ "$AWS_SESSION_TOKEN" = TESTTOKEN ] || exit 2
[ -z "$AWS_PROFILE" ] || exit 2
[ "$1 $2 $3 $4 $5 $6 $7 $8 $9" = "rds generate-db-auth-token --hostname db.example.rds.amazonaws.com --port 5432 --region us-east-1 --username" ] || exit 2
shift 9
[ "$1" = iam_reader ] || exit 2
printf '%s\n' 'signed-token'
`)
	t.Setenv("AWS_PROFILE", "wrong-role")
	db := domain.Connection{Kind: "db", Name: "db", Partition: "aws", AccountID: "123456789012", Region: "us-east-1", Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-west-2", AuthMode: "postgres-iam", Database: "exampledb", DBUser: "iam_reader"}
	token, err := c.DatabaseToken(context.Background(), snapshot(), db)
	if err != nil || token != "signed-token" {
		t.Fatal("remote signing arguments or credentials incorrect", err)
	}
	bad := fakeClient(t, "printf 'SECRET' >&2; exit 1\n")
	_, err = bad.DatabaseToken(context.Background(), snapshot(), db)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("raw token error exposed")
	}
}

// DatabaseToken is the PostgreSQL signer; DocumentDB IAM login signs inside
// the mongo client, so a docdb-iam bookmark must never reach it.
func TestDatabaseTokenRefusesDocDBIAM(t *testing.T) {
	// The fake signs successfully, so only the IAMDatabase guard can refuse.
	c := fakeClient(t, "printf '%s\\n' 'signed-token'\n")
	db := domain.Connection{Kind: "db", Name: "docs", Partition: "aws", AccountID: "123456789012", Region: "us-east-1", Endpoint: "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com", DBPort: 27017, LocalPort: 37017, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-west-2", AuthMode: "docdb-iam", Database: "app_db"}
	if token, err := c.DatabaseToken(context.Background(), snapshot(), db); err == nil || token != "" {
		t.Fatalf("DatabaseToken = %q, %v", token, err)
	}
}
