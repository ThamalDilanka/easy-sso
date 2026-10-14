//go:build !windows

package database

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
)

// A real psql/PTY talking only to an isolated TLS PostgreSQL protocol fixture.
// This proves hostaddr routing, remote hostname verification, password delivery,
// and prompt detection together without touching AWS or executing SQL.
func TestRealPSQLVerifiedTLSAndAuthenticatedPrompt(t *testing.T) {
	path, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql unavailable")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "db.example.rds.amazonaws.com"}, DNSNames: []string{"db.example.rds.amazonaws.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified", true: "wrong-hostname"}[mismatch], func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			received := make(chan string, 1)
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				raw, err := l.Accept()
				if err != nil {
					return
				}
				defer raw.Close()
				_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
				ssl := make([]byte, 8)
				if _, err = io.ReadFull(raw, ssl); err != nil || binary.BigEndian.Uint32(ssl[4:]) != 80877103 {
					return
				}
				_, _ = raw.Write([]byte("S"))
				conn := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
				if conn.Handshake() != nil {
					return
				}
				header := make([]byte, 4)
				if _, err = io.ReadFull(conn, header); err != nil {
					return
				}
				n := int(binary.BigEndian.Uint32(header))
				if n < 8 || n > 8192 {
					return
				}
				startup := make([]byte, n-4)
				if _, err = io.ReadFull(conn, startup); err != nil {
					return
				}
				send := func(kind byte, payload []byte) {
					b := make([]byte, 5+len(payload))
					b[0] = kind
					binary.BigEndian.PutUint32(b[1:5], uint32(4+len(payload)))
					copy(b[5:], payload)
					_, _ = conn.Write(b)
				}
				send('R', []byte{0, 0, 0, 3})
				h := make([]byte, 5)
				if _, err = io.ReadFull(conn, h); err != nil || h[0] != 'p' {
					return
				}
				n = int(binary.BigEndian.Uint32(h[1:]))
				if n < 4 || n > 8192 {
					return
				}
				password := make([]byte, n-4)
				if _, err = io.ReadFull(conn, password); err != nil {
					return
				}
				received <- strings.TrimSuffix(string(password), "\x00")
				send('R', []byte{0, 0, 0, 0})
				for _, parameter := range []string{"server_version\x0016.0\x00", "client_encoding\x00UTF8\x00", "standard_conforming_strings\x00on\x00", "session_authorization\x00iam_reader\x00", "is_superuser\x00off\x00"} {
					send('S', []byte(parameter))
				}
				send('Z', []byte{'I'})
				_, _ = io.Copy(io.Discard, conn)
			}()
			c := domain.Connection{Endpoint: "db.example.rds.amazonaws.com", LocalPort: l.Addr().(*net.TCPAddr).Port, Database: "exampledb", DBUser: "iam_reader"}
			if mismatch {
				c.Endpoint = "wrong.example.rds.amazonaws.com"
			}
			cmd, output := Command(path, ca, "tls-test", "TEST-IAM-TOKEN", c)
			term, err := terminal.NewEmbedded()
			if err != nil {
				t.Fatal(err)
			}
			defer term.CloseAll()
			done, err := term.LaunchClient(context.Background(), "tls-test", cmd, output.Filter)
			if err != nil {
				t.Fatal(err)
			}
			if mismatch {
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					t.Fatal("TLS failure did not exit")
				}
				select {
				case <-output.Ready:
					t.Fatal("bad certificate accepted")
				default:
				}
				if !strings.Contains(output.Failure(), "TLS") {
					t.Fatal(output.Failure())
				}
				return
			}
			select {
			case <-output.Ready:
			case <-done:
				frame, _ := term.Frame("tls-test", 100, 24, false)
				t.Fatal("psql exited before prompt", frame)
			case <-time.After(8 * time.Second):
				t.Fatal("psql prompt timeout")
			}
			select {
			case password := <-received:
				if password != "TEST-IAM-TOKEN" {
					t.Fatal("password delivery mismatch")
				}
			default:
				t.Fatal("prompt before authentication")
			}
			if err := term.Send("tls-test", "", "\\q\n", false); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("psql did not exit")
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("database socket not closed")
			}
		})
	}
}
