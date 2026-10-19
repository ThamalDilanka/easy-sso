package ui

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const docdbHost = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com"

func docdbBookmark(f fixture) domain.Connection {
	p, _ := f.m.profile()
	return domain.Connection{ID: "docdb", Kind: "db", Name: "documents", AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-1", Endpoint: docdbHost, DBPort: 27017, LocalPort: 37017, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DBUser: "reader"}
}

// newDatabaseForm opens a new database form with the endpoint field focused
// and holding host.
func newDatabaseForm(t *testing.T, f fixture, host string) {
	t.Helper()
	f.m.screen = databases
	key(f.m, "n")
	fill(f.m, "documents")
	if f.m.form == nil || f.m.form.index != 1 {
		t.Fatal("endpoint field not focused")
	}
	key(f.m, "ctrl+u")
	key(f.m, host)
}

func TestNewDocumentDBFormDefaults(t *testing.T) {
	const status = "DocumentDB endpoint: ports set to 27017/37017, auth set to tunnel"
	leave := map[string]func(fixture){
		"enter": func(f fixture) { key(f.m, "enter") },
		"tab":   func(f fixture) { key(f.m, "tab") },
		"down":  func(f fixture) { key(f.m, "down") },
		"up":    func(f fixture) { key(f.m, "up") },
		"click": func(f fixture) { clickHit(f.m, mouseHit(t, f.m, "field", "", 3)) },
	}
	for name, leaveField := range leave {
		f := setupUI(t)
		newDatabaseForm(t, f, strings.ToUpper(docdbHost)+" ")
		leaveField(f)
		got := []string{f.m.form.fields[3].value, f.m.form.fields[6].value, f.m.form.fields[7].value}
		if strings.Join(got, ",") != "27017,37017,tunnel" || f.m.status != status {
			t.Fatalf("%s: fields %v status %q", name, got, f.m.status)
		}
		content := f.m.content()
		if strings.Contains(content, "rds_iam") || !strings.Contains(content, "DocumentDB user stays in your client") {
			t.Fatalf("%s: form guidance not DocumentDB-specific:\n%s", name, content)
		}
	}

	// A value the user typed is kept; only untouched defaults switch.
	f := setupUI(t)
	newDatabaseForm(t, f, docdbHost)
	f.m.form.fields[3].value = "27018"
	key(f.m, "enter")
	if f.m.form.fields[3].value != "27018" || f.m.form.fields[6].value != "37017" {
		t.Fatalf("typed port replaced: %+v", f.m.form.fields)
	}

	// Leaving again with nothing to change keeps the status quiet.
	f.m.status = ""
	key(f.m, "up")
	key(f.m, "down")
	if f.m.status != "" {
		t.Fatalf("status repeated: %q", f.m.status)
	}

	f = setupUI(t)
	newDatabaseForm(t, f, "db.example.rds.amazonaws.com")
	key(f.m, "enter")
	if f.m.form.fields[3].value != "5432" || f.m.form.fields[6].value != "15432" || f.m.form.fields[7].value != "postgres-iam" {
		t.Fatalf("RDS defaults changed: %+v", f.m.form.fields)
	}

	// Editing a saved bookmark never rewrites its values.
	f = setupUI(t)
	f.m.screen = databases
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort, rds.DBUser = "db.example.rds.amazonaws.com", 5432, 15432, ""
	f.m.editConnection(rds)
	f.m.form.index = 1
	key(f.m, "ctrl+u")
	key(f.m, docdbHost)
	key(f.m, "enter")
	if f.m.form.fields[3].value != "5432" || f.m.form.fields[6].value != "15432" {
		t.Fatalf("edit form rewritten: %+v", f.m.form.fields)
	}
}

func TestDocumentDBSubmitRejectsPostgresIAM(t *testing.T) {
	const want = "PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel"
	f := setupUI(t)
	newDatabaseForm(t, f, docdbHost)
	key(f.m, "enter")
	// Database name and user stay blank: the DocumentDB message wins.
	fill(f.m, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "postgres-iam", "", "", "")
	if f.m.status != want || f.m.review != nil || f.m.form == nil {
		t.Fatalf("submit status %q review %v", f.m.status, f.m.review)
	}

	// A legacy bookmark on disk is refused before anything opens.
	f = setupUI(t)
	c := docdbBookmark(f)
	c.AuthMode, c.Database = "postgres-iam", "exampledb"
	if cmd := f.m.open(c); cmd != nil || f.m.status != want || len(f.sessions.opened) != 0 {
		t.Fatalf("open status %q opened %d", f.m.status, len(f.sessions.opened))
	}
}

func TestDocumentDBHidesIAMSetup(t *testing.T) {
	const status = "IAM login setup is for PostgreSQL; DocumentDB uses your own user in the client."
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	noIAMControl := func(where string) {
		t.Helper()
		for _, c := range f.m.controls() {
			if c.key == "l" {
				t.Fatalf("%s offers %q", where, c.label)
			}
		}
	}
	noIAMControl("databases list")
	if hint := f.m.footer(); strings.Contains(hint, "IAM") {
		t.Fatalf("footer hint offers IAM setup: %q", hint)
	}
	key(f.m, "l")
	if f.m.form != nil || f.m.status != status {
		t.Fatalf("list l: form %v status %q", f.m.form != nil, f.m.status)
	}
	key(f.m, "v")
	if f.m.review == nil || f.m.review.kind != "connection" {
		t.Fatal("details review not open")
	}
	noIAMControl("connection review")
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
	if f.m.form != nil || f.m.status != status {
		t.Fatalf("review l: form %v status %q", f.m.form != nil, f.m.status)
	}

	// The list panel and the compact layout say the same.
	key(f.m, "esc")
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
