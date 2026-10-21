package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/credentials"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/sessions"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	"github.com/ThamalDilanka/easy-sso/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

var version = "0.1.0-dev"
var commit = "unreleased"

type options struct{ State, Config, Credentials, CLI string }

func paths(o options) (domain.Prerequisites, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return domain.Prerequisites{}, e
	}
	state := o.State
	if state == "" {
		dir, e := os.UserConfigDir()
		if e != nil {
			return domain.Prerequisites{}, e
		}
		state = filepath.Join(dir, "easy-sso")
	}
	config := o.Config
	if config == "" {
		config = os.Getenv("AWS_CONFIG_FILE")
	}
	if config == "" {
		config = filepath.Join(home, ".aws", "config")
	}
	creds := o.Credentials
	if creds == "" {
		creds = os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	}
	if creds == "" {
		creds = filepath.Join(home, ".aws", "credentials")
	}
	p := domain.Prerequisites{StateDir: state, ConfigPath: config, CredentialsPath: creds}
	var err error
	p.StateDir, err = filepath.Abs(p.StateDir)
	if err != nil {
		return p, err
	}
	p.ConfigPath, err = filepath.Abs(p.ConfigPath)
	if err != nil {
		return p, err
	}
	p.CredentialsPath, err = filepath.Abs(p.CredentialsPath)
	if err != nil {
		return p, err
	}
	// Activation refuses to publish into a shared AWS directory; report it here too.
	dirs := []string{filepath.Dir(p.CredentialsPath)}
	if dir := filepath.Dir(p.ConfigPath); dir != dirs[0] {
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		if e := store.CheckEnsureDir(dir); e != nil {
			p.Problems = append(p.Problems, "AWS files directory: "+e.Error())
		}
	}
	cli := o.CLI
	if cli == "" {
		cli = "aws"
	}
	p.CLIPath, e = exec.LookPath(cli)
	if e != nil {
		p.Problems = append(p.Problems, "Install the official AWS CLI v2 to activate roles or connect.")
	} else {
		if runtime.GOOS == "linux" && strings.EqualFold(filepath.Ext(p.CLIPath), ".exe") {
			p.Problems = append(p.Problems, "Use the Linux AWS CLI in WSL, with the Linux home and credentials.")
			p.CLIPath = ""
		} else {
			var valid bool
			p.CLIVersion, valid = probeVersionInfo(p.CLIPath, "aws-cli/2.")
			if !valid {
				p.Problems = append(p.Problems, "AWS CLI v2 is required; the selected executable did not report a v2 version.")
				p.CLIPath = ""
			}
		}
	}
	p.PluginPath, e = exec.LookPath("session-manager-plugin")
	if e != nil {
		p.Problems = append(p.Problems, "Install the official Session Manager plugin before opening shells or tunnels.")
	} else if runtime.GOOS == "linux" && strings.EqualFold(filepath.Ext(p.PluginPath), ".exe") {
		p.Problems = append(p.Problems, "Use the Linux Session Manager plugin in WSL.")
		p.PluginPath = ""
	} else {
		var valid bool
		p.PluginVersion, valid = probeVersionInfo(p.PluginPath, "")
		if !valid {
			p.Problems = append(p.Problems, "Session Manager plugin version check failed.")
			p.PluginPath = ""
		}
	}
	adapter, e := terminal.Detect()
	if embedded, err := terminal.NewEmbedded(); err == nil {
		adapter = embedded
		e = nil
	}
	if e != nil {
		p.Terminal = e.Error()
	} else {
		caps := adapter.Capabilities()
		p.Terminal = fmt.Sprintf("%T: launchTab=%t launchWindow=%t focusExisting=%t", adapter, caps.LaunchTab, caps.LaunchWindow, caps.FocusExisting)
	}
	p.Ready = p.CLIPath != "" && p.PluginPath != ""
	return p, nil
}
func probeVersionInfo(path, prefix string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	var b boundedVersion
	cmd.Stdout = &b
	cmd.Stderr = &b
	if cmd.Run() != nil || b.exceeded {
		return "", false
	}
	valid := strings.Contains(b.text, prefix) && len(strings.TrimSpace(b.text)) > 0
	text := b.text
	if prefix != "" {
		if at := strings.Index(text, "aws-cli/"); at >= 0 {
			text = text[at+len("aws-cli/"):]
		}
	}
	version := regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+){1,3}\b`).FindString(text)
	return version, valid
}

type boundedVersion struct {
	text     string
	exceeded bool
}

func (b *boundedVersion) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.text)+n > 4096 {
		b.exceeded = true
		return n, nil
	}
	b.text += string(p)
	return n, nil
}

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "easy-sso:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		v, c, _ := installedVersion()
		fmt.Printf("easy-sso %s (%s) %s %s/%s\n", v, c, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return nil
	}
	if len(args) > 0 && args[0] == "update" {
		return updateCommand(args[1:])
	}
	releaseLease, err := runtimeLease()
	if err != nil {
		return err
	}
	defer releaseLease()
	if len(args) > 0 && args[0] == "internal-helper" {
		f := flag.NewFlagSet("internal-helper", flag.ContinueOnError)
		state := f.String("state-dir", "", "private runtime directory")
		id := f.String("run-id", "", "one-use run identifier")
		if e := f.Parse(args[1:]); e != nil {
			return e
		}
		if *state == "" || *id == "" {
			return errors.New("helper startup requires state directory and run ID")
		}
		return sessions.RunHelper(context.Background(), *state, *id)
	}
	diagnostic := len(args) > 0 && args[0] == "diagnostics"
	if diagnostic {
		args = args[1:]
	}
	f := flag.NewFlagSet("easysso", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: easysso [flags] | version | diagnostics | update [--check] [--prerelease]")
		f.PrintDefaults()
	}
	var o options
	f.StringVar(&o.State, "state-dir", "", "private application state directory")
	f.StringVar(&o.Config, "aws-config", "", "AWS config file (defaults to AWS_CONFIG_FILE)")
	f.StringVar(&o.Credentials, "credentials-file", "", "AWS shared credentials file (defaults to AWS_SHARED_CREDENTIALS_FILE)")
	f.StringVar(&o.CLI, "aws-cli", "", "official AWS CLI executable")
	if e := f.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments; use --help")
	}
	p, e := paths(o)
	if e != nil {
		return e
	}
	ps, e := profiles.Load(p.ConfigPath)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			ps = nil
		} else {
			p.Problems = append(p.Problems, "AWS profile configuration is invalid: "+e.Error())
			ps = nil
		}
	}
	if diagnostic {
		currentVersion, _, _ := installedVersion()
		out := struct {
			Version       string
			Runtime       string
			Prerequisites domain.Prerequisites
			Profiles      []domain.Profile
		}{currentVersion, runtime.GOOS + "/" + runtime.GOARCH, p, ps}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	// Registered before any child starts and stopped last, after Shutdown and
	// CloseAll, so a closed terminal window quits through the normal path.
	hangup, stopHangup := watchHangup()
	defer stopHangup()
	if e = store.EnsureDir(p.StateDir); e != nil {
		return e
	}
	bookmarks, e := connections.New(p.StateDir)
	if e != nil {
		return e
	}
	bookmarks.SetProfiles(ps)
	bookmarks.SetProtectedPaths(p.ConfigPath, p.CredentialsPath)
	var cli domain.CLI = unavailableCLI{}
	var client *awscli.Client
	if p.CLIPath != "" {
		client, e = awscli.New(p.CLIPath, p.ConfigPath, p.CredentialsPath, p.StateDir)
		if e != nil {
			return e
		}
		cli = client
	}
	creds, e := credentials.New(cli, p.ConfigPath, p.CredentialsPath, p.StateDir)
	if e != nil {
		return e
	}
	executable, e := os.Executable()
	if e != nil {
		return e
	}
	var embedded *terminal.Embedded
	opts := sessions.Options{RecoveryCredentials: creds.Snapshot}
	if embedded, e = terminal.NewEmbedded(); e == nil {
		opts.Terminal = embedded
		defer embedded.CloseAll()
	}
	manager, e := sessions.NewWithOptions(cli, p.StateDir, executable, bookmarks, opts)
	if e != nil {
		return e
	}
	settings, e := store.LoadSettings(p.StateDir)
	if e != nil {
		return e
	}
	services := domain.Services{Profiles: ps, Credentials: creds, Connections: bookmarks, Sessions: manager, OpenWindow: manager.OpenWindow, Prerequisites: p, Theme: settings.Theme, SaveTheme: func(theme string) error { return store.SaveTheme(p.StateDir, theme) }, AccountAliases: settings.AccountAliases, BookmarkPath: bookmarks.Path(), SaveAccountAlias: func(account, label string) error { return store.SaveAlias(p.StateDir, account, label) }, ClientPlaces: settings.ClientPlaces, SaveClientPlace: func(app, place string) error { return store.SaveClientPlace(p.StateDir, app, place) }, LoadProfiles: func() ([]domain.Profile, error) {
		current, err := profiles.Load(p.ConfigPath)
		if err != nil {
			bookmarks.SetProfiles(nil)
			return nil, err
		}
		bookmarks.SetProfiles(current)
		return current, nil
	}}
	if adapter, err := terminal.Detect(); err == nil {
		caps := adapter.Capabilities()
		services.FocusTerminal = caps.FocusExisting && caps.StableHandles
	}
	if embedded != nil {
		services.Terminals = embedded
		services.FocusTerminal = true
	}
	program := tea.NewProgram(ui.New(services), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if client != nil {
		client.LoginNotice = func(s string) { program.Send(ui.AuthNoticeMsg(s)) }
	}
	quit := make(chan struct{})
	go func() {
		select {
		case <-hangup:
			program.Quit() // a no-op once Run has returned
		case <-quit:
		}
	}()
	_, runErr := program.Run()
	close(quit)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(runErr, manager.Shutdown(ctx))
}

type unavailableCLI struct{}

func (unavailableCLI) Login(context.Context, string) error {
	return errors.New("install the official AWS CLI v2")
}
func (unavailableCLI) Export(context.Context, domain.Profile) (domain.Snapshot, error) {
	return domain.Snapshot{}, errors.New("install the official AWS CLI v2")
}
func (unavailableCLI) Logout(context.Context) error {
	return errors.New("install the official AWS CLI v2 to retry cleanup")
}
func (unavailableCLI) Identity(context.Context, domain.Snapshot, string) (domain.Identity, error) {
	return domain.Identity{}, errors.New("install the official AWS CLI v2")
}
func (unavailableCLI) Start(context.Context, domain.Snapshot, []string, io.Reader, io.Writer, io.Writer) (domain.Child, error) {
	return nil, errors.New("install the official AWS CLI v2")
}
func (unavailableCLI) Describe(context.Context, domain.Snapshot, string, []string) ([]domain.SessionMetadata, error) {
	return nil, errors.New("install the official AWS CLI v2")
}
func (unavailableCLI) Terminate(context.Context, domain.Snapshot, string, string) error {
	return errors.New("install the official AWS CLI v2")
}
