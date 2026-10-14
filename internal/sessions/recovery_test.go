package sessions

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/ipc"
)

type recoveryCLI struct {
	*fakeCLI
	history []domain.SessionMetadata
}

func (c *recoveryCLI) DescribeHistory(context.Context, domain.Snapshot, string, []string) ([]domain.SessionMetadata, error) {
	return c.history, nil
}

func TestRecoveryRecheckResolvesOldTunnel(t *testing.T) {
	c, role, snapshot, base := fixture(t)
	run := domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: role.Identity, Expiration: snapshot.Expiration, StartedAt: time.Now().Add(-time.Minute), SessionID: "old-session", State: "Unknown"}
	cli := &recoveryCLI{fakeCLI: base, history: []domain.SessionMetadata{{SessionID: run.SessionID, Target: c.RelayInstanceID, Owner: run.Identity.ARN, Reason: "easy-sso:" + run.ID, StartDate: run.StartedAt, Status: "Terminated"}}}
	dir := t.TempDir()
	if err := ipc.WriteJSON(filepath.Join(dir, "runtime", run.ID+".json"), registration{Run: run}); err != nil {
		t.Fatal(err)
	}
	m, err := NewWithOptions(cli, dir, "/fake", &bookmarks{}, Options{RecoveryCredentials: func(alias string) (domain.ManagedRole, domain.Snapshot, error) {
		if alias != role.Alias {
			t.Fatal("recovery selected the wrong role")
		}
		return role, snapshot, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown(context.Background())
	if err := m.Recheck(context.Background(), run.ID); err != nil {
		t.Fatal("old tunnel still cannot be checked:", err)
	}
	got := m.List()[0]
	if got.State != "Stopped" || !got.RemoteEnded {
		t.Fatalf("old tunnel remains unresolved: %+v", got)
	}
	if m.OwnsTunnel(run.ID) || len(base.terminations) != 0 {
		t.Fatal("read-only recovery adopted or stopped a process")
	}
}

func recoveredFixture(t *testing.T, state string) (*Manager, *recoveryCLI, domain.Run, *domain.ManagedRole, *domain.Snapshot) {
	t.Helper()
	c, role, snapshot, base := fixture(t)
	run := domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: role.Identity, Expiration: snapshot.Expiration, StartedAt: time.Now().Add(-time.Minute), SessionID: "saved-session", State: state}
	cli := &recoveryCLI{fakeCLI: base}
	dir := t.TempDir()
	if err := ipc.WriteJSON(filepath.Join(dir, "runtime", run.ID+".json"), registration{Run: run}); err != nil {
		t.Fatal(err)
	}
	m, err := NewWithOptions(cli, dir, "/fake", &bookmarks{}, Options{RecoveryCredentials: func(alias string) (domain.ManagedRole, domain.Snapshot, error) {
		if alias != run.Alias {
			t.Error("wrong recovery alias")
		}
		return role, snapshot, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, cli, run, &role, &snapshot
}
func recoveryMetadata(r domain.Run) domain.SessionMetadata {
	return domain.SessionMetadata{SessionID: r.SessionID, Target: target(r.Connection), Owner: r.Identity.ARN, Reason: "easy-sso:" + r.ID, StartDate: r.StartedAt, Status: "Connected"}
}

func (c *recoveryCLI) Terminate(ctx context.Context, s domain.Snapshot, region, id string) error {
	if err := c.fakeCLI.Terminate(ctx, s, region, id); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var active []domain.SessionMetadata
	for _, item := range c.metadata {
		if item.SessionID == id {
			item.Status = "Terminated"
			c.history = append(c.history, item)
		} else {
			active = append(active, item)
		}
	}
	c.metadata = active
	return nil
}

func TestRecoveryStopsOnlyExactSessionWithRenewedOriginalRole(t *testing.T) {
	m, cli, run, role, snapshot := recoveredFixture(t, "Listening")
	saved := recoveryMetadata(run)
	other := saved
	other.SessionID = "unrelated-session"
	other.Reason = "easy-sso:another-run"
	cli.metadata = []domain.SessionMetadata{other, saved}
	// The renewed login is a different STS session for the same original role.
	role.Identity.ARN = "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_ReadOnly_0123456789abcdef/renewed"
	role.Identity.UserID = "id:renewed"
	cli.identity = role.Identity
	snapshot.AccessKeyID = "ASIANEW"
	snapshot.Expiration = time.Now().Add(2 * time.Hour)
	role.Expiration = snapshot.Expiration
	m.Reconcile(context.Background())
	if got := m.List()[0]; got.State != "Remote active" || got.Identity != run.Identity || got.Expiration != run.Expiration {
		t.Fatalf("recovery changed original identity: %+v", got)
	}
	if len(cli.terminations) != 0 || m.OwnsTunnel(run.ID) {
		t.Fatal("automatic recovery stopped or adopted a live session")
	}
	if err := m.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if len(cli.terminations) != 1 || cli.terminations[0] != run.SessionID || len(cli.metadata) != 1 || cli.metadata[0].SessionID != other.SessionID {
		t.Fatal("stop was not scoped to exact verified session")
	}
	got := m.List()[0]
	if got.State != "Stopped" || !got.RemoteEnded {
		t.Fatal("stop not confirmed", got)
	}
	en, _ := m.find(run.ID)
	if en.snapshot.AccessKeyID != "" || en.child != nil {
		t.Fatal("recovered run retained credentials or process ownership")
	}
}

func TestRecoveryBlockedCasesNeverTerminate(t *testing.T) {
	for _, scenario := range []string{"expired", "wrong role", "wrong account", "owner", "reason", "target", "time", "ambiguous", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			m, cli, run, role, snapshot := recoveredFixture(t, "Unknown")
			item := recoveryMetadata(run)
			switch scenario {
			case "expired":
				snapshot.Expiration = time.Now().Add(-time.Minute)
			case "wrong role":
				role.Profile.Role = "AdministratorAccess"
			case "wrong account":
				role.Profile.AccountID = "999999999999"
			case "owner":
				item.Owner += "-wrong"
			case "reason":
				item.Reason += "-wrong"
			case "target":
				item.Target = "i-fffffffffffffffff"
			case "time":
				item.StartDate = run.StartedAt.Add(-time.Hour)
			case "permission":
				cli.describeErr = errors.New("AccessDenied secret must not be displayed")
			}
			cli.metadata = []domain.SessionMetadata{item}
			if scenario == "ambiguous" {
				cli.metadata = append(cli.metadata, item)
			}
			if err := m.Stop(context.Background(), run.ID); err == nil {
				t.Fatal("unsafe recovery succeeded")
			}
			got := m.List()[0]
			want := "Check failed"
			if scenario == "expired" {
				want = "Needs login"
			}
			if got.State != want || got.Error == "" || strings.Contains(got.Error, "secret") || len(cli.terminations) != 0 || m.OwnsTunnel(run.ID) {
				t.Fatalf("unsafe or unclear result: %+v", got)
			}
		})
	}
}

func TestRecoveryAbsentSessionAndOldStopWarningResolveAutomatically(t *testing.T) {
	for _, state := range []string{"Unknown", "Stopped", "Failed"} {
		m, cli, run, _, _ := recoveredFixture(t, state)
		// No active session and no retained history; local port is free.
		m.Reconcile(context.Background())
		got := m.List()[0]
		if got.State != "Stopped" || !got.RemoteEnded || !strings.Contains(got.Error, "No active") {
			t.Fatal("did not resolve absent session", got)
		}
		var saved registration
		if err := ipc.ReadJSON(filepath.Join(m.stateDir, "runtime", run.ID+".json"), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Run.State != "Stopped" || !saved.Run.RemoteEnded || len(cli.terminations) != 0 {
			t.Fatal("resolution not persisted safely")
		}
	}
}

func TestRecoveryBusyPortNeverKillsItsListener(t *testing.T) {
	m, cli, run, _, _ := recoveredFixture(t, "Unknown")
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(run.Connection.LocalPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := m.Recheck(context.Background(), run.ID); err == nil {
		t.Fatal("occupied port reported closed")
	}
	if got := m.List()[0]; got.State != "Port in use" || !got.RemoteEnded {
		t.Fatal("port conflict not explained", got)
	}
	conn, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal("unrelated listener was stopped", err)
	}
	conn.Close()
	l.Close()
	if err := m.Recheck(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].State != "Stopped" || len(cli.terminations) != 0 {
		t.Fatal("port release did not resolve history")
	}
}

func TestRecoveryWithoutSavedSessionIDUsesExactRunBinding(t *testing.T) {
	m, cli, run, _, _ := recoveredFixture(t, "Unknown")
	item := recoveryMetadata(run)
	en, _ := m.find(run.ID)
	en.run.SessionID = ""
	cli.metadata = []domain.SessionMetadata{item}
	if err := m.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if len(cli.terminations) != 1 || cli.terminations[0] != item.SessionID {
		t.Fatal("could not correlate original run")
	}
}

func TestRecoveryConfirmedHistoryNeedsNoCredentials(t *testing.T) {
	for _, state := range []string{"Stopped", "Ended", "Failed"} {
		m, _, run, _, snapshot := recoveredFixture(t, state)
		en, _ := m.find(run.ID)
		en.run.RemoteEnded = true
		snapshot.Expiration = time.Now().Add(-time.Hour)
		m.Reconcile(context.Background())
		if err := m.Stop(context.Background(), run.ID); err != nil {
			t.Fatal(err)
		}
		if m.List()[0].State != state {
			t.Fatal("completed history was changed")
		}
	}
}

func TestRecoveryRetriesAfterLoginAndThrottlesChecks(t *testing.T) {
	m, cli, run, role, snapshot := recoveredFixture(t, "Unknown")
	valid := *snapshot
	snapshot.Expiration = time.Now().Add(-time.Minute)
	m.Reconcile(context.Background())
	if m.List()[0].State != "Needs login" {
		t.Fatal("missing actionable login state")
	}
	*snapshot = valid
	role.Expiration = valid.Expiration
	cli.metadata = []domain.SessionMetadata{recoveryMetadata(run)}
	m.Reconcile(context.Background())
	if m.List()[0].State != "Remote active" {
		t.Fatal("login did not trigger recovery")
	}
	cli.describeErr = errors.New("temporary AWS failure")
	m.Reconcile(context.Background())
	if m.List()[0].State != "Remote active" {
		t.Fatal("automatic recovery did not throttle AWS requests")
	}
	if err := m.Recheck(context.Background(), run.ID); err == nil || m.List()[0].State != "Check failed" {
		t.Fatal("explicit recheck did not bypass throttle")
	}
}

func TestRecoveryCompletedLocalTunnelIgnoresReusedPort(t *testing.T) {
	m, _, run, _, _ := recoveredFixture(t, "Stopped")
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(run.Connection.LocalPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := m.Recheck(context.Background(), run.ID); err != nil {
		t.Fatal("new listener was attributed to old completed tunnel", err)
	}
	if m.List()[0].State != "Stopped" {
		t.Fatal("completed tunnel was revived")
	}
}
