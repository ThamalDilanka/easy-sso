package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type Settings struct {
	Theme          string            `json:"theme,omitempty"`
	Version        int               `json:"version"`
	AccountAliases map[string]string `json:"accountAliases"`
	// ClientPlaces remembers where to start a database client found both on
	// Windows and in WSL: app "datagrip" or "compass", place "local" or "windows".
	ClientPlaces map[string]string `json:"clientPlaces,omitempty"`
}

func LoadSettings(state string) (Settings, error) {
	s := Settings{Version: 1, AccountAliases: map[string]string{}}
	b, e := Read(filepath.Join(state, "settings.json"), 1024*1024)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	if json.Unmarshal(b, &s) != nil || s.Version != 1 {
		return s, errors.New("invalid local settings")
	}
	if s.AccountAliases == nil {
		s.AccountAliases = map[string]string{}
	}
	for account, label := range s.AccountAliases {
		if err := validateAlias(account, label); err != nil {
			return s, err
		}
	}
	// An unknown client choice is only a preference: drop it, so the user is
	// asked again, instead of refusing to start.
	for app, place := range s.ClientPlaces {
		if validateClientPlace(app, place) != nil {
			delete(s.ClientPlaces, app)
		}
	}
	return s, nil
}
func validateAlias(account, label string) error {
	if len(account) != 12 || strings.IndexFunc(account, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return errors.New("invalid account ID")
	}
	if strings.TrimSpace(label) == "" || len(label) > 100 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return errors.New("account alias must be nonempty text without control characters")
	}
	return nil
}
func SaveAlias(state, account, label string) error {
	if err := validateAlias(account, label); err != nil {
		return err
	}
	return updateSettings(state, func(s *Settings) { s.AccountAliases[account] = label })
}

func validateClientPlace(app, place string) error {
	if app != "datagrip" && app != "compass" {
		return errors.New("invalid database client")
	}
	if place != "local" && place != "windows" {
		return errors.New("invalid database client place")
	}
	return nil
}

// SaveClientPlace remembers where app starts when it is found in both places.
func SaveClientPlace(state, app, place string) error {
	if err := validateClientPlace(app, place); err != nil {
		return err
	}
	return updateSettings(state, func(s *Settings) {
		if s.ClientPlaces == nil {
			s.ClientPlaces = map[string]string{}
		}
		s.ClientPlaces[app] = place
	})
}

func SaveTheme(state, theme string) error {
	if theme != "light" && theme != "dark" {
		return errors.New("invalid theme")
	}
	return updateSettings(state, func(s *Settings) { s.Theme = theme })
}

// Keep the entire read-modify-write transaction under the same storage lock.
func updateSettings(state string, update func(*Settings)) error {
	unlock, err := Lock(filepath.Join(state, "settings.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	s, err := LoadSettings(state)
	if err != nil {
		return err
	}
	update(&s)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(state, "settings.json"), b)
}
