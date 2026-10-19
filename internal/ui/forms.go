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

func (m *Model) newForm() {
	if draft, ok := m.drafts[m.screen]; ok {
		draft.fields = append([]field(nil), draft.fields...)
		m.form = &draft
		return
	}
	p, _ := m.profile()
	kind := "ec2"
	fields := []field{{"Display name", ""}, {"Instance ID", ""}, {"Instance region", p.Region}, {"Session document (optional)", ""}}
	if m.screen == databases {
		kind = "db"
		fields = databaseFields(domain.Connection{Region: p.Region, RelayRegion: p.Region, DBPort: 5432, LocalPort: 15432, AuthMode: "postgres-iam"})
	}
	m.form = &form{kind: kind, fields: fields}
}
func (m *Model) editConnection(c domain.Connection) {
	fields := []field{{"Display name", c.Name}, {"Instance ID", c.InstanceID}, {"Instance region", c.Region}, {"Session document (optional)", c.DocumentName}}
	if c.Kind == "db" {
		fields = databaseFields(c)
	}
	m.form = &form{kind: c.Kind, fields: fields}
	if c.ID != "" {
		m.form.original = &c
	}
	m.scroll = 0
}

func (m *Model) setupDatabaseLogin(c domain.Connection) {
	if isDocumentDB(c) {
		m.status = "IAM login setup is for PostgreSQL; DocumentDB uses your own user in the client."
		return
	}
	m.editConnection(c)
	m.form.fields[7].value = "postgres-iam"
	m.form.index = 8
	if c.Database != "" {
		m.form.index = 9
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
		p, e := ValidatePort(strings.TrimSpace(f.fields[0].value))
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
		v := func(i int) string { return strings.TrimSpace(f.fields[i].value) }
		c := domain.Connection{Kind: f.kind, Name: v(0), Partition: p.Partition, AccountID: p.AccountID}
		if f.original != nil {
			c = *f.original
			c.Name = v(0)
		}
		if f.kind == "ec2" {
			c.InstanceID = v(1)
			c.Region = v(2)
			c.DocumentName = v(3)
		} else {
			c.Endpoint = strings.ToLower(v(1))
			c.Region = v(2)
			c.DBPort, _ = strconv.Atoi(v(3))
			c.RelayInstanceID = v(4)
			c.RelayRegion = v(5)
			c.LocalPort, _ = strconv.Atoi(v(6))
			c.DBAccountID = p.AccountID
			if len(f.fields) > 7 {
				c.AuthMode, c.Database, c.DBUser, c.CAFile = v(7), v(8), v(9), v(10)
				if c.AuthMode == "tunnel" {
					c.AuthMode = ""
				}
				if c.AuthMode != "" && c.AuthMode != "postgres-iam" {
					m.status = "Choose postgres-iam or tunnel for authentication."
					return nil
				}
				if err := connections.DocumentDBMode(c); err != nil {
					m.status = err.Error()
					return nil
				}
				if c.IAMDatabase() && (c.Database == "" || c.DBUser == "") {
					m.status = "PostgreSQL IAM login requires a database name and database username."
					return nil
				}
			}
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
		path := strings.TrimSpace(f.fields[0].value)
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
		label := strings.TrimSpace(f.fields[0].value)
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

// leaveField switches a new database form to DocumentDB defaults once focus
// leaves a DocumentDB endpoint. Only values still holding a default change.
func (m *Model) leaveField(old int) {
	f := m.form
	if f == nil || f.kind != "db" || f.original != nil || old != 1 || f.index == old || len(f.fields) < 8 || !connections.DocumentDBEndpoint(strings.TrimSpace(f.fields[1].value)) {
		return
	}
	var changed []string
	for _, d := range []struct {
		index          int
		from, to, name string
	}{{3, "5432", "27017", "DB port 27017"}, {6, "15432", "37017", "local port 37017"}, {7, "postgres-iam", "tunnel", "auth to tunnel"}} {
		if strings.TrimSpace(f.fields[d.index].value) == d.from {
			f.fields[d.index].value = d.to
			changed = append(changed, d.name)
		}
	}
	switch len(changed) {
	case 0:
	case 3:
		m.status = "DocumentDB endpoint: ports set to 27017/37017, auth set to tunnel"
	default:
		m.status = "DocumentDB endpoint: set " + strings.Join(changed, ", ")
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

func databaseFields(c domain.Connection) []field {
	mode := c.AuthMode
	if mode == "" {
		mode = "tunnel"
	}
	return []field{{"Display name", c.Name}, {"Endpoint (host)", c.Endpoint}, {"DB region", c.Region}, {"DB port", strconv.Itoa(c.DBPort)}, {"Relay EC2 ID", c.RelayInstanceID}, {"Relay / SSM region", c.RelayRegion}, {"Local port", strconv.Itoa(c.LocalPort)}, {"Auth (postgres-iam / tunnel)", mode}, {"Database name (IAM)", c.Database}, {"Database username (IAM)", c.DBUser}, {"RDS CA file (blank: automatic)", c.CAFile}}
}
