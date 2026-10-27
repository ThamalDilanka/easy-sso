package ui

import (
	"context"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"strconv"
	"strings"
)

// newForm opens the EC2 form, or on Databases the type picker; a database
// form is built only once its type is chosen.
func (m *Model) newForm() {
	if m.screen == databases {
		r := &review{kind: "new-database", listScroll: m.scroll}
		if draft, ok := m.drafts[databases]; ok && draft.engine == engDocumentDB {
			r.choice = 1
		}
		m.review, m.scroll = r, 0
		return
	}
	if draft, ok := m.drafts[m.screen]; ok {
		draft.fields = append([]field(nil), draft.fields...)
		m.form = &draft
		return
	}
	p, _ := m.profile()
	m.form = &form{kind: "ec2", fields: ec2Fields(domain.Connection{Region: p.Region})}
}

// newDatabaseForm opens a new database form for engine. A draft of the same
// engine resumes; a draft of the other engine stays until this form replaces it.
func (m *Model) newDatabaseForm(engine string) {
	m.review = nil
	if draft, ok := m.drafts[databases]; ok && draft.engine == engine {
		draft.fields = append([]field(nil), draft.fields...)
		m.form = &draft
		return
	}
	p, _ := m.profile()
	c := domain.Connection{Kind: "db", Region: p.Region, RelayRegion: p.Region, DBPort: 5432, LocalPort: 15432, AuthMode: "postgres-iam"}
	if engine == engDocumentDB {
		c.DBPort, c.LocalPort, c.AuthMode = 27017, 37017, ""
	}
	m.form = &form{kind: "db", engine: engine, fields: databaseFields(engine, c)}
}

// bookmarkEngine is the form a saved database bookmark edits in. The type of
// a saved bookmark is fixed, so editing never shows the type picker.
func bookmarkEngine(c domain.Connection) string {
	if isDocumentDB(c) || c.DocDBIAM() {
		return engDocumentDB
	}
	return engPostgres
}

func (m *Model) editConnection(c domain.Connection) {
	m.form = &form{kind: c.Kind, fields: ec2Fields(c)}
	if c.Kind == "db" {
		engine := bookmarkEngine(c)
		m.form.engine, m.form.fields = engine, databaseFields(engine, c)
	}
	if c.ID != "" {
		m.form.original = &c
	}
	m.scroll = 0
}

// focus moves the cursor to the field with id, if the form has one.
func (f *form) focus(id string) {
	for i, field := range f.fields {
		if field.id == id {
			f.index = i
		}
	}
}

func (m *Model) setupDatabaseLogin(c domain.Connection) {
	m.editConnection(c)
	if m.form.engine == engDocumentDB {
		m.form.set("auth", "docdb-iam")
		m.form.focus("database")
		m.status = docdbLoginStatus
		return
	}
	m.form.set("auth", "postgres-iam")
	m.form.focus("database")
	if c.Database != "" {
		m.form.focus("dbUser")
	}
	m.status = "Enter the database name and database username. Ctrl+S saves login settings; an administrator must configure IAM access first."
}

