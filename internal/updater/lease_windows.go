package updater

import (
	"errors"
	"os"
)

type Lease struct {
	file      *os.File
	path      string
	exclusive bool
	rename    func(string, string) error
}

func Acquire(path string, exclusive bool) (*Lease, error) {
	if exclusive {
		return nil, errors.New("Windows self-replacement is unavailable; close all easy-sso apps and helpers, then verify and replace the release manually")
	}
	return &Lease{}, nil
}
func (l *Lease) current() error { return errors.New("Windows self-replacement is unavailable") }
func (l *Lease) Close()         {}
