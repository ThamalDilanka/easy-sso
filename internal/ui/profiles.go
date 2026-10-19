package ui

import (
	"reflect"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

// Refreshes never authenticate or discover cloud resources. Definitions come
// only from the configured AWS file; active runs keep their frozen identities.
func (m *Model) refreshProfiles() tea.Cmd {
	if m.services.LoadProfiles == nil {
		return nil
	}
	if m.busy {
		m.profilesQueued = true
		return nil
	}
	if m.profilesLoading {
		return nil
	}
	m.profilesLoading = true
	m.profilesQueued = false
	m.profilesRequest++
	request, load := m.profilesRequest, m.services.LoadProfiles
	return func() tea.Msg {
		profiles, err := load()
		return profilesMsg{request: request, profiles: profiles, err: err}
	}
}

func (m *Model) afterOperation() tea.Cmd {
	if m.profilesQueued {
		return tea.Batch(m.load(), m.refreshProfiles())
	}
	return m.load()
}

func (m *Model) applyProfiles(msg profilesMsg) bool {
	profiles := m.loginProfiles(msg.profiles)
	if msg.err != nil {
		profiles = nil
	}
	profiles = orderedProfiles(profiles)
	changed := !reflect.DeepEqual(m.services.Profiles, profiles)
	if changed {
		selected, hadSelection := m.profile()
		m.services.Profiles = profiles
		m.selectedProfile = -1
		if hadSelection {
			for i, p := range profiles {
				if p.Name == selected.Name {
					m.selectedProfile = i
					break
				}
			}
		}
		current, hasSelection := m.profile()
		identityChanged := hadSelection && (!hasSelection || !reflect.DeepEqual(selected, current))
		activationChanged := false
		if m.review != nil && m.review.kind == "activation" {
			for _, selection := range m.review.selections {
				matches := false
				for _, p := range profiles {
					if reflect.DeepEqual(p, selection.Profile) {
						matches = true
						break
					}
				}
				activationChanged = activationChanged || !matches
			}
		}
		if identityChanged || activationChanged {
			m.selectedRow, m.scroll = 0, 0
			m.form = nil
			if m.review == nil || m.review.kind != "exit" {
				m.review = nil
			}
			m.drafts = make(map[screen]form)
			m.epoch++
			m.status = "AWS config identity changed. Pending forms and approvals cleared; review the current identity. Existing runs retain their identities."
		} else if m.picker {
			m.status = "AWS profile list changed. Select roles again before activation."
		}
		// Index-based picker choices must never move to newly sorted profiles.
		m.picker = false
		m.pickerRow = 0
		m.picked = make(map[int]bool)
		if m.selectedProfile < 0 {
			m.status = "AWS config changed. Select a configured profile explicitly. Existing runs retain their identities."
		}
		if len(profiles) == 0 {
			m.status = "No configured profiles. Bookmark browsing and existing run management remain available."
		}
	}
	if msg.err != nil {
		m.status = "AWS config reload failed: " + msg.err.Error() + ". New profile actions are unavailable until the file is fixed."
	}
	return changed
}

func (m *Model) renewRole(role domain.ManagedRole) {
	for _, p := range m.services.Profiles {
		if p.Name != role.Profile.Name {
			continue
		}
		if p.Error != "" {
			m.status = "Renewal blocked: the current AWS profile is unavailable: " + p.Error
			return
		}
		m.review = &review{kind: "activation", selections: []domain.Selection{{Profile: p, Alias: role.Alias}}}
		m.form = &form{kind: "activation", fields: []field{{label: "Published alias for " + p.Name, value: role.Alias, id: "alias"}}}
		return
	}
	m.status = "Renewal blocked: the source profile is no longer configured. Existing runs retain their identities."
}

// Published credential aliases are output profiles, not SSO login sources.
// Match the owned metadata, never a name prefix alone.
func (m *Model) loginProfiles(profiles []domain.Profile) []domain.Profile {
	out := make([]domain.Profile, 0, len(profiles))
	for _, p := range profiles {
		managed := false
		if p.SSOSession == "" && p.Error == "sso_session is missing from this AWS profile" {
			for _, r := range m.roles {
				if r.Alias == p.Name {
					managed = true
					break
				}
			}
		}
		if !managed {
			out = append(out, p)
		}
	}
	return out
}
