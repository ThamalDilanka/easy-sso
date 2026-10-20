package sessions

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"sync"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
)

type databaseSigner interface {
	DatabaseToken(context.Context, domain.Snapshot, domain.Connection) (string, error)
}
type databaseTerminal interface {
	LaunchClient(context.Context, string, *exec.Cmd, func([]byte) []byte) (<-chan struct{}, error)
	Has(string) bool
	Exited(string) bool
	Close(string) error
}

func sameRoute(a, b domain.Connection) bool {
	return a.Kind == "db" && b.Kind == "db" && a.Partition == b.Partition && a.AccountID == b.AccountID && a.Region == b.Region && a.Endpoint == b.Endpoint && a.DBPort == b.DBPort && a.LocalPort == b.LocalPort && a.RelayInstanceID == b.RelayInstanceID && a.RelayRegion == b.RelayRegion
}

// Never infer ownership from an open port, a saved PID, or a recovered run.
// Only one client attaches to a tunnel; repeated Connect focuses that client.
func (m *Manager) reuseDatabase(ctx context.Context, c domain.Connection, role domain.ManagedRole, s domain.Snapshot, identity domain.Identity, adapter terminal.Adapter, path, ca string) (domain.Run, bool, error) {
	for _, r := range m.List() {
		if r.Connection.Kind != "db" || r.Connection.LocalPort != c.LocalPort || r.State == "Stopped" || r.State == "Ended" || r.State == "Failed" {
			continue
		}
		en, _ := m.find(r.ID)
		en.mu.Lock()
		child, snapshot, clientDone := en.child, en.snapshot, en.clientDone
		en.mu.Unlock()
		// Historical registrations do not reserve local ports. Skip them when
		// looking for a reusable owned tunnel; open's bind check still rejects
		// any real external listener before starting a new forwarding process.
		if child == nil {
			continue
		}
		if !sameRoute(r.Connection, c) || r.Identity != identity || !reflect.DeepEqual(r.Profile, role.Profile) || r.Alias != role.Alias || snapshot != s {
			return domain.Run{}, false, errors.New("local port belongs to a different or unverified session; stop it in Tunnels or choose another local port")
		}
		ready, err := m.options.Probe(c.LocalPort, child)
		if err != nil || !ready {
			return domain.Run{}, false, errors.New("existing tunnel listener ownership is unconfirmed; recheck or stop it in Tunnels")
		}
		if !c.DatabaseClient() {
			return r, true, nil
		}
		term := adapter.(databaseTerminal)
		if clientDone != nil && term.Has(r.ID) && !term.Exited(r.ID) {
			if r.Connection.AuthMode == c.AuthMode && r.Connection.Database == c.Database && r.Connection.DBUser == c.DBUser && r.Connection.CAFile == c.CAFile && r.State == "Connected" {
				return r, true, nil
			}
			return domain.Run{}, false, errors.New("this tunnel already has a database client; close its terminal or choose another local port")
		}
		if term.Has(r.ID) {
			return domain.Run{}, false, errors.New("close the ended database terminal tab before reconnecting")
		}
		en.mu.Lock()
		en.run.Connection = c
		en.saved, en.keepTunnel, en.clientClosing = false, true, false
		en.mu.Unlock()
		err = m.openDatabaseClient(ctx, en, term, path, ca)
		en.mu.Lock()
		r = copyRun(en.run)
		en.mu.Unlock()
		return r, true, err
	}
	return domain.Run{}, false, nil
}