func (m *Model) formKey(k tea.KeyMsg) tea.Cmd {
	f := m.form
	key := k.String()
	if key == "ctrl+t" {
		return m.toggleTheme()
	}
	n := len(f.fields)
	if f.kind == "activation" {
		n++
	}
	if key == "esc" {
		m.saveDraft()
		m.form = nil
		if f.kind != "port" {
			m.review = nil
		}
		m.scroll = 0
		return nil
	}
	if key == "tab" || key == "down" {
		old := f.index
		f.index = (f.index + 1) % n
		m.leaveField(old)
		return nil
	}
	if key == "up" {
		old := f.index
		f.index = (f.index + n - 1) % n
		m.leaveField(old)
		return nil
	}
	if f.kind == "activation" && f.index == len(f.fields) {
		if key == " " {
			m.review.acknowledged = !m.review.acknowledged
		}
		if key == "enter" {
			return m.activate()
		}
		return nil
	}
	if key == "ctrl+s" && f.original != nil {
		return m.submitForm()
	}
	if key == "enter" {
		if f.kind == "activation" {
			f.index++
			return nil
		}
		if f.index+1 < len(f.fields) {
			f.index++
			m.leaveField(f.index - 1)
			return nil
		}
		return m.submitForm()
	}
	return m.editInput(k)
}
func (m *Model) submitForm() tea.Cmd {
	f := m.form
	switch f.kind {
	case "port":
		p, e := ValidatePort(f.get("localPort"))
		if e != nil {
			m.status = e.Error()
			return nil
		}
		m.review.connection.LocalPort = p
		m.form = nil
	case "ec2", "db":
		p, ok := m.profile()
		if !ok || p.Error != "" {
			m.status = "Select an available profile first."
			return nil
		}
		c := domain.Connection{Kind: f.kind, Name: f.get("name"), Partition: p.Partition, AccountID: p.AccountID}
		if f.original != nil {
			c = *f.original
			c.Name = f.get("name")
		}
		if f.kind == "ec2" {
			c.InstanceID = f.get("instance")
			c.Region = f.get("region")
			c.DocumentName = f.get("document")
		} else if msg := readDatabaseForm(f, p.AccountID, &c); msg != "" {
			m.status = msg
			return nil
		}
		if c.Name == "" || c.Region == "" || (c.Kind == "ec2" && c.InstanceID == "") || (c.Kind == "db" && (c.Endpoint == "" || c.RelayInstanceID == "" || c.RelayRegion == "" || c.DBPort < 1 || c.DBPort > 65535 || c.LocalPort < 1024 || c.LocalPort > 65535)) {
			m.status = "Complete all fields; local ports must be 1024–65535 and DB ports 1–65535."
			return nil
		}
		if f.original != nil && c.ID != "" {
			updater, ok := m.services.Connections.(interface{ Update(domain.Connection) error })
			if !ok {
				m.status = "Bookmark editing unavailable."
				return nil
			}
			return m.operation("Save connection", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, updater.Update(c)
			})
		}
		m.saveDraft()
		m.form = nil
		m.connectionReview(c)
	case "export", "import":
		if m.services.Connections == nil {
			m.status = "Bookmark service unavailable."
			return nil
		}
		path := f.get("path")
		if path == "" {
			m.status = "Enter a file path."
			return nil
		}
		if f.kind == "export" {
			return m.operation("Export bookmarks", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.Connections.Export(path)
			})
		}
		return m.operation("Import preview", func(context.Context) (domain.Run, domain.ImportPlan, error) {
			p, e := m.services.Connections.Preview(path)
			return domain.Run{}, p, e
		})
	case "alias":
		p, _ := m.profile()
		label := f.get("label")
		if label == "" {
			m.status = "Account label cannot be empty."
			return nil
		}
		if m.services.SaveAccountAlias != nil {
			cmd := m.operation("Save account label", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.SaveAccountAlias(p.AccountID, label)
			})
			return func() tea.Msg {
				result := cmd().(resultMsg)
				result.aliasID = p.AccountID
				result.aliasLabel = label
				return result
			}
		}
		m.aliases[p.AccountID] = label
		m.form = nil
		m.status = "Account label updated for this workspace."
	}
	return nil
}

// docdbLoginStatus is shown when l switches a DocumentDB bookmark to IAM login.
const docdbLoginStatus = "DocumentDB IAM login: your role ARN is the user. Enter a database or leave it blank. Ctrl+S saves; an administrator must add the role to the cluster first."

// readDatabaseForm copies a database form into c and returns the status text
// that refuses it, or "". Each form accepts only its own engine's endpoint
// and login modes; DocumentDBMode stays as the shared defensive check.
func readDatabaseForm(f *form, accountID string, c *domain.Connection) string {
	c.Endpoint = strings.ToLower(f.get("endpoint"))
	c.Region = f.get("region")
	c.DBPort, _ = strconv.Atoi(f.get("dbPort"))
	c.RelayInstanceID = f.get("relay")
	c.RelayRegion = f.get("relayRegion")
	c.LocalPort, _ = strconv.Atoi(f.get("localPort"))
	c.DBAccountID = accountID
	c.AuthMode, c.Database, c.CAFile = f.get("auth"), f.get("database"), f.get("ca")
	c.DocumentName = f.get("document")
	if c.AuthMode == "tunnel" {
		c.AuthMode = ""
	}
	if f.engine == engDocumentDB {
		if !connections.DocumentDBEndpoint(c.Endpoint) {
			return "Enter a DocumentDB cluster endpoint (.docdb.amazonaws.com). For other databases choose RDS PostgreSQL."
		}
		if c.AuthMode != "" && c.AuthMode != "docdb-iam" {
			return "Choose tunnel or docdb-iam for authentication."
		}
		// The form has no username field. A tunnel edit keeps a legacy
		// bookmark's username; IAM login uses the role ARN instead.
		if c.DocDBIAM() {
			c.DBUser = ""
		}
	} else {
		if connections.DocumentDBEndpoint(c.Endpoint) {
			return "This is a DocumentDB endpoint. Press Esc, then n, and choose DocumentDB."
		}
		c.DBUser = f.get("dbUser")
		if c.AuthMode != "" && c.AuthMode != "postgres-iam" {
			return "Choose postgres-iam or tunnel for authentication."
		}
		if c.IAMDatabase() && (c.Database == "" || c.DBUser == "") {
			return "PostgreSQL IAM login requires a database name and database username."
		}
	}
	if err := connections.DocumentDBMode(*c); err != nil {
		return err.Error()
	}
	return ""
}

