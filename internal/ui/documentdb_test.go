package ui

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const docdbHost = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com"

func docdbBookmark(f fixture) domain.Connection {
	p, _ := f.m.profile()
	return domain.Connection{ID: "docdb", Kind: "db", Name: "documents", AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-1", Endpoint: docdbHost, DBPort: 27017, LocalPort: 37017, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DBUser: "reader"}
}

// newDatabaseForm opens a new database form of engine with the endpoint
// field focused and holding host.
func newDatabaseForm(t *testing.T, f fixture, engine, host string) {
	t.Helper()
	f.m.screen = databases
	key(f.m, "n")
	if engine == engDocumentDB {
		key(f.m, "d")
	} else {
		key(f.m, "p")
	}
	fill(f.m, "documents")
	if f.m.form == nil || f.m.form.engine != engine || f.m.form.index != 1 {
		t.Fatal("endpoint field not focused")
	}
	key(f.m, "ctrl+u")
	key(f.m, host)
}

func TestNewDocumentDBFormDefaults(t *testing.T) {
	// The DocumentDB form opens with its own defaults and guidance.
	f := setupUI(t)
	newDatabaseForm(t, f, engDocumentDB, docdbHost)
	if got := []string{f.m.form.get("dbPort"), f.m.form.get("localPort"), f.m.form.get("auth")}; strings.Join(got, ",") != "27017,37017,tunnel" {
		t.Fatalf("DocumentDB defaults %v", got)
	}
	for _, field := range f.m.form.fields {
		if field.id == "dbUser" {
			t.Fatalf("DocumentDB form has a username field: %+v", f.m.form.fields)
		}
	}
	if content := f.m.content(); strings.Contains(content, "rds_iam") || !strings.Contains(content, "DocumentDB user stays in your client") {
		t.Fatalf("form guidance not DocumentDB-specific:\n%s", content)
	}

	// The PostgreSQL form keeps its defaults. Leaving a DocumentDB host there
	// only points to the DocumentDB form, on every way to leave the field.
	const hint = "DocumentDB endpoint: press Esc and choose DocumentDB for its defaults."
	leave := map[string]func(fixture){
		"enter": func(f fixture) { key(f.m, "enter") },
		"tab":   func(f fixture) { key(f.m, "tab") },
		"down":  func(f fixture) { key(f.m, "down") },
		"up":    func(f fixture) { key(f.m, "up") },
		"click": func(f fixture) { clickHit(f.m, mouseHit(t, f.m, "field", "", 3)) },
	}
	for name, leaveField := range leave {
		f := setupUI(t)
		newDatabaseForm(t, f, engPostgres, strings.ToUpper(docdbHost)+" ")
		leaveField(f)
		got := []string{f.m.form.get("dbPort"), f.m.form.get("localPort"), f.m.form.get("auth")}
		if strings.Join(got, ",") != "5432,15432,postgres-iam" || f.m.status != hint {
			t.Fatalf("%s: fields %v status %q", name, got, f.m.status)
		}
	}

	f = setupUI(t)
	newDatabaseForm(t, f, engPostgres, "db.example.rds.amazonaws.com")
	key(f.m, "enter")
	if f.m.status != "" || f.m.form.get("dbPort") != "5432" || f.m.form.get("localPort") != "15432" || f.m.form.get("auth") != "postgres-iam" {
		t.Fatalf("RDS defaults changed: %+v status %q", f.m.form.fields, f.m.status)
	}

	// Editing a saved bookmark never rewrites its values or shows the hint.
	f = setupUI(t)
	f.m.screen = databases
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort, rds.DBUser = "db.example.rds.amazonaws.com", 5432, 15432, ""
	f.m.editConnection(rds)
	f.m.form.index = 1
	key(f.m, "ctrl+u")
	key(f.m, docdbHost)
	key(f.m, "enter")
	if f.m.form.get("dbPort") != "5432" || f.m.form.get("localPort") != "15432" || f.m.status != "" {
		t.Fatalf("edit form rewritten: %+v status %q", f.m.form.fields, f.m.status)
	}
}

func TestDocumentDBSubmitRejectsPostgresIAM(t *testing.T) {
	const want = "PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel"
	f := setupUI(t)
	newDatabaseForm(t, f, engDocumentDB, docdbHost)
	key(f.m, "enter")
	fill(f.m, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "postgres-iam", "", "")
	if f.m.status != "Choose tunnel or docdb-iam for authentication." || f.m.review != nil || f.m.form == nil {
		t.Fatalf("submit status %q review %v", f.m.status, f.m.review)
	}

	// The PostgreSQL form refuses a DocumentDB host before any login rule.
	f = setupUI(t)
	newDatabaseForm(t, f, engPostgres, docdbHost)
	key(f.m, "enter")
	fill(f.m, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "postgres-iam", "", "", "")
	if f.m.status != "This is a DocumentDB endpoint. Press Esc, then n, and choose DocumentDB." || f.m.review != nil || f.m.form == nil {
		t.Fatalf("PostgreSQL form status %q review %v", f.m.status, f.m.review)
	}

	// A legacy bookmark on disk is refused before anything opens.
	f = setupUI(t)
	c := docdbBookmark(f)
	c.AuthMode, c.Database = "postgres-iam", "exampledb"
	if cmd := f.m.open(c); cmd != nil || f.m.status != want || len(f.sessions.opened) != 0 {
		t.Fatalf("open status %q opened %d", f.m.status, len(f.sessions.opened))
	}
}

func TestDocumentDBOffersIAMSetup(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	iamControl := func(where string) {
		t.Helper()
		for _, c := range f.m.controls() {
			if c.key == "l" && c.label == "Set up IAM" {
				return
			}
		}
		t.Fatalf("%s lacks the Set up IAM control: %+v", where, f.m.controls())
	}
	iamControl("databases list")
	if hint := f.m.footer(); !strings.Contains(hint, "l: IAM setup") {
		t.Fatalf("footer hint lacks IAM setup: %q", hint)
	}
	// l on DocumentDB opens the edit form switched to DocumentDB IAM login.
	iamForm := func(where string) {
		t.Helper()
		form := f.m.form
		if form == nil || form.engine != engDocumentDB || form.original == nil || form.get("auth") != "docdb-iam" || form.fields[form.index].id != "database" || f.m.status != docdbLoginStatus {
			t.Fatalf("%s l: form %+v status %q", where, form, f.m.status)
		}
		key(f.m, "esc")
	}
	key(f.m, "l")
	iamForm("list")
	// The button runs the same handler as the key.
	clickHit(f.m, mouseHit(t, f.m, "key", "l", 0))
	iamForm("list click")
	key(f.m, "v")
	if f.m.review == nil || f.m.review.kind != "connection" {
		t.Fatal("details review not open")
	}
	iamControl("connection review")
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"Engine", "DocumentDB", "Tunnel only · your DocumentDB user stays in your client"} {
		if !strings.Contains(content, want) {
			t.Fatalf("review lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"rds_iam", "Set up IAM", "PostgreSQL IAM"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("review shows %q:\n%s", unwanted, content)
		}
	}
	f.m.status = ""
	key(f.m, "l")
	iamForm("review")
	key(f.m, "v")
	clickHit(f.m, mouseHit(t, f.m, "key", "l", 0))
	iamForm("review click")

	// The list panel and the compact layout say the same.
	for _, height := range []int{24, 40} {
		f.m.height = height
		if view := f.m.View(); strings.Contains(view, "PostgreSQL IAM") || !strings.Contains(view, "DocumentDB") {
			t.Fatalf("list panel at height %d:\n%s", height, view)
		}
	}

	// An RDS tunnel keeps its engine label and IAM setup.
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort = "db.example.rds.amazonaws.com", 5432, 15432
	f.m.connections = []domain.Connection{rds}
	f.m.connectionReview(rds)
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "PostgreSQL / other") || !strings.Contains(content, "Set up IAM login (l)") {
		t.Fatalf("RDS review changed:\n%s", content)
	}
}

