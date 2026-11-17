package upgrade_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JLugagne/aw/internal/upgrade"
)

func archive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"README.md", "readme"}, {"aw", content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func server(t *testing.T, data []byte, sum string) *httptest.Server {
	t.Helper()
	name := upgrade.ArchiveName("linux", "amd64")
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v1.2.3","assets":[{"name":%q,"browser_download_url":"%s/dl/a"},{"name":"checksums.txt","browser_download_url":"%s/dl/sums"}]}`, name, srv.URL, srv.URL)
	})
	mux.HandleFunc("/dl/a", func(w http.ResponseWriter, _ *http.Request) { w.Write(data) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  aw_darwin_arm64.tar.gz\n%s  %s\n", sum, sum, name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestAndInstall(t *testing.T) {
	data := archive(t, "new-binary")
	h := sha256.Sum256(data)
	srv := server(t, data, hex.EncodeToString(h[:]))
	u := upgrade.Updater{APIBase: srv.URL, Repo: "o/r", OS: "linux", Arch: "amd64"}

	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version() != "1.2.3" {
		t.Fatalf("version = %q", rel.Version())
	}
	target := filepath.Join(t.TempDir(), "aw")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background(), rel, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-binary" {
		t.Fatalf("binary not replaced: %q", got)
	}
}

func TestInstallChecksumMismatch(t *testing.T) {
	srv := server(t, archive(t, "evil"), "00")
	u := upgrade.Updater{APIBase: srv.URL, Repo: "o/r", OS: "linux", Arch: "amd64"}
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "aw")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background(), rel, target); !errors.Is(err, upgrade.ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("binary replaced despite checksum mismatch")
	}
}

func TestInstallNoAsset(t *testing.T) {
	srv := server(t, nil, "00")
	u := upgrade.Updater{APIBase: srv.URL, Repo: "o/r", OS: "plan9", Arch: "mips"}
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background(), rel, filepath.Join(t.TempDir(), "aw")); !errors.Is(err, upgrade.ErrNoAsset) {
		t.Fatalf("want ErrNoAsset, got %v", err)
	}
}

func TestLatestNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	u := upgrade.Updater{APIBase: srv.URL, Repo: "o/r"}
	if _, err := u.Latest(context.Background()); !errors.Is(err, upgrade.ErrNoRelease) {
		t.Fatalf("want ErrNoRelease, got %v", err)
	}
}
