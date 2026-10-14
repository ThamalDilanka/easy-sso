package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
)

type sessionHistory interface {
	DescribeHistory(context.Context, domain.Snapshot, string, []string) ([]domain.SessionMetadata, error)
}

// Reconcile checks recovered records without opening or terminating sessions.
// It never adopts a saved PID or retains the new credentials on a recovered run.
// UI refreshes can overlap; skip them rather than queueing AWS requests.
func (m *Manager) Reconcile(ctx context.Context) {
	if m.options.RecoveryCredentials == nil || !m.dbOpen.TryLock() {
		return
	}
	defer m.dbOpen.Unlock()
	m.mu.Lock()
	closing := m.closing
	m.mu.Unlock()
	if closing {
		return
	}
	for _, r := range m.List() {
		if ctx.Err() != nil {
			return
		}
		en, _ := m.find(r.ID)
		en.mu.Lock()
		check := en.recovered && !recoveryComplete(en.run) && !time.Now().Before(en.recoveryAfter)
		en.mu.Unlock()
		if check {
			_ = m.recoverTunnel(ctx, en, false)
		}
	}
}

// Caller serializes with dbOpen. Verification always uses the original profile,
// with a newly validated snapshot. The selected UI account cannot retarget it.
func (m *Manager) recoverTunnel(ctx context.Context, en *entry, stop bool) error {
	en.mu.Lock()
	r := copyRun(en.run)
	closedLocally := en.closedLocally
	en.recoveryAfter = time.Now().Add(30 * time.Second)
	en.mu.Unlock()
	if recoveryComplete(r) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fail := func(state, message string) error {
		en.mu.Lock()
		en.run.State, en.run.Error = state, message
		en.mu.Unlock()
		// Recovery failures are local observations, not updates to a live owner's file.
		return errors.New(message)
	}
	role, snapshot, err := m.options.RecoveryCredentials(r.Alias)
	if err != nil || (role.Status != "Active" && role.Status != "active") || awscli.ValidateSnapshot(snapshot) != nil || !role.Expiration.Equal(snapshot.Expiration) {
		en.mu.Lock()
		en.recoveryAfter = time.Time{}
		en.mu.Unlock()
		return fail("Needs login", fmt.Sprintf("Log in to profile [%s] to check this saved tunnel; recovery uses its original account and role.", r.Profile.Name))
	}
	if role.Profile.Name != r.Profile.Name || role.Profile.AccountID != r.Profile.AccountID || role.Profile.Partition != r.Profile.Partition || role.Profile.Role != r.Profile.Role || role.Profile.SSOSession != r.Profile.SSOSession {
		return fail("Check failed", "The saved profile now refers to a different account or role; restore its original identity before checking this tunnel.")
	}
	identity, err := m.cli.Identity(ctx, snapshot, region(r.Connection))
	if err != nil {
		return fail("Check failed", fmt.Sprintf("Cannot verify profile [%s] with AWS; check its login and network, then Recheck (r).", r.Profile.Name))
	}
	if profiles.ValidateIdentity(r.Profile, identity) != nil || identity != role.Identity {
		return fail("Check failed", "AWS identity does not match the original tunnel account and role; no session was changed.")
	}
	filters := recoveryFilters(r)
	active, err := m.cli.Describe(ctx, snapshot, region(r.Connection), filters)
	if err != nil {
		return fail("Check failed", fmt.Sprintf("Cannot read SSM sessions using profile [%s] in %s. Check ssm:DescribeSessions permission and network, then Recheck (r).", r.Profile.Name, region(r.Connection)))
	}
	match, err := recoveryMatch(r, active)
	if err != nil {
		return fail("Check failed", err.Error())
	}
	if match != nil {
		en.mu.Lock()
		en.run.SessionID = match.SessionID
		en.run.RemoteEnded = false
		en.mu.Unlock()
		if !stop {
			en.mu.Lock()
			en.run.State = "Remote active"
			en.run.Error = "AWS confirms this SSM session is active. Stop (d) closes this exact session; its local terminal cannot be restored after restart."
			en.mu.Unlock()
			return nil
		}
		if err := m.cli.Terminate(ctx, snapshot, region(r.Connection), match.SessionID); err != nil {
			return fail("Remote active", fmt.Sprintf("Could not stop the verified SSM session using profile [%s]. Check ssm:TerminateSession permission and network, then retry Stop.", r.Profile.Name))
		}
		// TerminateSession acknowledgement alone is not proof of final termination.
		for attempt := 0; attempt < 10; attempt++ {
			active, err = m.cli.Describe(ctx, snapshot, region(r.Connection), filters)
			if err != nil {
				return fail("Stopping", "SSM termination requested; checking the final state failed. Recheck (r) to confirm.")
			}
			match, err = recoveryMatch(r, active)
			if err != nil {
				return fail("Check failed", err.Error())
			}
			if match == nil {
				break
			}
			if err = pause(ctx, 250*time.Millisecond); err != nil {
				break
			}
		}
		if match != nil {
			return fail("Stopping", "SSM termination requested; AWS still reports the session active. Recheck (r) shortly.")
		}
	}
	// No active match. History confirms termination when retained; after retention
	// expires, a complete successful active-session query still proves no active match.
	history, ok := m.cli.(sessionHistory)
	if !ok {
		return fail("Check failed", "Session history lookup is unavailable; update easy-sso to complete recovery.")
	}
	past, err := history.DescribeHistory(ctx, snapshot, region(r.Connection), filters)
	if err != nil {
		return fail("Check failed", fmt.Sprintf("Cannot read SSM history using profile [%s]. Check ssm:DescribeSessions permission and network, then Recheck (r).", r.Profile.Name))
	}
	old, err := recoveryMatch(r, past)
	if err != nil {
		return fail("Check failed", err.Error())
	}
	if old != nil && old.Status != "Terminated" && old.Status != "Failed" {
		return fail("Checking", "SSM has not yet reported a final session state. Recheck (r) shortly.")
	}
	// Completed local records remain complete even if a newer tunnel has reused
	// the port. Otherwise, bind only to prove absence; never adopt its listener.
	if !closedLocally {
		address := net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Connection.LocalPort))
		attempts := 1
		if stop {
			attempts = 20
		}
		free := false
		for i := 0; i < attempts; i++ {
			listener, err := net.Listen("tcp", address)
			if err == nil {
				_ = listener.Close()
				free = true
				break
			}
			if i+1 < attempts {
				if pause(ctx, 100*time.Millisecond) != nil {
					break
				}
			}
		}
		if !free {
			en.mu.Lock()
			en.run.RemoteEnded = true
			en.mu.Unlock()
			return fail("Port in use", fmt.Sprintf("No active matching SSM session remains, but local port %d is still in use. Let its forwarding client exit or close the owning app, then Recheck (r).", r.Connection.LocalPort))
		}
	}
	en.mu.Lock()
	en.run.State, en.run.RemoteEnded = "Stopped", true
	en.run.Error = ""
	if old != nil {
		en.run.SessionID = old.SessionID
	}
	if old == nil {
		en.run.Error = "No active matching SSM session and no local listener; no retained AWS history was found."
	}
	en.mu.Unlock()
	m.persistTunnel(en)
	return nil
}