func TestEmptyStatesMentionDocumentDB(t *testing.T) {
	f := setupUI(t)
	for _, screen := range []screen{databases, tunnels} {
		f.m.screen = screen
		f.m.connections, f.m.runs = nil, nil
		if content := f.m.content(); !strings.Contains(content, "DocumentDB") {
			t.Fatalf("empty state of screen %d lacks DocumentDB:\n%s", screen, content)
		}
	}
}

func TestHelpMentionsDocumentDB(t *testing.T) {
	f := setupUI(t)
	if help := f.m.helpContent(); !strings.Contains(help, "DocumentDB") {
		t.Fatalf("help lacks DocumentDB:\n%s", help)
	}
}

// collect runs cmd and every command it batches, returning their messages.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func caMsgs(msgs []tea.Msg) []caMsg {
	var out []caMsg
	for _, msg := range msgs {
		if ca, ok := msg.(caMsg); ok {
			out = append(out, ca)
		}
	}
	return out
}

func docdbRun(f fixture, port int) domain.Run {
	c := docdbBookmark(f)
	c.LocalPort = port
	p, _ := f.m.profile()
	return domain.Run{ID: "run-docdb", Connection: c, Profile: p, State: "Listening", SessionID: "exact-session"}
}

const sharedOptions = "directConnection=true&retryWrites=false&authSource=admin&tlsAllowInvalidHostnames=true"

func TestDataGripURLAndClientURI(t *testing.T) {
	url := dataGripURL(37017)
	if url != "mongodb://127.0.0.1:37017/?"+sharedOptions {
		t.Fatalf("DataGrip URL %q", url)
	}
	uri := clientURI(37017, "/test-state/rds-ca-aws.pem", false)
	if uri != "mongodb://127.0.0.1:37017/?tls=true&"+sharedOptions+"&tlsCAFile=%2Ftest-state%2Frds-ca-aws.pem" {
		t.Fatalf("client URI %q", uri)
	}
	// A15: a space is %20, never +, so shells and drivers read the same path.
	if got := clientURI(37017, "/state/Application Support/easy-sso/rds-ca-aws.pem", false); !strings.HasSuffix(got, "tlsCAFile=%2Fstate%2FApplication%20Support%2Feasy-sso%2Frds-ca-aws.pem") {
		t.Fatalf("space not escaped: %q", got)
	}
	for _, s := range []string{clientURI(37017, "", false), clientURI(37017, "/test-state/rds-ca-aws.pem", true)} {
		if strings.Contains(s, "tlsCAFile") || !strings.Contains(s, "tls=true&"+sharedOptions) {
			t.Fatalf("elastic or CA-less URI %q", s)
		}
	}
	for _, s := range []string{url, uri} {
		if strings.Contains(s, "@") || strings.Contains(s, "reader") || strings.Contains(s, "password") {
			t.Fatalf("credential in %q", s)
		}
	}
}

func TestDocumentDBReviewShowsClientSteps(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	key(f.m, "v")
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"Amazon DocumentDB", "127.0.0.1 / 37017", "User & Password", "Verify CA", "/test-state/rds-ca-aws.pem (downloads when the tunnel opens)", "one data source each", "\n" + dataGripURL(37017) + "\n", "retryWrites=false works on every engine; 8.0.2+ (not elastic, not planner 1.0) may remove it", "checks the certificate chain"} {
		if !strings.Contains(content, want) {
			t.Fatalf("review lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"rds_iam", "Windows", "Reader endpoint"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("review shows %q:\n%s", unwanted, content)
		}
	}
	if len(*f.ensured) != 0 {
		t.Fatal("bookmark review fetched the CA bundle")
	}
}

func TestDocumentDBRunDetailsShowClientSteps(t *testing.T) {
	f := setupUI(t)
	run := docdbRun(f, 37018)
	f.sessions.runs = []domain.Run{run}
	finish(f.m, f.m.load())
	f.m.screen = tunnels
	f.m.selectedRow = 0
	msgs := collect(press(f.m, "v"))
	if f.m.review == nil || f.m.review.kind != "run" {
		t.Fatal("run details not open")
	}
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"127.0.0.1 / 37018", "127.0.0.1:37018/"} {
		if !strings.Contains(content, want) {
			t.Fatalf("run details lack %q:\n%s", want, content)
		}
	}
	if got := caMsgs(msgs); len(got) != 1 || len(*f.ensured) != 1 {
		t.Fatalf("CA requests %d, msgs %v", len(*f.ensured), msgs)
	}
	f.m.Update(caMsgs(msgs)[0])
	if content = strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "/test-state/rds-ca-aws.pem (cached)") {
		t.Fatalf("CA not shown as cached:\n%s", content)
	}

	// A6: an ended run shows no client steps and asks for a reopen.
	key(f.m, "esc")
	f.m.runs[0].State = "Ended"
	f.sessions.runs[0].State = "Ended"
	if msgs = collect(press(f.m, "v")); len(caMsgs(msgs)) != 0 {
		t.Fatal("ended run fetched the CA bundle")
	}
	if content = strings.Join(f.m.reviewContent(), "\n"); strings.Contains(content, "DATAGRIP") || f.m.status != "Tunnel not open; Enter on the bookmark reopens it." {
		t.Fatalf("ended run: status %q\n%s", f.m.status, content)
	}
}

func TestDocumentDBListeningEnsuresCA(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	other := docdbBookmark(f)
	other.ID, other.Name, other.Endpoint = "other", "aaa-other", "example-2.c0example.us-east-1.docdb.amazonaws.com"
	f.m.connections = []domain.Connection{other}
	f.conns.items = []domain.Connection{other}
	run := docdbRun(f, 37018)
	run.Connection.ID = "" // A2: a first open has no bookmark ID yet
	saved := run.Connection
	saved.ID = "new-id"
	f.conns.items = append(f.conns.items, saved)
	f.sessions.runs = []domain.Run{run}
	_, cmd := f.m.Update(resultMsg{request: f.m.request, epoch: f.m.epoch, action: "Open connection", run: run})
	want := "Tunnel open at 127.0.0.1:37018 · profile [one]. Connect DataGrip or any MongoDB client with your DocumentDB user; c copies the URL, v shows steps."
	if f.m.status != want {
		t.Fatalf("status %q", f.m.status)
	}
	var cas []caMsg
	for _, msg := range collect(cmd) {
		if ca, ok := msg.(caMsg); ok {
			cas = append(cas, ca)
			continue
		}
		f.m.Update(msg)
	}
	if len(cas) != 1 {
		t.Fatalf("CA messages %d", len(cas))
	}
	if ts := f.m.targets(); f.m.selectedRow >= len(ts) || ts[f.m.selectedRow].ID != "new-id" {
		t.Fatalf("selected row %d of %+v", f.m.selectedRow, ts)
	}
	f.m.Update(cas[0])
	key(f.m, "v")
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "(cached)") || !strings.Contains(content, "127.0.0.1 / 37018") {
		t.Fatalf("review after Listening:\n%s", content)
	}

	// A failed CA fetch only changes text; the tunnel stays open.
	f = setupUI(t)
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) {
		return "", errors.New("could not download the AWS RDS CA bundle; check network access or set a local CA file")
	}
	run = docdbRun(f, 37017)
	f.sessions.runs = []domain.Run{run}
	_, cmd = f.m.Update(resultMsg{request: f.m.request, epoch: f.m.epoch, action: "Open connection", run: run})
	for _, msg := range collect(cmd) {
		f.m.Update(msg)
	}
	for _, want := range []string{"Tunnel open at 127.0.0.1:37017", "CA bundle unavailable: could not download", "Databases > Edit (e) > RDS CA file"} {
		if !strings.Contains(f.m.status, want) {
			t.Fatalf("status lacks %q: %q", want, f.m.status)
		}
	}
	if len(f.sessions.stopped) != 0 || len(f.m.runs) != 1 || f.m.runs[0].State != "Listening" {
		t.Fatalf("tunnel changed by CA failure: %+v stopped %v", f.m.runs, f.sessions.stopped)
	}
	f.m.connectionReview(run.Connection)
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "unavailable: could not download") {
		t.Fatalf("CA row lacks error:\n%s", content)
	}
}

