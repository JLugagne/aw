// Package repo turns the repository strings written in the configuration into
// a clone URL and a stable location relative to the base directory.
package repo

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

// ErrInvalid is returned by Parse when a spec cannot be mapped to a clone URL
// and a safe relative path.
var ErrInvalid = errors.New("invalid repository")

// Protocol values accepted by Parse for host/owner/name specs.
const (
	ProtocolSSH   = "ssh"
	ProtocolHTTPS = "https"
)

// Ref is a parsed repository spec.
type Ref struct {
	Spec string
	// Path is slash-separated and relative to the base directory, e.g.
	// github.com/org/name. It never contains "..".
	Path string
	URL  string
}

// Name is the last element of Path, used to name the session directory of the repository.
func (r Ref) Name() string {
	return path.Base(r.Path)
}

// Owner is the element of Path preceding Name, used to disambiguate
// session directories when two repositories share the same Name.
func (r Ref) Owner() string {
	return path.Base(path.Dir(r.Path))
}

// Parse accepts "host/owner/name", scp-like "git@host:owner/name.git" and
// URLs ("https://", "ssh://", "file://"). For the host/owner/name form the
// clone URL is built with protocol (ProtocolSSH when empty). It returns
// ErrInvalid for empty specs or paths escaping the base directory.
func Parse(spec, protocol string) (Ref, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return Ref{}, errors.Join(ErrInvalid, errors.New("empty repository"))
	}
	// A leading "-" would be read by git as an option (e.g. --upload-pack).
	if strings.HasPrefix(s, "-") {
		return Ref{}, errors.Join(ErrInvalid, errors.New("repository cannot start with '-': "+s))
	}
	host, p, cloneURL, err := split(s, protocol)
	if err != nil {
		return Ref{}, errors.Join(ErrInvalid, err)
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || p == "" {
		return Ref{}, errors.Join(ErrInvalid, errors.New("missing host or path in "+s))
	}
	if !safeSegment(host) {
		return Ref{}, errors.Join(ErrInvalid, errors.New("unsafe host in "+s))
	}
	for seg := range strings.SplitSeq(p, "/") {
		if !safeSegment(seg) {
			return Ref{}, errors.Join(ErrInvalid, errors.New("unsafe path in "+s))
		}
	}
	return Ref{Spec: s, Path: host + "/" + p, URL: cloneURL}, nil
}

// safeSegment reports whether s can be used as one directory name under the
// base directory without escaping it.
func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`+"\x00")
}

func split(s, protocol string) (string, string, string, error) {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", "", "", err
		}
		if u.Scheme == "file" {
			return "local", u.Path, s, nil
		}
		return u.Hostname(), u.Path, s, nil
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		host := s[:i]
		if j := strings.LastIndex(host, "@"); j >= 0 {
			host = host[j+1:]
		}
		return host, s[i+1:], s, nil
	}
	host, p, _ := strings.Cut(s, "/")
	switch protocol {
	case "", ProtocolSSH:
		return host, p, "git@" + host + ":" + strings.TrimSuffix(p, ".git") + ".git", nil
	case ProtocolHTTPS:
		return host, p, "https://" + host + "/" + strings.TrimSuffix(p, ".git") + ".git", nil
	default:
		return "", "", "", errors.New("unknown protocol " + protocol)
	}
}

// SplitSpec separates the optional "@branch" suffix used on the command line
// (github.com/org/name@feature). The "@" of scp-like and ssh:// URLs is not
// mistaken for a branch separator.
func SplitSpec(s string) (string, string) {
	i := strings.LastIndex(s, "@")
	if i <= 0 || i == len(s)-1 {
		return s, ""
	}
	left, right := s[:i], s[i+1:]
	if strings.Contains(right, ":") {
		return s, ""
	}
	bare := left
	if _, after, ok := strings.Cut(bare, "://"); ok {
		bare = after
	}
	if !strings.ContainsAny(bare, "/:") {
		return s, ""
	}
	return left, right
}
