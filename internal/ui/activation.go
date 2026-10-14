package ui

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
)

func (m *Model) startActivation() {
	if len(m.services.Profiles) == 0 {
		return
	}
	m.picker = true
	m.pickerRow = max(0, m.selectedProfile)
	m.picked = map[int]bool{m.pickerRow: true}
}
func (m *Model) activationReview() {
	var selections []domain.Selection
	var fields []field
	for i, p := range m.services.Profiles {
		if m.picked[i] && p.Error == "" {
			alias := "easy-sso-managed-" + p.Name
			for _, r := range m.roles {
				if sameProfile(r.Profile, p) {
					alias = r.Alias
				}
			}
			selections = append(selections, domain.Selection{Profile: p, Alias: alias})
			fields = append(fields, field{"Published alias for " + p.Name, alias})
		}
	}
	if len(selections) == 0 {
		m.status = "Select at least one available role."
		return
	}
	m.picker = false
	m.review = &review{kind: "activation", selections: selections}
	m.form = &form{kind: "activation", fields: fields}
}
func (m *Model) activate() tea.Cmd {
	if !m.review.acknowledged {
		m.status = "Acknowledge AWS CLI SSO cleanup before activation."
		return nil
	}
	if m.services.Credentials == nil || m.services.Prerequisites.CLIPath == "" {
		m.status = "AWS CLI prerequisite unavailable. See diagnostics."
		return nil
	}
	selections := append([]domain.Selection(nil), m.review.selections...)
	for i := range selections {
		selections[i].Alias = strings.TrimSpace(m.form.fields[i].value)
		if selections[i].Alias == "" {
			m.status = "Published aliases cannot be empty."
			return nil
		}
	}
	return m.operation("Activate roles", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		_, e := m.services.Credentials.Activate(ctx, selections, true)
		return domain.Run{}, domain.ImportPlan{}, e
	})
}
func (m *Model) loginSelected() tea.Cmd {
	p, ok := m.profile()
	if !ok || p.Error != "" {
		m.status = "Select an available profile to log in."
		return nil
	}
	m.startActivation()
	m.activationReview()
	m.review.acknowledged = true
	cmd := m.activate()
	if cmd != nil {
		m.form = nil
		m.review = nil
	}
	return cmd
}