func TestDocumentDBCASingleFlightAndStaleCache(t *testing.T) {
	f := setupUI(t)
	c := docdbBookmark(f)
	if f.m.ensureCA(c) == nil || f.m.ensureCA(c) != nil {
		t.Fatal("second CA request while one is in flight")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(nil)
	srv.Close()
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err = os.WriteFile(filepath.Join(dir, "rds-ca-aws.pem"), bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	f = setupUI(t)
	f.m.services.Prerequisites.StateDir = dir
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) {
		return "", errors.New("AWS RDS CA bundle download was invalid")
	}
	f.m.Update(f.m.ensureCA(c)())
	f.m.connectionReview(c)
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "(cached; refresh failed: AWS RDS CA bundle download was invalid)") {
		t.Fatalf("stale cache not labelled:\n%s", content)
	}
}

func TestRDSTunnelUnchangedByDocumentDB(t *testing.T) {
	f := setupUI(t)
	run := domain.Run{State: "Listening", Profile: domain.Profile{Name: "readonly"}, Connection: domain.Connection{Kind: "db", Endpoint: "db.example.rds.amazonaws.com", LocalPort: 15432}}
	_, cmd := f.m.Update(resultMsg{request: f.m.request, epoch: f.m.epoch, action: "Open connection", run: run})
	if len(caMsgs(collect(cmd))) != 0 || len(*f.ensured) != 0 {
		t.Fatal("RDS tunnel fetched a CA bundle")
	}
	if !strings.Contains(f.m.status, "database login not attempted") || !strings.Contains(f.m.status, "Set up IAM login") {
		t.Fatalf("RDS status changed: %q", f.m.status)
	}
}

func TestDocumentDBElasticAndReaderNotes(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example") // after setupUI, which clears it
	elastic := docdbBookmark(f)
	elastic.Endpoint = "example.us-east-1.docdb-elastic.amazonaws.com"
	if f.m.ensureCA(elastic) != nil {
		t.Fatal("elastic cluster fetched a CA bundle")
	}
	f.m.connectionReview(elastic)
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"not needed for elastic clusters; keep Use SSL on", "Use SSL on · no CA file · SSH tunnel off"} {
		if !strings.Contains(content, want) {
			t.Fatalf("elastic lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "rds-ca") || strings.Contains(content, "CA file Windows") {
		t.Fatalf("elastic shows a CA path:\n%s", content)
	}
	reader := docdbBookmark(f)
	reader.Endpoint = "example-cluster.cluster-ro-c0example.us-east-1.docdb.amazonaws.com"
	f.m.connectionReview(reader)
	if content = strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "Reader endpoint: read-only for every user; use the cluster endpoint to write.") {
		t.Fatalf("reader note missing:\n%s", content)
	}
	// B5: the worst case (WSL + reader + engine note) stays within 12 rows.
	if rows := len(f.m.documentDBClient(reader, 37017)) - 2; rows > 12 {
		t.Fatalf("CLIENT block has %d rows", rows)
	}
	for _, line := range f.m.documentDBClient(reader, 37017) {
		if at := strings.Index(line, " | "); at >= 0 && at != 16 {
			t.Fatalf("label wider than the column: %q", line)
		}
	}
	f.m.services.Prerequisites.StateDir = "/test-state"
	cn := docdbBookmark(f)
	cn.Partition = "aws-cn"
	f.m.connectionReview(cn)
	if content = strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "aws-cn needs an RDS CA file (Databases > Edit)") || strings.Contains(content, "CA file Windows") {
		t.Fatalf("aws-cn row:\n%s", content)
	}
}

func TestDocumentDBWSLPath(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	f.m.connectionReview(docdbBookmark(f))
	content := strings.Join(f.m.reviewContent(), "\n")
	windows := strings.Index(content, `CA file Windows  | \\wsl.localhost\Example\test-state\rds-ca-aws.pem`)
	linux := strings.Index(content, "CA file WSL      | /test-state/rds-ca-aws.pem")
	if windows < 0 || linux < 0 || windows > linux {
		t.Fatalf("WSL CA rows missing or out of order:\n%s", content)
	}
	// A18: a CA file already on a Windows drive maps to its drive path.
	if got := wslPath("/mnt/d/certs/global-bundle.pem"); got != `D:\certs\global-bundle.pem` {
		t.Fatalf("drive path %q", got)
	}
	t.Setenv("WSL_DISTRO_NAME", "")
	f.m.connectionReview(docdbBookmark(f))
	if content = strings.Join(f.m.reviewContent(), "\n"); strings.Contains(content, "Windows") || !strings.Contains(content, "CA file          | /test-state/rds-ca-aws.pem") {
		t.Fatalf("native rows:\n%s", content)
	}
	if wslPath("/test-state/rds-ca-aws.pem") != "" {
		t.Fatal("Windows path outside WSL")
	}
}

func TestDocumentDBNativeWindowsPaths(t *testing.T) {
	old := goos
	goos = "windows"
	t.Cleanup(func() { goos = old })
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example") // ignored off Linux
	c := docdbBookmark(f)
	c.CAFile = `D:\certs\global-bundle.pem`
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) { return c.CAFile, nil }
	if wslPath(`D:\certs\global-bundle.pem`) != "" {
		t.Fatal("WSL path on native Windows")
	}
	if got := clientURI(37017, `D:\certs\my certs\global-bundle.pem`, false); !strings.HasSuffix(got, "tlsCAFile=D%3A%5Ccerts%5Cmy%20certs%5Cglobal-bundle.pem") {
		t.Fatalf("drive path escaping %q", got)
	}
	lines := f.m.caRows(c, `D:\certs\global-bundle.pem`, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], `D:\certs\global-bundle.pem`) || strings.Contains(lines[0], "Windows") {
		t.Fatalf("native Windows rows %q", lines)
	}
}

func TestDocumentDBDetailsFit(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 20}} {
		f := setupUI(t)
		t.Setenv("WSL_DISTRO_NAME", "Example")
		reader := docdbBookmark(f)
		reader.Endpoint = "example-cluster.cluster-ro-c0example.us-east-1.docdb.amazonaws.com"
		run := docdbRun(f, 37018)
		run.Connection = reader
		f.sessions.runs = []domain.Run{run}
		finish(f.m, f.m.load())
		f.m.width, f.m.height = size[0], size[1]
		for name, open := range map[string]func(){
			"review": func() { f.m.review = nil; f.m.connectionReview(reader) },
			"run":    func() { f.m.review = &review{kind: "run", run: run} },
		} {
			open()
			if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "CA file Windows") {
				t.Fatalf("%s: WSL rows not rendered:\n%s", name, content)
			}
			for scroll := 0; scroll < 40; scroll++ {
				f.m.scroll = scroll
				lines := strings.Split(f.m.View(), "\n")
				if len(lines) > size[1] {
					t.Fatalf("%s %v scroll %d: %d lines", name, size, scroll, len(lines))
				}
				for _, line := range lines {
					if w := ansi.StringWidth(line); w > size[0] {
						t.Fatalf("%s %v: line width %d: %q", name, size, w, line)
					}
				}
			}
		}
	}
}

func recordCopies(f fixture) *[]string {
	copied := &[]string{}
	f.m.services.CopyText = func(text string) error { *copied = append(*copied, text); return nil }
	return copied
}

func noCredentials(t *testing.T, copied []string) {
	t.Helper()
	for _, s := range copied {
		if strings.Contains(s, "@") || strings.Contains(s, "password") || strings.Contains(s, "reader") {
			t.Fatalf("credential in copied text %q", s)
		}
	}
}