func recoveryFilters(r domain.Run) []string {
	filters := []map[string]string{{"key": "Target", "value": target(r.Connection)}, {"key": "Owner", "value": r.Identity.ARN}, {"key": "InvokedAfter", "value": r.StartedAt.Add(-30 * time.Second).Format(time.RFC3339)}, {"key": "InvokedBefore", "value": r.StartedAt.Add(5 * time.Minute).Format(time.RFC3339)}}
	if r.SessionID != "" {
		filters = []map[string]string{{"key": "SessionId", "value": r.SessionID}}
	}
	encoded, _ := json.Marshal(filters)
	return []string{string(encoded)}
}

func recoveryMatch(r domain.Run, items []domain.SessionMetadata) (*domain.SessionMetadata, error) {
	var result *domain.SessionMetadata
	for _, v := range items {
		if r.SessionID != "" {
			if v.SessionID != r.SessionID {
				continue
			}
		} else if v.Reason != "easy-sso:"+r.ID {
			continue
		}
		if v.SessionID == "" || v.Reason != "easy-sso:"+r.ID || v.Owner != r.Identity.ARN || v.Target != target(r.Connection) || v.StartDate.Before(r.StartedAt.Add(-30*time.Second)) || v.StartDate.After(r.StartedAt.Add(5*time.Minute)) {
			return nil, errors.New("SSM session metadata does not match this saved run; no session was changed.")
		}
		if result != nil {
			return nil, errors.New("Multiple SSM sessions match this saved run; no session was changed.")
		}
		copy := v
		result = &copy
	}
	return result, nil
}

func recoveryComplete(r domain.Run) bool {
	return r.RemoteEnded && (r.State == "Stopped" || r.State == "Ended" || r.State == "Failed")
}