// leaveField runs whenever focus leaves a field, by key or by click. On a
// new PostgreSQL form it points a DocumentDB endpoint to the DocumentDB form;
// it never changes a value.
func (m *Model) leaveField(old int) {
	f := m.form
	if f == nil || f.kind != "db" || f.engine != engPostgres || f.original != nil || f.index == old || old >= len(f.fields) || f.fields[old].id != "endpoint" {
		return
	}
	if connections.DocumentDBEndpoint(f.get("endpoint")) {
		m.status = "DocumentDB endpoint: press Esc and choose DocumentDB for its defaults."
	}
}

// ValidatePort provides inline validation before backend validation and binding.
func ValidatePort(value string) (int, error) {
	p, e := strconv.Atoi(value)
	if e != nil || p < 1024 || p > 65535 {
		return 0, fmt.Errorf("local port must be 1024–65535")
	}
	return p, nil
}

// databaseFields builds the form for engine. Both engines share the first
// seven fields in the same order; only the login fields after them differ.
// The optional tunnel document is last in both.
func databaseFields(engine string, c domain.Connection) []field {
	mode := c.AuthMode
	if mode == "" {
		mode = "tunnel"
	}
	endpoint := "Endpoint (host)"
	if engine == engDocumentDB {
		endpoint = "Cluster endpoint (host)"
	}
	fields := []field{
		{label: "Display name", value: c.Name, id: "name"},
		{label: endpoint, value: c.Endpoint, id: "endpoint"},
		{label: "DB region", value: c.Region, id: "region"},
		{label: "DB port", value: strconv.Itoa(c.DBPort), id: "dbPort"},
		{label: "Relay EC2 ID", value: c.RelayInstanceID, id: "relay"},
		{label: "Relay / SSM region", value: c.RelayRegion, id: "relayRegion"},
		{label: "Local port", value: strconv.Itoa(c.LocalPort), id: "localPort"},
	}
	document := field{label: "Tunnel document (optional)", value: c.DocumentName, id: "document"}
	if engine == engDocumentDB {
		return append(fields,
			field{label: "Auth (tunnel / docdb-iam)", value: mode, id: "auth"},
			field{label: "Database (optional, IAM)", value: c.Database, id: "database"},
			field{label: "CA file (blank: automatic)", value: c.CAFile, id: "ca"},
			document,
		)
	}
	return append(fields,
		field{label: "Auth (postgres-iam / tunnel)", value: mode, id: "auth"},
		field{label: "Database name (IAM)", value: c.Database, id: "database"},
		field{label: "Database username (IAM)", value: c.DBUser, id: "dbUser"},
		field{label: "RDS CA file (blank: automatic)", value: c.CAFile, id: "ca"},
		document,
	)
}

func ec2Fields(c domain.Connection) []field {
	return []field{
		{label: "Display name", value: c.Name, id: "name"},
		{label: "Instance ID", value: c.InstanceID, id: "instance"},
		{label: "Instance region", value: c.Region, id: "region"},
		{label: "Session document (optional)", value: c.DocumentName, id: "document"},
	}
}

// formTitle names a form: database forms by engine, others by kind.
func formTitle(f *form) string {
	if f.kind != "db" || f.engine == "" {
		return "Edit " + f.kind
	}
	engine := "RDS PostgreSQL"
	if f.engine == engDocumentDB {
		engine = "DocumentDB"
	}
	if f.original != nil {
		return "Edit " + engine + " connection"
	}
	return "New " + engine + " connection"
}