func TestDocumentDBCopyKeys(t *testing.T) {
	f := setupUI(t)
	copied := recordCopies(f)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	f.conns.items = f.m.connections // survives the reload below
	key(f.m, "c")
	if len(*copied) != 1 || (*copied)[0] != dataGripURL(37017) {
		t.Fatalf("c copied %q", *copied)
	}
	if want := "Copied DataGrip URL for 127.0.0.1:37017. In SSH/SSL set the CA file and Verify CA; enter your DocumentDB user in DataGrip."; f.m.status != want {
		t.Fatalf("c status %q", f.m.status)
	}
	clickHit(f.m, mouseHit(t, f.m, "key", "c", 0))
	if len(*copied) != 2 || (*copied)[1] != dataGripURL(37017) {
		t.Fatalf("click copied %q", *copied)
	}
	key(f.m, "C")
	if len(*copied) != 3 || (*copied)[2] != clientURI(37017, "/test-state/rds-ca-aws.pem", false) || !strings.Contains((*copied)[2], "tlsCAFile=%2Ftest-state%2Frds-ca-aws.pem") {
		t.Fatalf("C copied %q", *copied)
	}
	if want := "Copied client URI with tlsCAFile /test-state/rds-ca-aws.pem. In a shell, wrap it in single quotes."; f.m.status != want || len(*f.ensured) != 1 {
		t.Fatalf("C status %q, CA requests %d", f.m.status, len(*f.ensured))
	}
	if !f.m.ca["/test-state/rds-ca-aws.pem"].ready {
		t.Fatal("C did not record the CA result")
	}
	key(f.m, "v")
	key(f.m, "c")
	if len(*copied) != 4 || (*copied)[3] != dataGripURL(37017) {
		t.Fatalf("review c copied %q", *copied)
	}

	// The open run's port wins in the run review and the tunnels list.
	key(f.m, "esc")
	f.sessions.runs = []domain.Run{docdbRun(f, 37018)}
	finish(f.m, f.m.load())
	f.m.screen = tunnels
	f.m.selectedRow = 0
	key(f.m, "c")
	key(f.m, "v")
	key(f.m, "c")
	if len(*copied) != 6 || (*copied)[4] != dataGripURL(37018) || (*copied)[5] != dataGripURL(37018) {
		t.Fatalf("run copies %q", *copied)
	}
	key(f.m, "esc")
	f.m.screen = databases
	f.m.selectedRow = 0
	key(f.m, "c")
	if (*copied)[6] != dataGripURL(37018) {
		t.Fatalf("bookmark with open tunnel copied %q", (*copied)[6])
	}
	noCredentials(t, *copied)
}

func TestDocumentDBCopyFailureNamesDetails(t *testing.T) {
	f := setupUI(t)
	f.m.services.CopyText = func(string) error { return errors.New("no clipboard") }
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	for k, want := range map[string]string{"c": "The URL is in Details (v).", "C": "add tls=true&tlsCAFile=<CA file row> to the URL in Details (v)"} {
		f.m.status = ""
		key(f.m, k)
		if !strings.Contains(f.m.status, "Could not copy") || !strings.Contains(f.m.status, want) || strings.Contains(f.m.status, "Copied") {
			t.Fatalf("%s failure status %q", k, f.m.status)
		}
	}
}

func TestDocumentDBCopyCAEdgeCases(t *testing.T) {
	f := setupUI(t)
	copied := recordCopies(f)
	f.m.screen = databases
	c := docdbBookmark(f)
	f.m.connections = []domain.Connection{c}
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) {
		return "", errors.New("AWS RDS CA bundle download was invalid")
	}
	key(f.m, "C")
	if (*copied)[0] != clientURI(37017, "/test-state/rds-ca-aws.pem", false) || f.m.status != "CA bundle unavailable: AWS RDS CA bundle download was invalid. Copied the URI anyway; its tlsCAFile /test-state/rds-ca-aws.pem may not exist yet. Fix: Databases > Edit (e) > RDS CA file." {
		t.Fatalf("failed CA: copied %q status %q", *copied, f.m.status)
	}
	if f.m.ca["/test-state/rds-ca-aws.pem"].err == "" {
		t.Fatal("failed CA not recorded")
	}
	cn := c
	cn.Partition = "aws-cn"
	f.m.connectionReview(cn) // the list shows only the profile's partition
	key(f.m, "C")
	if (*copied)[1] != clientURI(37017, "", false) || f.m.status != "Copied without tlsCAFile; set RDS CA file for aws-cn." {
		t.Fatalf("aws-cn: copied %q status %q", (*copied)[1], f.m.status)
	}
	elastic := c
	elastic.Endpoint = "example.us-east-1.docdb-elastic.amazonaws.com"
	f.m.connectionReview(elastic)
	key(f.m, "C")
	if strings.Contains((*copied)[2], "tlsCAFile") || !strings.Contains(f.m.status, "Copied client URI") {
		t.Fatalf("elastic: copied %q status %q", (*copied)[2], f.m.status)
	}
	// A6: an ended run copies nothing.
	run := docdbRun(f, 37018)
	run.State = "Ended"
	f.sessions.runs = []domain.Run{run}
	finish(f.m, f.m.load())
	f.m.review = nil
	f.m.screen = tunnels
	f.m.selectedRow = 0
	before := len(*copied)
	key(f.m, "c")
	if len(*copied) != before || f.m.status != "Tunnel not open; Enter on the bookmark reopens it." {
		t.Fatalf("ended run: copied %d status %q", len(*copied)-before, f.m.status)
	}
	noCredentials(t, *copied)
}

func TestDocumentDBWSLClientURI(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	copied := recordCopies(f)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	key(f.m, "C")
	if want := clientURI(37017, `\\wsl.localhost\Example\test-state\rds-ca-aws.pem`, false); len(*copied) != 1 || (*copied)[0] != want {
		t.Fatalf("WSL C copied %q", *copied)
	}
	if !strings.Contains(f.m.status, `tlsCAFile \\wsl.localhost\Example\test-state\rds-ca-aws.pem`) {
		t.Fatalf("WSL C status %q", f.m.status)
	}
	key(f.m, "c")
	if want := "Copied DataGrip URL for 127.0.0.1:37017. CA file: press x to copy it to Windows · Mode Verify CA · log in with your DocumentDB user."; f.m.status != want {
		t.Fatalf("WSL c status %q", f.m.status)
	}
}

func TestCopyKeyRegressions(t *testing.T) {
	f := setupUI(t)
	copied := recordCopies(f)
	f.m.screen = databases
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort = "db.example.rds.amazonaws.com", 5432, 15432
	f.m.connections = []domain.Connection{rds}
	f.m.status = "unchanged"
	for _, k := range []string{"c", "C"} {
		key(f.m, k)
	}
	key(f.m, "v")
	for _, k := range []string{"c", "C"} {
		key(f.m, k)
	}
	if len(*copied) != 0 || f.m.status != "unchanged" {
		t.Fatalf("RDS copied %q status %q", *copied, f.m.status)
	}
	for _, c := range f.m.controls() {
		if c.key == "c" {
			t.Fatal("RDS review offers a copy control")
		}
	}
	// The role review still copies the profile name.
	f.m.review = &review{kind: "role", role: domain.ManagedRole{Alias: "example-profile"}}
	key(f.m, "c")
	if len(*copied) != 1 || (*copied)[0] != "example-profile" {
		t.Fatalf("role review copied %q", *copied)
	}
}

func TestDocumentDBTunnelFooterFits(t *testing.T) {
	for _, width := range []int{80, 100, 140} {
		f := setupUI(t)
		f.sessions.runs = []domain.Run{docdbRun(f, 37018)}
		finish(f.m, f.m.load())
		f.m.screen = tunnels
		f.m.selectedRow = 0
		f.m.width, f.m.height = width, 24
		lines := strings.Split(f.m.View(), "\n")
		if len(lines) > 24 {
			t.Fatalf("%d: %d lines", width, len(lines))
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("%d: line width %d %q", width, w, line)
			}
		}
		copied := recordCopies(f)
		clickHit(f.m, mouseHit(t, f.m, "key", "c", 0))
		if len(*copied) != 1 || (*copied)[0] != dataGripURL(37018) {
			t.Fatalf("%d: click copied %q", width, *copied)
		}
	}
}

