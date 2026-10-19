package connections

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const docdbHost = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com"

func docdb() domain.Connection {
	c := db()
	c.ID, c.Name, c.Endpoint, c.DBPort, c.LocalPort = "docdb-1", "documents", docdbHost, 27017, 37017
	return c
}

func docdbIAM() domain.Connection {
	c := docdb()
	c.AuthMode, c.Database, c.DBUser = "postgres-iam", "exampledb", "reader"
	return c
}

func TestDocumentDBEndpoint(t *testing.T) {
	for host, want := range map[string]bool{
		docdbHost: true,
		"example-cluster.cluster-ro-c0example.us-east-1.docdb.amazonaws.com":  true,
		"example-1.c0example.us-east-1.docdb.amazonaws.com":                   true,
		"example.us-east-1.docdb-elastic.amazonaws.com":                       true,
		"EXAMPLE-CLUSTER.CLUSTER-C0EXAMPLE.US-EAST-1.DOCDB.AMAZONAWS.COM":     true,
		"example-cluster.cluster-c0example.cn-north-1.docdb.amazonaws.com.cn": true,
		"db.example.rds.amazonaws.com":                                        false,
		"docdb.amazonaws.com.example.com":                                     false,
		"":                                                                    false,
	} {
		if got := DocumentDBEndpoint(host); got != want {
			t.Errorf("DocumentDBEndpoint(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDocumentDBElastic(t *testing.T) {
	for host, want := range map[string]bool{
		"example.us-east-1.docdb-elastic.amazonaws.com": true,
		"EXAMPLE.US-EAST-1.DOCDB-ELASTIC.AMAZONAWS.COM": true,
		docdbHost: false,
		"example-1.c0example.us-east-1.docdb.amazonaws.com": false,
		// Documented false negative: elastic clusters can also use the
		// sharded-cluster form under .docdb.amazonaws.com (display only).
		"example.sharded-cluster-c0example.us-east-1.docdb.amazonaws.com": false,
	} {
		if got := DocumentDBElastic(host); got != want {
			t.Errorf("DocumentDBElastic(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestSaveRejectsPostgresIAMOnDocumentDB(t *testing.T) {
	const want = "PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel"
	if err := DocumentDBMode(docdbIAM()); err == nil || err.Error() != want {
		t.Fatalf("DocumentDBMode = %v", err)
	}
	if err := DocumentDBMode(docdb()); err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(docdbIAM()); err == nil || err.Error() != want {
		t.Fatalf("Save = %v", err)
	}
	if err = m.Save(docdb()); err != nil {
		t.Fatal(err)
	}
	if err = m.Update(docdbIAM()); err == nil || err.Error() != want {
		t.Fatalf("Update = %v", err)
	}
	got, _ := m.List()
	if len(got) != 1 || got[0].IAMDatabase() {
		t.Fatalf("stored %+v", got)
	}
}

func TestStartupToleratesLegacyPostgresIAMOnDocumentDB(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "connections.json"), jsonDoc(t, docdbIAM()), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.List()
	if err != nil || len(got) != 1 || !got[0].IAMDatabase() {
		t.Fatalf("legacy bookmark not listed: %v %+v", err, got)
	}
	// An unrelated import still commits beside the legacy bookmark.
	path := filepath.Join(t.TempDir(), "import.json")
	if err = os.WriteFile(path, jsonDoc(t, db()), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := m.Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Commit(plan, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = m.List(); len(got) != 2 {
		t.Fatalf("import not committed: %+v", got)
	}
}

func TestImportRejectsPostgresIAMOnDocumentDB(t *testing.T) {
	const want = "PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel"
	path := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(path, jsonDoc(t, docdbIAM()), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := m.Preview(path)
	if err == nil {
		err = m.Commit(plan, false)
	}
	if err == nil || err.Error() != "documents: "+want {
		t.Fatalf("addition import = %v", err)
	}
	// A replaced conflict is refused; a kept conflict leaves the local copy.
	if err = m.Save(docdb()); err != nil {
		t.Fatal(err)
	}
	if plan, err = m.Preview(path); err != nil || len(plan.Conflicts) != 1 {
		t.Fatalf("preview %v %+v", err, plan)
	}
	if err = m.Commit(plan, true); err == nil || err.Error() != "documents: "+want {
		t.Fatalf("replace import = %v", err)
	}
	if err = m.Commit(plan, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.List(); len(got) != 1 || got[0].IAMDatabase() {
		t.Fatalf("stored %+v", got)
	}
}

func TestResourceKeyStableForDatabaseBookmarks(t *testing.T) {
	iam := db()
	iam.AuthMode, iam.Database, iam.DBUser = "postgres-iam", "exampledb", "reader"
	for c, want := range map[*domain.Connection]string{
		ptr(db()):                                "db|aws|111111111111|db.example.rds.amazonaws.com|5432|eu-west-1|i-0123456789abcdef0|ap-south-1",
		&iam:                                     `db|aws|111111111111|db.example.rds.amazonaws.com|5432|eu-west-1|i-0123456789abcdef0|ap-south-1|["postgres-iam","exampledb","reader"]`,
		ptr(docdb()):                             "db|aws|111111111111|" + docdbHost + "|27017|eu-west-1|i-0123456789abcdef0|ap-south-1",
		ptr(withAuth(docdb(), "tunnel", "", "")): "db|aws|111111111111|" + docdbHost + "|27017|eu-west-1|i-0123456789abcdef0|ap-south-1",
		ptr(withAuth(docdb(), "docdb-iam", "app_db", "")): "db|aws|111111111111|" + docdbHost + "|27017|eu-west-1|i-0123456789abcdef0|ap-south-1|" + `["docdb-iam","app_db",""]`,
	} {
		if got := ResourceKey(*c); got != want {
			t.Errorf("ResourceKey = %q, want %q", got, want)
		}
	}
}

func withAuth(c domain.Connection, mode, database, user string) domain.Connection {
	c.AuthMode, c.Database, c.DBUser = mode, database, user
	return c
}

func ptr(c domain.Connection) *domain.Connection { return &c }

func TestDocumentDBTunnelFileLoadsInV120Schema(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "connections.schema.v1.2.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err = c.AddResource("https://easy-sso.local/v1.2.0.schema.json", raw); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("https://easy-sso.local/v1.2.0.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(docdb()); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	if err = sch.Validate(doc); err != nil {
		t.Fatalf("v1.2.0 schema rejects a DocumentDB tunnel file: %v", err)
	}
}

func TestDocDBIAMBookmarkRules(t *testing.T) {
	elastic := "example.us-east-1.docdb-elastic.amazonaws.com"
	for _, tc := range []struct {
		name     string
		conn     domain.Connection
		endpoint string
		want     string
		// Imports check the file against the schema before the field rules, so
		// the two rules the schema also carries fail there first.
		importWant string
	}{
		{"rds endpoint", withAuth(db(), "docdb-iam", "app_db", ""), "", "DocumentDB IAM login needs a DocumentDB endpoint; set Auth to tunnel", ""},
		{"elastic endpoint", withAuth(docdb(), "docdb-iam", "app_db", ""), elastic, "Elastic clusters do not support IAM login; set Auth to tunnel", ""},
		{"database user", withAuth(docdb(), "docdb-iam", "app_db", "reader"), "", "DocumentDB IAM login uses your role ARN as the user; leave the database username blank", "bookmark schema validation failed"},
		{"bad database name", withAuth(docdb(), "docdb-iam", "bad name", ""), "", "DocumentDB database name may use only letters, digits, _ and -", "bookmark schema validation failed"},
		{"blank database", withAuth(docdb(), "docdb-iam", "", ""), "", "", ""},
		{"safe database name", withAuth(docdb(), "docdb-iam", "app_db-1", ""), "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.conn
			if tc.endpoint != "" {
				c.Endpoint = tc.endpoint
			}
			check := func(op string, err error, want string) {
				t.Helper()
				if want == "" && err != nil {
					t.Fatalf("%s = %v", op, err)
				}
				if want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
					t.Fatalf("%s = %v, want %q", op, err, want)
				}
			}
			importWant := tc.importWant
			if importWant == "" {
				importWant = tc.want
			}
			m, err := New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			check("Save", m.Save(c), tc.want)
			path := filepath.Join(t.TempDir(), "import.json")
			if err = os.WriteFile(path, jsonDoc(t, c), 0o600); err != nil {
				t.Fatal(err)
			}
			m, err = New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := m.Preview(path)
			if err == nil {
				err = m.Commit(plan, false)
			}
			check("import", err, importWant)
		})
	}
}

func TestDocDBIAMAndTunnelOnSameRouteCoexist(t *testing.T) {
	tunnel := docdb()
	iam := withAuth(docdb(), "docdb-iam", "app_db", "")
	iam.ID, iam.Name = "docdb-2", "documents iam"
	state := filepath.Join(t.TempDir(), "state")
	m, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(tunnel); err != nil {
		t.Fatal(err)
	}
	if err = m.Save(iam); err != nil {
		t.Fatal(err)
	}
	m, err = New(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.List()
	if err != nil || len(got) != 2 {
		t.Fatalf("reload: %v %+v", err, got)
	}
	var iamCount int
	for _, c := range got {
		if c.DocDBIAM() {
			iamCount++
		}
	}
	if iamCount != 1 {
		t.Fatalf("docdb-iam bookmarks = %d, want 1", iamCount)
	}
}

func TestDocDBIAMFileRejectedByV120Schema(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "connections.schema.v1.2.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw any
	if err = json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err = c.AddResource("https://easy-sso.local/v1.2.0.schema.json", raw); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("https://easy-sso.local/v1.2.0.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(withAuth(docdb(), "docdb-iam", "app_db", "")); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	// Pinned on purpose: a file with this mode cannot be read by v1.2.0, so
	// downgrading needs the bookmark removed first.
	if err = sch.Validate(doc); err == nil {
		t.Fatal("v1.2.0 schema accepted a docdb-iam bookmark")
	}
}
