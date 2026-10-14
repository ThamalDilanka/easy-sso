package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
	"github.com/ThamalDilanka/easy-sso/internal/updater"
)

var distribution = "development"

func buildVersion(info *debug.BuildInfo) (string, string, string) {
	v, c, d := version, commit, distribution
	if d == "release" && releases.Stamp != releases.StampHeader(strings.TrimPrefix(v, "v"), runtime.GOOS, runtime.GOARCH)+c+"|end" {
		return v, c, "unknown"
	}
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs" {
				return v, c, d
			}
		}
	}
	if d == "development" && info != nil && info.Main.Path == "github.com/ThamalDilanka/easy-sso" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v, d = info.Main.Version, "go"
		c = "module"
	}
	return v, c, d
}
func installedVersion() (string, string, string) {
	info, _ := debug.ReadBuildInfo()
	return buildVersion(info)
}

func updateCommand(args []string) error {
	f := flag.NewFlagSet("update", flag.ContinueOnError)
	check := f.Bool("check", false, "check for updates without changing files")
	pre := f.Bool("prerelease", false, "include explicitly tagged prereleases")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected update arguments; use update --help")
	}
	v, _, dist := installedVersion()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	install, err := updater.Inspect(executable, dist)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := updater.New(os.Getenv("EASY_SSO_GITHUB_TOKEN"))
	fmt.Printf("Current: %s (%s)\n", v, install.Channel)
	candidate, err := client.Latest(ctx, runtime.GOOS, runtime.GOARCH, *pre)
	if errors.Is(err, updater.ErrNoRelease) {
		fmt.Println("No compatible published release is available yet.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("Latest compatible release: %s\n%s\n", candidate.Version, install.Guidance(candidate.Version))
	if dist != "release" && dist != "go" {
		message := "development or unknown build; install a tagged release explicitly"
		if *check {
			fmt.Println(message)
			return nil
		}
		return errors.New(message)
	}
	available, err := updater.UpgradeAvailable(v, candidate.Version)
	if err != nil {
		if *check {
			fmt.Println(err)
			return nil
		}
		return err
	}
	if !available {
		fmt.Println("No newer compatible release is available.")
		return nil
	}
	if *check {
		fmt.Println("An update is available.")
		return nil
	}
	if install.Channel != "direct" {
		return errors.New("use the installation method shown above; this executable was not modified")
	}
	if runtime.GOOS == "windows" {
		return errors.New("Windows requires manual replacement after closing all easy-sso apps and helpers")
	}
	lease, err := updater.Acquire(install.Path, true)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := verifyRunningVersion(install.Path); err != nil {
		return err
	}
	fmt.Printf("Downloading and verifying %s…\n", candidate.Version)
	binary, err := client.Download(ctx, candidate)
	if err != nil {
		return err
	}
	if err = lease.Replace(binary); err != nil {
		return err
	}
	fmt.Printf("Installed %s. Start easy-sso when ready.\n", candidate.Version)
	return nil
}

// Hold a shared lease for the whole UI/helper lifetime. This covers independent
// helpers and custom state directories without inspecting credentials or PIDs.
func runtimeLease() (func(), error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	lease, err := updater.Acquire(path, false)
	if err != nil {
		return nil, err
	}
	if err := verifyRunningVersion(path); err != nil {
		lease.Close()
		return nil, err
	}
	return lease.Close, nil
}

func verifyRunningVersion(path string) error {
	_, _, dist := installedVersion()
	if runtime.GOOS == "windows" || dist != "release" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot inspect installed executable")
	}
	defer f.Close()
	binary, err := io.ReadAll(io.LimitReader(f, (96<<20)+1))
	if err != nil || len(binary) > 96<<20 {
		return errors.New("cannot inspect installed executable")
	}
	if releases.Stamp != "development" && bytes.Contains(binary, []byte(releases.Stamp)) {
		return nil
	}
	return errors.New("executable was updated during startup; start easy-sso again")
}
