package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
)

func checksum(data []byte, c Candidate) (string, error) {
	expected := map[string]bool{}
	for _, t := range releases.Contract.Targets {
		p := strings.Split(t, "-")
		name, _ := releases.Asset(c.Version, p[0], p[1])
		expected[name] = true
	}
	seen := map[string]bool{}
	result := ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Split(line, "  ")
		if len(fields) != 2 || !expected[fields[1]] || seen[fields[1]] {
			return "", errors.New("invalid checksum manifest contents")
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return "", errors.New("invalid SHA256 digest")
		}
		seen[fields[1]] = true
		if fields[1] == c.archive.Name {
			result = strings.ToLower(fields[0])
		}
	}
	if len(seen) != len(expected) || result == "" {
		return "", errors.New("incomplete checksum manifest")
	}
	return result, nil
}
func verifyArchive(data []byte, expected string, c Candidate) ([]byte, error) {
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		return nil, errors.New("release checksum mismatch; installed executable unchanged")
	}
	binary, err := extract(data, c.goos)
	if err != nil {
		return nil, err
	}
	if err = validateBinary(binary, c); err != nil {
		return nil, err
	}
	return binary, nil
}
func extract(data []byte, goos string) ([]byte, error) {
	allowed := map[string]bool{releases.Binary(goos): true, "easy-sso.install.json": true}
	for _, p := range releases.Contract.Documents {
		allowed[p] = true
	}
	seen := map[string]bool{}
	var binary []byte
	var total int64
	consume := func(name string, size int64, regular bool, r io.Reader) error {
		if !allowed[name] || seen[name] || !regular {
			return errors.New("unsafe or unexpected archive entry")
		}
		if size < 0 || size > 96<<20 || total+size > 160<<20 {
			return errors.New("expanded archive exceeds size limit")
		}
		seen[name] = true
		total += size
		b, err := io.ReadAll(io.LimitReader(r, size+1))
		if err != nil || int64(len(b)) != size {
			return errors.New("invalid archive entry size or content")
		}
		if name == releases.Binary(goos) {
			binary = b
		}
		return nil
	}
	if goos == "windows" {
		z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, errors.New("invalid ZIP archive")
		}
		if len(z.File) > len(allowed) {
			return nil, errors.New("too many archive entries")
		}
		for _, f := range z.File {
			if f.UncompressedSize64 > 96<<20 {
				return nil, errors.New("expanded archive exceeds size limit")
			}
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = consume(f.Name, int64(f.UncompressedSize64), f.Mode().IsRegular(), r)
			r.Close()
			if err != nil {
				return nil, err
			}
		}
	} else {
		z, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("invalid gzip archive")
		}
		defer z.Close()
		bounded := io.LimitReader(z, (160<<20)+1)
		t := tar.NewReader(bounded)
		for {
			h, err := t.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, errors.New("invalid tar archive")
			}
			if err = consume(h.Name, h.Size, h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA, t); err != nil {
				return nil, err
			}
		}
		// Read through the gzip trailer to detect truncation and CRC errors.
		tail, err := io.ReadAll(io.LimitReader(bounded, (1<<20)+1))
		if err != nil || len(tail) > 1<<20 || len(bytes.Trim(tail, "\x00")) != 0 {
			return nil, errors.New("invalid trailing archive data")
		}
	}
	if len(seen) != len(allowed) || len(binary) == 0 {
		return nil, errors.New("release archive is incomplete")
	}
	return binary, nil
}
func validateBinary(binary []byte, c Candidate) error {
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		return errors.New("release executable has no Go build metadata")
	}
	if info.Path != "github.com/ThamalDilanka/easy-sso/cmd/easysso" {
		return errors.New("release executable has wrong module path")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != c.goos || settings["GOARCH"] != c.arch {
		return errors.New("release executable platform mismatch")
	}
	if !bytes.Contains(binary, []byte(releases.StampHeader(c.Version, c.goos, c.arch))) {
		return errors.New("release executable version or provenance mismatch")
	}
	return nil
}