func TestHelpListsDocumentDBCopy(t *testing.T) {
	f := setupUI(t)
	help := f.m.helpContent()
	databasesAt, tunnelsAt := strings.Index(help, "DATABASES"), strings.Index(help, "SESSIONS & TUNNELS")
	first := strings.Index(help, "c / C")
	second := strings.LastIndex(help, "c / C")
	if databasesAt < 0 || tunnelsAt < 0 || first < databasesAt || first > tunnelsAt || second < tunnelsAt {
		t.Fatalf("help lacks c / C in both sections:\n%s", help)
	}
}

const exportedCA = `D:\winhome\.easy-sso\rds-ca-aws.pem`

// exportTo makes the export stub succeed like a real export to D:\winhome.
func exportTo(f fixture, changed bool, err error) {
	f.m.services.ExportCA = func(_ context.Context, src string, refreshOnly bool) (domain.CAExport, error) {
		*f.exports = append(*f.exports, fmt.Sprint(src, " ", refreshOnly))
		if err != nil {
			return domain.CAExport{Windows: exportedCA}, err
		}
		return domain.CAExport{Windows: exportedCA, Linux: "/mnt/d/winhome/.easy-sso/rds-ca-aws.pem", Changed: changed, Exists: true}, nil
	}
}

func TestDocumentDBExportCAKey(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	copied := recordCopies(f)
	exportTo(f, true, nil)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	f.conns.items = f.m.connections
	cmd := press(f.m, "x")
	if f.m.status != "Copying the CA file to Windows..." || press(f.m, "x") != nil {
		t.Fatalf("start status %q (or a second export started)", f.m.status)
	}
	finish(f.m, cmd)
	if want := "Copied the CA file and its path: " + exportedCA + ". Paste it into DataGrip SSH/SSL > CA file."; f.m.status != want {
		t.Fatalf("status %q", f.m.status)
	}
	if strings.Join(*f.exports, ",") != "/test-state/rds-ca-aws.pem false" || strings.Join(*copied, ",") != exportedCA || len(*f.ensured) != 1 {
		t.Fatalf("exports %q copied %q ensured %d", *f.exports, *copied, len(*f.ensured))
	}
	key(f.m, "v")
	content := strings.Join(f.m.reviewContent(), "\n")
	if !strings.Contains(content, exportedCA+" (copied by x)") || strings.Contains(content, `\\wsl.localhost`) {
		t.Fatalf("Windows row after export:\n%s", content)
	}
	key(f.m, "C")
	if last := (*copied)[len(*copied)-1]; !strings.HasSuffix(last, "tlsCAFile=D%3A%5Cwinhome%5C.easy-sso%5Crds-ca-aws.pem") {
		t.Fatalf("C after export %q", last)
	}
	key(f.m, "c")
	if want := "Copied DataGrip URL for 127.0.0.1:37017. CA file: " + exportedCA + " · Mode Verify CA · log in with your DocumentDB user."; f.m.status != want {
		t.Fatalf("c after export %q", f.m.status)
	}
	key(f.m, "x")
	key(f.m, "esc")
	f.sessions.runs = []domain.Run{docdbRun(f, 37018)}
	finish(f.m, f.m.load())
	f.m.screen = tunnels
	f.m.selectedRow = 0
	key(f.m, "x")
	key(f.m, "v")
	key(f.m, "x")
	explicit := 0
	for _, e := range *f.exports {
		if e == "/test-state/rds-ca-aws.pem false" {
			explicit++
		}
	}
	if explicit != 4 {
		t.Fatalf("explicit exports %d: %q", explicit, *f.exports)
	}
	noCredentials(t, *copied)
}

func TestDocumentDBExportCAStatuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed bool
		err     error
		copyErr error
		want    string
	}{
		{"unchanged", false, nil, nil, "Windows CA file is up to date: " + exportedCA + ". Path copied to the clipboard."},
		{"clipboard", true, nil, errors.New("no clipboard"), "Copied the CA file to " + exportedCA + ". Could not copy its path; it is in Details (v)."},
		{"no home", false, database.ErrNoWindowsHome, nil, "Windows home folder not found (is WSL interop on?). Copy it yourself: mkdir -p /mnt/c/certs && cp '/test-state/rds-ca-aws.pem' /mnt/c/certs/"},
		{"link", false, store.ErrLink, nil, `Refusing to write through a link in D:\winhome\.easy-sso; remove it or copy the file yourself.`},
		{"foreign", false, database.ErrNotCABundle, nil, exportedCA + " exists and is not a CA bundle; move it away, then press x again."},
		{"in use", false, store.ErrInUse, nil, "Windows has " + exportedCA + " open; close the DataGrip connection, then press x again."},
		{"other", false, errors.New("disk full"), nil, "Could not copy the CA file to Windows: disk full."},
	} {
		f := setupUI(t)
		t.Setenv("WSL_DISTRO_NAME", "Example")
		recordCopies(f)
		if tc.copyErr != nil {
			f.m.services.CopyText = func(string) error { return tc.copyErr }
		}
		exportTo(f, tc.changed, tc.err)
		f.m.connectionReview(docdbBookmark(f))
		key(f.m, "x")
		if f.m.status != tc.want {
			t.Errorf("%s: status %q", tc.name, f.m.status)
		}
		if tc.err != nil && f.m.ca["/test-state/rds-ca-aws.pem"].win != "" {
			t.Errorf("%s: failed export adopted", tc.name)
		}
	}

	// No valid CA: nothing is exported. A stale but valid file still is.
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	recordCopies(f)
	exportTo(f, true, nil)
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) {
		return "", errors.New("AWS RDS CA bundle download was invalid")
	}
	f.m.connectionReview(docdbBookmark(f))
	key(f.m, "x")
	if f.m.status != "CA bundle unavailable: AWS RDS CA bundle download was invalid; nothing copied. Set Databases > Edit (e) > RDS CA file." || len(*f.exports) != 0 {
		t.Fatalf("unavailable: %q exports %q", f.m.status, *f.exports)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	srv := httptest.NewTLSServer(nil)
	srv.Close()
	_ = os.WriteFile(filepath.Join(dir, "rds-ca-aws.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	f.m.services.Prerequisites.StateDir = dir
	key(f.m, "x")
	if !strings.HasPrefix(f.m.status, "CA refresh failed; copied the cached bundle. ") || len(*f.exports) != 1 {
		t.Fatalf("stale: %q exports %q", f.m.status, *f.exports)
	}
}

func TestDocumentDBExportCANoOps(t *testing.T) {
	f := setupUI(t)
	copied := recordCopies(f)
	f.m.connectionReview(docdbBookmark(f))
	key(f.m, "x")
	if f.m.status != "x is for WSL; clients here read the CA file directly: /test-state/rds-ca-aws.pem." || len(*f.exports) != 0 {
		t.Fatalf("native Linux: %q", f.m.status)
	}
	old := goos
	goos = "windows"
	t.Setenv("WSL_DISTRO_NAME", "Example")
	key(f.m, "x")
	goos = old
	if !strings.HasPrefix(f.m.status, "x is for WSL") || len(*f.exports) != 0 {
		t.Fatalf("native Windows: %q", f.m.status)
	}
	for _, tc := range []struct {
		mutate func(*domain.Connection)
		want   string
	}{
		{func(c *domain.Connection) { c.Endpoint = "example.us-east-1.docdb-elastic.amazonaws.com" }, "Elastic clusters need no CA file; keep Use SSL on."},
		{func(c *domain.Connection) { c.Partition = "aws-cn" }, "aws-cn needs an RDS CA file (Databases > Edit) before x can copy it."},
		{func(c *domain.Connection) { c.CAFile = "/mnt/d/certs/global-bundle.pem" }, `CA file is already on Windows: D:\certs\global-bundle.pem. Path copied to the clipboard.`},
	} {
		c := docdbBookmark(f)
		tc.mutate(&c)
		f.m.connectionReview(c)
		key(f.m, "x")
		if f.m.status != tc.want || len(*f.exports) != 0 {
			t.Fatalf("status %q exports %q", f.m.status, *f.exports)
		}
	}
	if strings.Join(*copied, ",") != `D:\certs\global-bundle.pem` {
		t.Fatalf("copied %q", *copied)
	}
	// RDS, EC2 and credentials ignore x.
	f.m.review = nil
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort = "db.example.rds.amazonaws.com", 5432, 15432
	f.m.connections = []domain.Connection{rds}
	for _, s := range []screen{databases, ec2, credentials} {
		f.m.screen = s
		f.m.status = "unchanged"
		key(f.m, "x")
		if f.m.status != "unchanged" {
			t.Fatalf("screen %d: %q", s, f.m.status)
		}
	}
	f.m.connectionReview(rds)
	f.m.status = "unchanged"
	key(f.m, "x")
	if f.m.status != "unchanged" || len(*f.exports) != 0 {
		t.Fatalf("RDS review: %q", f.m.status)
	}
}

func TestDocumentDBExportCAPassiveAdopt(t *testing.T) {
	f := setupUI(t)
	const path = "/test-state/rds-ca-aws.pem"
	// Off WSL a CA check never probes Windows.
	_, cmd := f.m.Update(caMsg{path: path})
	if cmd != nil {
		t.Fatal("native CA check started an export")
	}
	t.Setenv("WSL_DISTRO_NAME", "Example")
	exportTo(f, false, nil)
	f.m.status = "unchanged"
	_, cmd = f.m.Update(caMsg{path: path})
	finish(f.m, cmd)
	if strings.Join(*f.exports, ",") != path+" true" || f.m.ca[path].win != exportedCA || f.m.status != "unchanged" {
		t.Fatalf("adopt: exports %q win %q status %q", *f.exports, f.m.ca[path].win, f.m.status)
	}
	exportTo(f, false, errors.New("disk full"))
	_, cmd = f.m.Update(caMsg{path: path})
	finish(f.m, cmd)
	if len(*f.exports) != 2 || f.m.status != "unchanged" {
		t.Fatalf("refresh: exports %q status %q", *f.exports, f.m.status)
	}
	f.m.connectionReview(docdbBookmark(f))
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, exportedCA+" (refresh failed: disk full; x retries)") {
		t.Fatalf("refresh failure row:\n%s", content)
	}
	// A probe that found nothing is not repeated for every check.
	f = setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	for range 2 {
		_, cmd = f.m.Update(caMsg{path: path})
		finish(f.m, cmd)
	}
	if len(*f.exports) != 1 {
		t.Fatalf("absent export probed %d times", len(*f.exports))
	}
}

func TestHelpListsDocumentDBExport(t *testing.T) {
	f := setupUI(t)
	if help := f.m.helpContent(); !strings.Contains(help, `%USERPROFILE%\.easy-sso`) || !strings.Contains(help, "c / C / x") {
		t.Fatalf("help lacks x:\n%s", help)
	}
}

func TestDocumentDBCAStatusLeadsWithTheProblem(t *testing.T) {
	const path = "/test-state/rds-ca-aws.pem"
	open := func(f fixture) {
		run := docdbRun(f, 37017)
		f.sessions.runs = []domain.Run{run}
		f.m.Update(resultMsg{request: f.m.request, epoch: f.m.epoch, action: "Open connection", run: run})
	}
	f := setupUI(t)
	open(f)
	f.m.Update(caMsg{path: path, err: errors.New("could not download the AWS RDS CA bundle; check network access or set a local CA file")})
	if want := "Tunnel open at 127.0.0.1:37017 · CA bundle unavailable: could not download the AWS RDS CA bundle; check network access or set a local CA file. Fix: Databases > Edit (e) > RDS CA file."; f.m.status != want {
		t.Fatalf("status %q", f.m.status)
	}
	// A stale but valid bundle is still usable: the row says so, the status stays.
	f = setupUI(t)
	open(f)
	before := f.m.status
	f.m.Update(caMsg{path: path, err: errors.New("offline"), stale: true})
	if f.m.status != before {
		t.Fatalf("stale status %q", f.m.status)
	}
	// Another bundle's failure never lands on this tunnel's status.
	f.m.Update(caMsg{path: "/elsewhere/custom.pem", err: errors.New("RDS CA file unavailable or invalid")})
	if f.m.status != before {
		t.Fatalf("other path status %q", f.m.status)
	}
}

func TestDocumentDBBookmarkReviewChecksCAOfOpenTunnel(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	f.conns.items = f.m.connections
	f.sessions.runs = []domain.Run{docdbRun(f, 37018)}
	finish(f.m, f.m.load())
	key(f.m, "v")
	if len(*f.ensured) != 1 {
		t.Fatalf("CA requests %d", len(*f.ensured))
	}
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "(cached)") || !strings.Contains(content, "127.0.0.1 / 37018") {
		t.Fatalf("review of an open tunnel:\n%s", content)
	}
}

func TestDocumentDBRoutesUse127001(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	f.conns.items = f.m.connections
	f.sessions.runs = []domain.Run{docdbRun(f, 37018)}
	finish(f.m, f.m.load())
	views := []string{f.m.View()}
	f.m.connectionReview(docdbBookmark(f))
	views = append(views, strings.Join(f.m.reviewContent(), "\n"))
	f.m.review = &review{kind: "run", run: f.m.runs[0]}
	views = append(views, strings.Join(f.m.reviewContent(), "\n"))
	f.m.review = nil
	f.m.screen = tunnels
	views = append(views, f.m.View())
	for i, v := range views {
		if strings.Contains(strings.ReplaceAll(v, "wsl.localhost", ""), "localhost") {
			t.Fatalf("view %d shows localhost:\n%s", i, v)
		}
	}
	// RDS rows keep their wording.
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort = "db.example.rds.amazonaws.com", 5432, 15432
	f.m.connectionReview(rds)
	if !strings.Contains(strings.Join(f.m.reviewContent(), "\n"), "localhost:15432") {
		t.Fatal("RDS route changed")
	}
}

// visible is what the two-line status bar shows at width, spaces dropped so
// a wrapped path still matches.
func visible(status string, width int) string {
	side := 0
	if width >= 120 {
		side = 33
	} else if width >= 100 {
		side = 29
	}
	return strings.ReplaceAll(strings.Join(clipStatus(status, width-side-4), ""), " ", "")
}

func TestDocumentDBStatusesKeepKeyInfoVisible(t *testing.T) {
	check := func(name, status, want string) {
		t.Helper()
		for _, width := range []int{80, 100} {
			if !strings.Contains(visible(status, width), strings.ReplaceAll(want, " ", "")) {
				t.Errorf("%s at %d cuts %q from %q", name, width, want, status)
			}
		}
	}
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	recordCopies(f)
	f.m.connectionReview(docdbBookmark(f))
	key(f.m, "c")
	check("c before export", f.m.status, "press x")
	f.m.ca["/test-state/rds-ca-aws.pem"] = caResult{ready: true, win: `D:\winhome\example-profile-folder\.easy-sso\rds-ca-aws.pem`}
	key(f.m, "c")
	check("c after export", f.m.status, `D:\winhome\example-profile-folder\.easy-sso\rds-ca-aws.pem`)
	f.m.ca = map[string]caResult{}
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) {
		return "", errors.New("could not download the AWS RDS CA bundle; check network access or set a local CA file")
	}
	key(f.m, "C")
	check("C with CA failure", f.m.status, "CA bundle unavailable")
	exportTo(f, true, nil)
	f.m.services.EnsureCA = func(context.Context, domain.Connection) (string, error) { return "/test-state/rds-ca-aws.pem", nil }
	key(f.m, "x")
	check("x", f.m.status, exportedCA)
}

func TestDocumentDBExplicitExportWaitsForQuietProbe(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	copied := recordCopies(f)
	exportTo(f, true, nil)
	const path = "/test-state/rds-ca-aws.pem"
	_, probe := f.m.Update(caMsg{path: path})
	if probe == nil {
		t.Fatal("no quiet probe")
	}
	f.m.connectionReview(docdbBookmark(f))
	if cmd := press(f.m, "x"); cmd != nil || f.m.status != "Copying the CA file to Windows..." {
		t.Fatalf("x during probe: cmd %v status %q", cmd != nil, f.m.status)
	}
	finish(f.m, probe)
	if len(*copied) != 1 || !strings.HasPrefix(f.m.status, "Copied the CA file") {
		t.Fatalf("queued x did not run: copied %q status %q", *copied, f.m.status)
	}
}

