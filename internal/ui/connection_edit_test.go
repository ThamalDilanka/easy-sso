package ui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func TestEditSavedConnections(t *testing.T) {
	for _, kind := range []string{"ec2", "db"} {
		t.Run(kind, func(t *testing.T) {
			f := setupUI(t)
			manager, err := connections.New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			c := domain.Connection{ID: "saved", Kind: kind, Name: "before", AccountID: "111111111111", Partition: "aws", Region: "us-east-1", LastUsedProfile: "one"}
			if kind == "ec2" {
				c.InstanceID = "i-0123456789abcdef0"
			} else {
				c.Endpoint = "old.example.com"
				c.DBPort = 5432
				c.LocalPort = 15432
				c.RelayInstanceID = "i-0123456789abcdef0"
				c.RelayRegion = "us-east-1"
				c.DBAccountID = c.AccountID
				f.m.screen = databases
			}
			if err := manager.Save(c); err != nil {
				t.Fatal(err)
			}
			f.m.services.Connections = manager
			finish(f.m, f.m.load())
			key(f.m, "e")
			if f.m.form == nil || f.m.form.original.ID != c.ID {
				t.Fatal("edit did not select saved bookmark")
			}
			f.m.form.fields[0].value = "after"
			key(f.m, "esc")
			got, _ := manager.List()
			if !reflect.DeepEqual(got[0], c) {
				t.Fatal("cancel changed bookmark")
			}
			key(f.m, "e")
			values := []string{"after", "i-1123456789abcdef0", "ap-south-1", "  Example-ShellDocument  "}
			if kind == "db" {
				values = []string{"after", "NEW.example.com", "ap-south-1", "5433", "i-1123456789abcdef0", "ap-south-1", "15433"}
			}
			for i, v := range values {
				f.m.form.fields[i].value = v
			}
			key(f.m, "ctrl+s")
			got, err = manager.List()
			if err != nil || len(got) != 1 || got[0].ID != c.ID || got[0].Name != "after" || got[0].Region != "ap-south-1" || got[0].LastUsedProfile != "one" {
				t.Fatalf("update failed: %+v %v", got, err)
			}
			if kind == "db" && (got[0].Endpoint != "new.example.com" || got[0].DBPort != 5433 || got[0].LocalPort != 15433 || got[0].RelayRegion != "ap-south-1" || got[0].RelayInstanceID != values[4]) {
				t.Fatal("DB fields not saved")
			}
			if kind == "ec2" && (got[0].InstanceID != values[1] || got[0].DocumentName != "Example-ShellDocument") {
				t.Fatalf("instance or session document not saved: %+v", got[0])
			}
			if f.m.form != nil || len(f.sessions.runs) > 0 || f.creds.snapshots > 0 {
				t.Fatal("save connected or left form open")
			}
			// Review exposes the same editor; invalid edits remain available to fix.
			f.m.connectionReview(got[0])
			key(f.m, "e")
			f.m.form.fields[1].value = "invalid"
			key(f.m, "ctrl+s")
			if f.m.form == nil || !strings.HasPrefix(f.m.status, "Error:") {
				t.Fatal("invalid edit should keep form and show error")
			}
			unchanged, _ := manager.List()
			if !reflect.DeepEqual(unchanged, got) {
				t.Fatal("invalid edit modified storage")
			}
		})
	}
}
