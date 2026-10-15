package domain

import (
	"context"
	"io"
	"time"
)

type Profile struct {
	Name       string   `json:"name"`
	SSOSession string   `json:"ssoSession"`
	AccountID  string   `json:"accountId"`
	Role       string   `json:"role"`
	Partition  string   `json:"partition"`
	Region     string   `json:"region"`
	Chain      []string `json:"chain"`
	DirectSSO  bool     `json:"directSSO"`
	Error      string   `json:"error,omitempty"`
}
type Identity struct {
	Account string
	ARN     string
	UserID  string
}

// Snapshot never belongs in bookmarks, logs, command arguments or metadata files.
type Snapshot struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}
type Selection struct {
	Profile Profile
	Alias   string
}
type ManagedRole struct {
	Alias       string    `json:"alias"`
	Profile     Profile   `json:"profile"`
	Identity    Identity  `json:"identity"`
	Expiration  time.Time `json:"expiration"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
}
type Connection struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	Partition       string     `json:"partition"`
	AccountID       string     `json:"accountId"`
	Region          string     `json:"region"`
	InstanceID      string     `json:"instanceId,omitempty"`
	Endpoint        string     `json:"endpoint,omitempty"`
	AuthMode        string     `json:"authMode,omitempty"`
	Database        string     `json:"database,omitempty"`
	DBUser          string     `json:"dbUser,omitempty"`
	CAFile          string     `json:"caFile,omitempty"`
	DBPort          int        `json:"dbPort,omitempty"`
	LocalPort       int        `json:"localPort,omitempty"`
	RelayInstanceID string     `json:"relayInstanceId,omitempty"`
	RelayRegion     string     `json:"relayRegion,omitempty"`
	DBAccountID     string     `json:"dbAccountId,omitempty"`
	LastUsedProfile string     `json:"lastUsedProfile,omitempty"`
	LastUsedRole    string     `json:"lastUsedRole,omitempty"`
	LastUsedAt      *time.Time `json:"lastUsedAt,omitempty"`
}

// IAMDatabase identifies the PostgreSQL terminal workflow. Empty auth mode is legacy tunnel-only.
func (c Connection) IAMDatabase() bool { return c.Kind == "db" && c.AuthMode == "postgres-iam" }

type Run struct {
	ID          string     `json:"id"`
	Connection  Connection `json:"connection"`
	Profile     Profile    `json:"profile"`
	Alias       string     `json:"alias"`
	Identity    Identity   `json:"identity"`
	Expiration  time.Time  `json:"expiration"`
	SessionID   string     `json:"sessionId,omitempty"`
	State       string     `json:"state"`
	Title       string     `json:"title"`
	StartedAt   time.Time  `json:"startedAt"`
	Error       string     `json:"error,omitempty"`
	RemoteEnded bool       `json:"remoteEnded"`
}
type SessionMetadata struct {
	SessionID, Target, Owner, Reason, Status string
	StartDate                                time.Time
}
type Child interface {
	Wait() error
	Stop() error
}
type CredentialCLI interface {
	Login(context.Context, string) error
	Export(context.Context, Profile) (Snapshot, error)
	Logout(context.Context) error
	Identity(context.Context, Snapshot, string) (Identity, error)
}

type SessionCLI interface {
	Identity(context.Context, Snapshot, string) (Identity, error)
	Start(context.Context, Snapshot, []string, io.Reader, io.Writer, io.Writer) (Child, error)
	Describe(context.Context, Snapshot, string, []string) ([]SessionMetadata, error)
	Terminate(context.Context, Snapshot, string, string) error
}

// CLI is implemented by the official AWS CLI adapter. Callers depend on the
// smaller interface appropriate to their operation.
type CLI interface {
	CredentialCLI
	SessionCLI
}

type CredentialService interface {
	List() ([]ManagedRole, error)
	Activate(context.Context, []Selection, bool) ([]ManagedRole, error)
	Snapshot(string) (ManagedRole, Snapshot, error)
	Remove(string) error
	RetryCleanup(context.Context) error
}
type ConnectionService interface {
	List() ([]Connection, error)
	Save(Connection) error
	Export(string) error
	Preview(string) (ImportPlan, error)
	Commit(ImportPlan, bool) error
}
type ImportPlan struct {
	Additions   []Connection
	Conflicts   []Connection
	Existing    []Connection
	Fingerprint string
	Unbound     []string
}
type SessionService interface {
	List() []Run
	Open(context.Context, Connection, ManagedRole, Snapshot) (Run, error)
	Stop(context.Context, string) error
	Focus(string) error
	Recheck(context.Context, string) error
	Shutdown(context.Context) error
}
type Prerequisites struct {
	CLIVersion, PluginVersion                                            string
	CLIPath, PluginPath, ConfigPath, CredentialsPath, StateDir, Terminal string
	Ready                                                                bool
	Problems                                                             []string
}

// TerminalService exposes only in-memory terminal screens and input, never credentials.
type TerminalService interface {
	Has(string) bool
	Frame(string, int, int, bool) (string, error)
	Send(string, string, string, bool) error
	// Scroll moves the viewport over retained output; lines > 0 shows older output.
	Scroll(string, int) error
	// Wheel delivers one wheel notch at a pane cell; up selects older output.
	Wheel(string, bool, int, int) error
	// ScrollPosition reports lines above live output and lines retained.
	ScrollPosition(string) (int, int)
	// Exited reports that the owned process behind a screen has finished.
	Exited(string) bool
}

type Services struct {
	Terminals  TerminalService
	OpenWindow func(context.Context, Connection, ManagedRole, Snapshot) (Run, error)
	Profiles   []Profile
	// LoadProfiles rereads the effective AWS config without authenticating or
	// executing credential providers. A failed load disables new profile actions.
	LoadProfiles     func() ([]Profile, error)
	Credentials      CredentialService
	Connections      ConnectionService
	Sessions         SessionService
	Prerequisites    Prerequisites
	Theme            string
	SaveTheme        func(string) error
	AccountAliases   map[string]string
	SaveAccountAlias func(string, string) error
	BookmarkPath     string
	// True only for a qualified terminal adapter with stable handles.
	FocusTerminal bool
}