func TestDocumentDBCopyAdoptsExportAfterC(t *testing.T) {
	f := setupUI(t)
	t.Setenv("WSL_DISTRO_NAME", "Example")
	copied := recordCopies(f)
	exportTo(f, false, nil)
	f.m.connectionReview(docdbBookmark(f))
	key(f.m, "C")
	key(f.m, "C")
	// Each successful check refreshes quietly; none is an explicit export.
	if len(*f.exports) == 0 || strings.Contains(strings.Join(*f.exports, ","), "false") || !strings.HasSuffix((*copied)[1], "tlsCAFile=D%3A%5Cwinhome%5C.easy-sso%5Crds-ca-aws.pem") {
		t.Fatalf("exports %q copied %q", *f.exports, *copied)
	}
}

func TestDocumentDBListeningSelectsOpenedBookmark(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	first, second := docdbBookmark(f), docdbBookmark(f)
	first.ID, first.Name = "first", "aaa"
	second.ID, second.Name, second.LocalPort = "second", "bbb", 37018
	f.conns.items = []domain.Connection{first, second}
	run := docdbRun(f, 37018)
	run.Connection = second
	f.sessions.runs = []domain.Run{run}
	_, cmd := f.m.Update(resultMsg{request: f.m.request, epoch: f.m.epoch, action: "Open connection", run: run})
	for _, msg := range collect(cmd) {
		if _, ok := msg.(caMsg); !ok {
			f.m.Update(msg)
		}
	}
	if ts := f.m.targets(); ts[f.m.selectedRow].ID != "second" {
		t.Fatalf("selected %q", ts[f.m.selectedRow].ID)
	}
}

func docdbIAMBookmark(f fixture) domain.Connection {
	c := docdbBookmark(f)
	c.AuthMode, c.Database, c.DBUser = "docdb-iam", "app_db", ""
	return c
}

func TestDocDBIAMConnectOpensTerminal(t *testing.T) {
	for _, via := range []string{"key", "mouse"} {
		t.Run(via, func(t *testing.T) {
			f := setupUI(t)
			activateOne(t, f)
			c := docdbIAMBookmark(f)
			f.m.screen, f.m.focus = databases, 1
			f.conns.items = []domain.Connection{c}
			finish(f.m, f.m.load())
			f.sessions.clientState = "Connected"
			f.m.services.Terminals = &testTerminals{frames: map[string]string{"run-1": "app_db> "}}
			if controls := f.m.controls(); controls[0].label != "Connect terminal" || controls[0].key != "enter" {
				t.Fatalf("primary action %+v", controls[0])
			}
			for _, c := range f.m.controls() {
				if c.key == "l" {
					t.Fatal("docdb-iam bookmark offers IAM setup again")
				}
			}
			if hint := f.m.footer(); !strings.Contains(hint, "Enter: connect") || strings.Contains(hint, "open tunnel") || strings.Contains(hint, "IAM setup") {
				t.Fatalf("footer %q", hint)
			}
			if via == "key" {
				key(f.m, "enter")
			} else {
				clickHit(f.m, mouseHit(t, f.m, "key", "enter", 0))
			}
			if len(f.sessions.opened) != 1 || f.sessions.opened[0].AuthMode != "docdb-iam" {
				t.Fatalf("opened %+v status %q", f.sessions.opened, f.m.status)
			}
			if len(f.m.terminalTabs) != 1 || f.m.terminalTabs[0].ID != "run-1" {
				t.Fatalf("terminal not shown: tabs %+v status %q", f.m.terminalTabs, f.m.status)
			}
			if !strings.Contains(f.m.status, "Terminal connected") {
				t.Fatalf("status %q", f.m.status)
			}
		})
	}
}

func TestDocDBIAMDetailsShowRoleARN(t *testing.T) {
	f := setupUI(t)
	c := docdbIAMBookmark(f)
	p, _ := f.m.profile()
	id := domain.Identity{Account: p.AccountID, ARN: "arn:aws:sts::" + p.AccountID + ":assumed-role/AWSReservedSSO_ReadOnly_0123456789abcdef/dev", UserID: "id:dev"}
	arn, err := profiles.IAMRoleARN(id)
	if err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-1", State: "Connected", Connection: c, Profile: p, Identity: id}
	details := strings.Join(f.m.runDetails(run), "\n")
	for _, want := range []string{"DocumentDB IAM · user is your role ARN", arn, "app_db"} {
		if !strings.Contains(details, want) {
			t.Fatalf("run details lack %q:\n%s", want, details)
		}
	}
	if strings.Contains(details, id.ARN) {
		// The session ARN is not the $external user name.
		t.Fatalf("run details show the STS session ARN as the user:\n%s", details)
	}
	f.m.connectionReview(c)
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"DocumentDB IAM · user is your role ARN", "app_db"} {
		if !strings.Contains(content, want) {
			t.Fatalf("review lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"PostgreSQL IAM", "rds_iam", "your DocumentDB user stays in your client"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("review shows %q:\n%s", unwanted, content)
		}
	}
	f.m.review = nil
	f.m.screen = databases
	f.m.connections = []domain.Connection{c}
	for _, height := range []int{24, 40} {
		f.m.height = height
		if view := f.m.View(); !strings.Contains(view, "DocumentDB IAM") || strings.Contains(view, "PostgreSQL IAM") {
			t.Fatalf("list panel at height %d:\n%s", height, view)
		}
	}
}

const iamOptions = "directConnection=true&retryWrites=false&authSource=%24external&authMechanism=MONGODB-AWS&tlsAllowInvalidHostnames=true"

func TestIAMGuideRowsFit(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 20}} {
		f, _, run := setupLaunch(t)
		t.Setenv("WSL_DISTRO_NAME", "Example")
		reader := run.Connection
		reader.Endpoint = "example-cluster.cluster-ro-c0example.us-east-1.docdb.amazonaws.com"
		run.Connection = reader
		f.sessions.runs = []domain.Run{run}
		f.conns.items = []domain.Connection{reader}
		// The review takes the ARN from the active role, the run from its own identity.
		f.creds.roles = []domain.ManagedRole{{Alias: "managed-one", Profile: run.Profile, Identity: run.Identity, Status: "active", Expiration: time.Now().Add(time.Hour)}}
		finish(f.m, f.m.load())
		arn := runRoleARN(run)
		guide := f.m.documentDBIAMClient(reader, 37018, arn)
		// B5: the worst case (WSL + reader) stays within 12 rows after the title.
		if rows := len(guide) - 2; rows > 12 {
			t.Fatalf("IAM guide has %d rows:\n%s", rows, strings.Join(guide, "\n"))
		}
		text := strings.Join(guide, "\n")
		for _, want := range []string{
			f.m.detailRow("Login", "IAM (MONGODB-AWS), no password"),
			f.m.detailRow("DB user", arn),
			f.m.detailRow("DataGrip", "URL only, Authentication No auth, then g to start it"),
			f.m.detailRow("Admin", "createUser in $external with mechanisms MONGODB-AWS (v shows the command)"),
			"\n" + dataGripIAMURL(37018) + "\n",
			"CA file Windows", "Reader endpoint",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("guide lacks %q:\n%s", want, text)
			}
		}
		for _, unwanted := range []string{"User & Password", "authSource=admin", "your DocumentDB user"} {
			if strings.Contains(text, unwanted) {
				t.Fatalf("IAM guide shows %q:\n%s", unwanted, text)
			}
		}
		f.m.width, f.m.height = size[0], size[1]
		for name, open := range map[string]func(){
			"review": func() { f.m.review = nil; f.m.connectionReview(reader) },
			"run":    func() { f.m.review = &review{kind: "run", run: run} },
		} {
			open()
			content := strings.Join(f.m.reviewContent(), "\n")
			command := `db.getSiblingDB("$external").runCommand({createUser: "` + arn + `", mechanisms: ["MONGODB-AWS"], roles: [{role: "read", db: "app_db"}]})`
			for _, want := range []string{"IAM (MONGODB-AWS), no password", command, "5.0 instance-based; not elastic; never the primary user", "db.runCommand({connectionStatus:1})"} {
				if !strings.Contains(content, want) {
					t.Fatalf("%s lacks %q:\n%s", name, want, content)
				}
			}
			if strings.Contains(content, "User & Password") || strings.Contains(content, dataGripURL(37018)) {
				t.Fatalf("%s shows the tunnel guide:\n%s", name, content)
			}
			for scroll := 0; scroll < 50; scroll++ {
				f.m.scroll = scroll
				lines := strings.Split(f.m.View(), "\n")
				if len(lines) > size[1] {
					t.Fatalf("%s %v scroll %d: %d lines", name, size, scroll, len(lines))
				}
				for _, line := range lines {
					if w := ansi.StringWidth(line); w > size[0] {
						t.Fatalf("%s %v: line width %d: %q", name, size, w, line)
					}
				}
			}
		}
	}
}

