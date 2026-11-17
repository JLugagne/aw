// Package upgrade replaces the running aw binary with the latest GitHub
// release, verifying the archive against the release's checksums.txt.
package upgrade

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	// ErrNoRelease is returned when the repository has no published release.
	ErrNoRelease = errors.New("no release published")
	// ErrNoAsset is returned when the release has no archive for this platform.
	ErrNoAsset = errors.New("no release archive for this platform")
	// ErrChecksum is returned when the archive does not match checksums.txt.
	ErrChecksum = errors.New("checksum mismatch")
)

// Repository is the GitHub repository releases are fetched from.
const Repository = "JLugagne/aw"

// ModulePath is used by the "go install" fallback.
const ModulePath = "github.com/JLugagne/aw/cmd/aw@latest"

const maxBinarySize = 200 << 20

// Updater fetches releases. The zero value targets api.github.com.
type Updater struct {
	HTTP    *http.Client
	APIBase string
	Repo    string
	OS      string
	Arch    string
}

// Release is the subset of the GitHub release payload aw needs.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Asset is a downloadable release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Version is the tag without its leading "v".
func (r Release) Version() string {
	return strings.TrimPrefix(r.Tag, "v")
}

// ArchiveName is the goreleaser archive name for goos/goarch.
func ArchiveName(goos, goarch string) string {
	return "aw_" + goos + "_" + goarch + ".tar.gz"
}

func (u Updater) client() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return http.DefaultClient
}

func (u Updater) platform() (string, string) {
	goos, goarch := u.OS, u.Arch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (u Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "aw-upgrade")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && strings.HasPrefix(url, u.apiBase()) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, errors.Join(ErrNoRelease, errors.New("GET "+url+": "+resp.Status))
		}
		return nil, errors.New("GET " + url + ": " + resp.Status)
	}
	return resp, nil
}

func (u Updater) apiBase() string {
	if u.APIBase != "" {
		return strings.TrimRight(u.APIBase, "/")
	}
	return "https://api.github.com"
}

// Latest returns the latest published release. It returns ErrNoRelease.
func (u Updater) Latest(ctx context.Context) (Release, error) {
	repo := u.Repo
	if repo == "" {
		repo = Repository
	}
	resp, err := u.get(ctx, u.apiBase()+"/repos/"+repo+"/releases/latest")
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, err
	}
	return r, nil
}

// Install downloads the archive of r for the configured platform, checks its
// SHA-256 against checksums.txt and atomically replaces target. It returns
// ErrNoAsset or ErrChecksum.
func (u Updater) Install(ctx context.Context, r Release, target string) error {
	goos, goarch := u.platform()
	name := ArchiveName(goos, goarch)
	var archive, sums Asset
	for _, a := range r.Assets {
		switch a.Name {
		case name:
			archive = a
		case "checksums.txt":
			sums = a
		}
	}
	if archive.URL == "" {
		return errors.Join(ErrNoAsset, errors.New(name))
	}
	if sums.URL == "" {
		return errors.Join(ErrChecksum, errors.New("checksums.txt missing from release"))
	}
	want, err := u.expectedSum(ctx, sums.URL, name)
	if err != nil {
		return err
	}
	resp, err := u.get(ctx, archive.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBinarySize))
	if err != nil {
		return err
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return errors.Join(ErrChecksum, errors.New(name))
	}
	return replace(data, target)
}

func (u Updater) expectedSum(ctx context.Context, url, name string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.Join(ErrChecksum, errors.New(name+" not listed in checksums.txt"))
}

func replace(archive []byte, target string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("aw binary not found in archive")
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "aw" {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".aw-upgrade-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := io.Copy(tmp, io.LimitReader(tr, maxBinarySize)); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp.Name(), 0o755); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), target)
	}
}

// Executable returns the resolved path of the running binary.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