func (m *Manager) openDatabaseClient(ctx context.Context, en *entry, term databaseTerminal, path, ca string) error {
	en.mu.Lock()
	r, snapshot, recovered := copyRun(en.run), en.snapshot, en.recovered
	en.mu.Unlock()
	fail := func(message string) error {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		en.mu.Lock()
		keep := en.keepTunnel
		en.mu.Unlock()
		state := "Failed"
		if !keep {
			if err := m.stop(cleanup, r.ID); err != nil {
				message += " " + err.Error() + "."
				en.mu.Lock()
				if en.run.State == "Unknown" {
					state = "Unknown"
				}
				en.mu.Unlock()
			}
		}
		if keep {
			state = "Listening"
			message += " Existing tunnel remains open."
		}
		m.set(en, state, message)
		return errors.New(message)
	}
	var cmd *exec.Cmd
	var output *database.Output
	// removeHome deletes mongosh's private HOME once the client has exited.
	removeHome := func() {}
	if r.Connection.DocDBIAM() {
		// Only this run's own frozen snapshot may log in; a recovered record has none.
		if recovered || snapshot.AccessKeyID == "" {
			return fail("DocumentDB IAM login needs this run's own credentials; open the connection again from Databases")
		}
		if err := m.verifyTLS(ctx, r.Connection.LocalPort, r.Connection.Endpoint, ca); err != nil {
			return fail(err.Error())
		}
		home, cleanup, err := database.MongoHome(m.stateDir, r.ID)
		if err != nil {
			return fail(err.Error())
		}
		removeHome = sync.OnceFunc(cleanup)
		if cmd, output, err = database.MongoCommand(path, ca, home, r.ID, snapshot, r.Connection); err != nil {
			removeHome()
			return fail(err.Error())
		}
	} else {
		token, err := m.cli.(databaseSigner).DatabaseToken(ctx, snapshot, r.Connection)
		if err != nil {
			return fail("IAM token generation failed; renew the selected role and retry")
		}
		cmd, output = database.Command(path, ca, r.ID, token, r.Connection)
	}
	m.set(en, "Connecting", "")
	done, err := term.LaunchClient(ctx, r.ID, cmd, output.Filter)
	if err != nil {
		removeHome()
		return fail("Database terminal could not start; check the database client and close any ended tab")
	}
	if r.Connection.DocDBIAM() {
		// Shutdown waits for this once every client has closed, so a clean exit leaves no home.
		m.dbWorkers.Add(1)
		go func() {
			defer m.dbWorkers.Done()
			<-done
			removeHome()
		}()
	}
	en.mu.Lock()
	en.clientDone = done
	en.handle = r.ID
	en.mu.Unlock()
	login, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	select {
	case <-done:
		// Preserve failed client output for diagnosis. Stop only the new tunnel.
		en.mu.Lock()
		en.clientDone = nil
		en.mu.Unlock()
		removeHome()
		return fail(output.Failure())
	case <-login.Done():
		en.mu.Lock()
		en.clientClosing = true
		en.mu.Unlock()
		if err := term.Close(r.ID); err != nil {
			return errors.New("database startup interrupted; local client cleanup is unconfirmed; close its terminal")
		}
		en.mu.Lock()
		en.clientDone = nil
		en.mu.Unlock()
		removeHome()
		return fail("Database login cancelled or timed out; check relay connectivity and database IAM setup")
	case <-output.Ready:
	}
	if term.Exited(r.ID) {
		en.mu.Lock()
		en.clientDone = nil
		en.mu.Unlock()
		removeHome()
		return fail(output.Failure())
	}
	m.set(en, "Connected", "")
	err = m.save(en)
	m.dbWorkers.Add(1)
	go func() {
		defer m.dbWorkers.Done()
		<-done
		m.dbOpen.Lock()
		defer m.dbOpen.Unlock()
		en.mu.Lock()
		if en.clientClosing || en.clientDone != done {
			en.mu.Unlock()
			return
		}
		en.clientDone = nil
		keep := en.keepTunnel
		en.mu.Unlock()
		if keep {
			en.mu.Lock()
			state := en.run.State
			en.mu.Unlock()
			if state != "Stopped" && state != "Ended" && state != "Failed" {
				m.set(en, "Listening", "Database terminal ended; existing tunnel remains open")
			}
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := m.stop(cleanup, r.ID)
		message := ""
		if err != nil {
			message = "Database terminal ended; remote tunnel termination is unconfirmed"
		}
		state := "Ended"
		en.mu.Lock()
		if en.run.State == "Unknown" {
			state = "Unknown"
			message = "Database terminal ended; owned tunnel cleanup is unconfirmed. Stop it from Tunnels."
		}
		en.mu.Unlock()
		m.set(en, state, message)
	}()
	return err
}

func (m *Manager) closeDatabaseClient(ctx context.Context, en *entry) error {
	en.mu.Lock()
	id, keep := en.run.ID, en.keepTunnel
	en.clientClosing = true
	en.mu.Unlock()
	term := m.options.Terminal.(databaseTerminal)
	if err := term.Close(id); err != nil {
		return err
	}
	en.mu.Lock()
	en.clientDone = nil
	en.mu.Unlock()
	if keep {
		en.mu.Lock()
		state := en.run.State
		en.mu.Unlock()
		if state != "Stopped" && state != "Ended" && state != "Failed" {
			m.set(en, "Listening", "Database terminal closed; existing tunnel remains open")
		}
		return nil
	}
	return m.stop(ctx, id)
}