func TestIAMReviewRoleARNComesFromActiveRole(t *testing.T) {
	f := setupUI(t)
	c := docdbIAMBookmark(f)
	f.m.connectionReview(c)
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, f.m.detailRow("DB user", "Log in to resolve your role ARN")) || !strings.Contains(content, `createUser: "<role ARN>"`) {
		t.Fatalf("no role: \n%s", content)
	}
	p, _ := f.m.profile()
	id := domain.Identity{Account: p.AccountID, ARN: "arn:aws:sts::" + p.AccountID + ":assumed-role/ExampleRole/dev", UserID: "id:dev"}
	f.m.roles = []domain.ManagedRole{{Alias: "managed-one", Profile: p, Identity: id, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	c.Database = ""
	f.m.connectionReview(c)
	content := strings.Join(f.m.reviewContent(), "\n")
	arn := "arn:aws:iam::" + p.AccountID + ":role/ExampleRole"
	if !strings.Contains(content, f.m.detailRow("DB user", arn)) || !strings.Contains(content, `createUser: "`+arn+`"`) || !strings.Contains(content, `db: "<db>"`) {
		t.Fatalf("active role ARN missing:\n%s", content)
	}
}

func TestIAMURLsCarryNoSecret(t *testing.T) {
	if got := dataGripIAMURL(37018); got != "mongodb://127.0.0.1:37018/?"+iamOptions {
		t.Fatalf("DataGrip IAM URL %q", got)
	}
	if got := clientIAMURI(37018, "/test-state/rds-ca-aws.pem"); got != "mongodb://127.0.0.1:37018/?tls=true&"+iamOptions+"&tlsCAFile=%2Ftest-state%2Frds-ca-aws.pem" {
		t.Fatalf("client IAM URI %q", got)
	}
	f, _, run := setupLaunch(t, dataGripLocal)
	activateOne(t, f) // the fake snapshot holds the fixture secrets
	copied := recordCopies(f)
	// The bookmark row, its review, the run row and the run review all copy
	// the IAM strings with the run's port.
	key(f.m, "c")
	key(f.m, "C")
	clickHit(f.m, mouseHit(t, f.m, "key", "c", 0))
	key(f.m, "v")
	key(f.m, "c")
	key(f.m, "C")
	key(f.m, "esc")
	f.m.screen, f.m.selectedRow = tunnels, 0
	key(f.m, "c")
	key(f.m, "v")
	key(f.m, "C")
	want := []string{dataGripIAMURL(37018), clientIAMURI(37018, "/test-state/rds-ca-aws.pem"), dataGripIAMURL(37018), dataGripIAMURL(37018), clientIAMURI(37018, "/test-state/rds-ca-aws.pem"), dataGripIAMURL(37018), clientIAMURI(37018, "/test-state/rds-ca-aws.pem")}
	if strings.Join(*copied, "\n") != strings.Join(want, "\n") {
		t.Fatalf("copied:\n%s\nwant:\n%s", strings.Join(*copied, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(f.m.status, "Copied client URI with tlsCAFile") {
		t.Fatalf("C status %q", f.m.status)
	}
	key(f.m, "esc")
	key(f.m, "c")
	if !strings.Contains(f.m.status, "No auth") || strings.Contains(f.m.status, "DocumentDB user") {
		t.Fatalf("c status %q", f.m.status)
	}
	shown := append(append([]string(nil), *copied...), f.m.status, strings.Join(f.m.runDetails(run), "\n"), f.m.View())
	for _, s := range shown {
		for _, secret := range []string{"@", "ASIA", "AKIA", "SECRET_ACCESS_TEST_ONLY", "SECRET_TEST_ONLY", "TOKEN_TEST_ONLY"} {
			if strings.Contains(s, secret) {
				t.Fatalf("%q in %q", secret, s)
			}
		}
	}
}

func TestTunnelURLUnchanged(t *testing.T) {
	if documentDBOptions != "directConnection=true&retryWrites=false&authSource=admin&tlsAllowInvalidHostnames=true" || documentDBIAMOptions != iamOptions {
		t.Fatal("options changed")
	}
	f, ls, _ := setupLaunch(t, dataGripLocal)
	copied := recordCopies(f)
	tunnel := docdbBookmark(f)
	tunnel.ID = "tunnel"
	f.m.connections = []domain.Connection{tunnel}
	f.m.selectedRow = 0
	key(f.m, "c")
	key(f.m, "C")
	if len(*copied) != 2 || (*copied)[0] != dataGripURL(37018) || (*copied)[1] != clientURI(37018, "/test-state/rds-ca-aws.pem", false) {
		t.Fatalf("tunnel copies %q", *copied)
	}
	f.m.connectionReview(tunnel)
	content := strings.Join(f.m.reviewContent(), "\n")
	if !strings.Contains(content, "User & Password") || strings.Contains(content, "MONGODB-AWS") || strings.Contains(content, "g to start") {
		t.Fatalf("tunnel guide changed:\n%s", content)
	}
	if hasControl(f.m, "Open client", "g") || strings.Contains(f.m.footer(), "g:") {
		t.Fatal("tunnel offers g")
	}
	key(f.m, "g")
	if ls.lookups != 0 || f.m.review == nil || f.m.review.kind != "connection" {
		t.Fatalf("g on a tunnel bookmark: lookups %d review %+v", ls.lookups, f.m.review)
	}
}

func TestIAMListRowAndFooterOfferLaunch(t *testing.T) {
	f, _, _ := setupLaunch(t, dataGripLocal)
	if hint := f.m.footer(); !strings.Contains(hint, "g: open client") || !strings.Contains(hint, "c: copy URL") {
		t.Fatalf("bookmark footer %q", hint)
	}
	if view := f.m.View(); !strings.Contains(view, "g starts one") {
		t.Fatalf("list panel lacks the launch hint:\n%s", view)
	}
	f.m.screen, f.m.selectedRow = tunnels, 0
	if hint := f.m.footer(); !strings.Contains(hint, "g: open client") {
		t.Fatalf("tunnels footer %q", hint)
	}
	key(f.m, "v")
	if hint := f.m.footer(); !strings.Contains(hint, "g: open client") {
		t.Fatalf("run review footer %q", hint)
	}
}

func TestHelpListsLaunchKey(t *testing.T) {
	f := setupUI(t)
	help := f.m.helpContent()
	databasesAt, tunnelsAt := strings.Index(help, "DATABASES"), strings.Index(help, "SESSIONS & TUNNELS")
	first, second := strings.Index(help, "\ng "), strings.LastIndex(help, "\ng ")
	if first < databasesAt || first > tunnelsAt || second < tunnelsAt || !strings.Contains(help, "MongoDB Compass") {
		t.Fatalf("help lacks g in both sections:\n%s", help)
	}
}
