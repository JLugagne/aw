// Package config loads and saves the aw configuration file
// (~/.config/aw/config.yaml by default).
package config

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Defaults applied when the corresponding key is missing from the file.
const (
	DefaultBase     = "~/src"
	DefaultTool     = "claude"
	DefaultSyncFile = "aw.yaml"
)

var (
	// ErrInvalidID is returned by ValidateID.
	ErrInvalidID = errors.New("invalid session id: use letters, digits, '.', '_' or '-', starting with a letter or digit")
	// ErrSessionExists is returned by AddSession when the id is taken.
	ErrSessionExists = errors.New("session already exists")
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Project is one repository of a session. An empty Branch means the session
// id is used as branch name.
type Project struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch,omitempty"`
}

// Session groups the projects opened together in one workspace.
type Session struct {
	Name     string    `yaml:"name,omitempty"`
	Projects []Project `yaml:"projects"`
}

// Tool describes how to launch an agent CLI inside a session directory.
type Tool struct {
	Command string   `yaml:"command,omitempty"`
	Args    []string `yaml:"args,omitempty"`
	// AddDirFlag is emitted before every project directory. When empty the
	// tool only receives the session directory as working directory.
	AddDirFlag string `yaml:"add_dir_flag,omitempty"`
	// ContinueArgs resume the latest conversation ("aw run -c"); they are
	// placed before the project directories.
	ContinueArgs []string `yaml:"continue_args,omitempty"`
	// Env holds defaults: a variable already defined in the caller's
	// environment is never overridden.
	Env map[string]string `yaml:"env,omitempty"`
}

// Sync configures "aw config sync". It normally lives in config.local.yaml.
type Sync struct {
	// Remote is any git URL: a GitHub gist, a GitLab snippet or a repository.
	Remote string `yaml:"remote"`
	File   string `yaml:"file,omitempty"`
	// Auto syncs before and after every command that modifies the config,
	// and before run, edit and sync.
	Auto bool `yaml:"auto,omitempty"`
}

// FileName is the name of the config file inside the remote.
func (s Sync) FileName() string {
	if s.File == "" {
		return DefaultSyncFile
	}
	return s.File
}

// Config mirrors config.yaml, overlaid by config.local.yaml. Use the accessor
// methods rather than the raw fields: they apply the local overlay and the
// defaults, and expand "~" and environment variables. Save only writes the
// shared file.
type Config struct {
	Base       string              `yaml:"base,omitempty"`
	Workspaces string              `yaml:"workspaces,omitempty"`
	Tool       string              `yaml:"tool,omitempty"`
	Editor     string              `yaml:"editor,omitempty"`
	Protocol   string              `yaml:"protocol,omitempty"`
	Tools      map[string]Tool     `yaml:"tools,omitempty"`
	Sync       *Sync               `yaml:"sync,omitempty"`
	Sessions   map[string]*Session `yaml:"sessions,omitempty"`

	local     *Config
	localPath string
}

var builtinTools = map[string]Tool{
	"claude": {
		Command:      "claude",
		AddDirFlag:   "--add-dir",
		ContinueArgs: []string{"--continue"},
		Env:          map[string]string{"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1"},
	},
	"codex":    {Command: "codex", AddDirFlag: "--add-dir"},
	"opencode": {Command: "opencode", ContinueArgs: []string{"--continue"}},
}

// DefaultPath returns $AW_CONFIG, else $XDG_CONFIG_HOME/aw/config.yaml, else
// ~/.config/aw/config.yaml.
func DefaultPath() (string, error) {
	if p := os.Getenv("AW_CONFIG"); p != "" {
		return ExpandPath(p)
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "aw", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "aw", "config.yaml"), nil
}

// LocalPathFor returns the machine-specific overlay of path:
// config.yaml -> config.local.yaml.
func LocalPathFor(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".local" + ext
}

// Load reads path and its overlay (see LocalPathFor). Missing files yield an
// empty configuration so the first "aw create" can bootstrap it. Unknown keys
// are rejected to surface typos; sessions are rejected in the overlay.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, errors.Join(errors.New("parse "+path), err)
	}
	if data == nil {
		c.Base = DefaultBase
	}
	c.localPath = LocalPathFor(path)
	localData, err := os.ReadFile(c.localPath)
	if errors.Is(err, os.ErrNotExist) {
		localData, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	local, err := Parse(localData)
	if err != nil {
		return nil, errors.Join(errors.New("parse "+c.localPath), err)
	}
	if len(local.Sessions) > 0 {
		return nil, errors.New(c.localPath + ": sessions belong in " + filepath.Base(path))
	}
	c.local = local
	return c, nil
}

// Parse decodes the content of a shared config file.
func Parse(data []byte) (*Config, error) {
	var c Config
	if len(data) > 0 {
		if err := yaml.Load(data, &c, yaml.WithKnownFields()); err != nil {
			return nil, err
		}
	}
	if c.Sessions == nil {
		c.Sessions = map[string]*Session{}
	}
	for id, s := range c.Sessions {
		if s == nil {
			c.Sessions[id] = &Session{}
		}
	}
	return &c, nil
}

// Marshal encodes the shared part of the configuration.
func (c *Config) Marshal() ([]byte, error) {
	return yaml.Dump(c, yaml.WithIndent(2))
}

// Save writes the shared configuration atomically, creating parent
// directories. Comments present in the original file are not preserved.
func (c *Config) Save(path string) error {
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	return WriteFile(path, data)
}

// LocalPath is the overlay file of a configuration returned by Load.
func (c *Config) LocalPath() string {
	return c.localPath
}

// SetSync stores s in the overlay file, keeping its other settings.
func (c *Config) SetSync(s Sync) error {
	if c.localPath == "" {
		return errors.New("configuration was not loaded from a file")
	}
	l := c.overlay()
	l.Sync = &s
	c.local = l
	data, err := l.Marshal()
	if err != nil {
		return err
	}
	return WriteFile(c.localPath, data)
}

// SyncSettings returns the sync section of the overlay, else of the shared
// file; Remote is empty when sync is not configured.
func (c *Config) SyncSettings() Sync {
	if l := c.overlay(); l.Sync != nil {
		return *l.Sync
	}
	if c.Sync != nil {
		return *c.Sync
	}
	return Sync{}
}

func (c *Config) overlay() *Config {
	if c.local == nil {
		return &Config{}
	}
	return c.local
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// WriteFile atomically replaces path with data, creating parent directories.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// BaseDir is the absolute directory holding the shared clones, laid out as
// <base>/<host>/<owner>/<name>.
func (c *Config) BaseDir() (string, error) {
	return ExpandPath(first(c.overlay().Base, c.Base, DefaultBase))
}

// WorkspacesDir is the absolute directory holding one sub-directory per
// session. It defaults to <base>/.aw/sessions.
func (c *Config) WorkspacesDir() (string, error) {
	if w := first(c.overlay().Workspaces, c.Workspaces); w != "" {
		return ExpandPath(w)
	}
	base, err := c.BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, ".aw", "sessions"), nil
}

// ToolName returns the configured default tool.
func (c *Config) ToolName() string {
	return first(c.overlay().Tool, c.Tool, DefaultTool)
}

// ProtocolName returns the clone protocol for host/owner/name specs.
func (c *Config) ProtocolName() string {
	return first(c.overlay().Protocol, c.Protocol)
}

// EditorCommand splits the configured editor (e.g. "code -n") into argv.
// It falls back to $VISUAL, $EDITOR, then "vi".
func (c *Config) EditorCommand(override string) []string {
	for _, e := range []string{override, c.overlay().Editor, c.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if f := strings.Fields(e); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

// ResolveTool returns the built-in definition of name (claude, codex,
// opencode), or a bare command for any other name, overlaid with the fields
// set in the "tools" section of the shared file, then of the overlay. Env
// maps are merged, the last layer winning.
func (c *Config) ResolveTool(name string) Tool {
	t := Tool{Command: name}
	if b, ok := builtinTools[name]; ok {
		t = b
	}
	t.Env = maps.Clone(t.Env)
	for _, layer := range []*Config{c, c.overlay()} {
		if o, ok := layer.Tools[name]; ok {
			t = overlayTool(t, o)
		}
	}
	return t
}

func overlayTool(t, o Tool) Tool {
	if o.Command != "" {
		t.Command = o.Command
	}
	if o.Args != nil {
		t.Args = o.Args
	}
	if o.AddDirFlag != "" {
		t.AddDirFlag = o.AddDirFlag
	}
	if o.ContinueArgs != nil {
		t.ContinueArgs = o.ContinueArgs
	}
	if len(o.Env) > 0 {
		if t.Env == nil {
			t.Env = map[string]string{}
		}
		maps.Copy(t.Env, o.Env)
	}
	return t
}

// AddSession registers an empty session. It returns ErrInvalidID or
// ErrSessionExists.
func (c *Config) AddSession(id, name string) (*Session, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if _, ok := c.Sessions[id]; ok {
		return nil, ErrSessionExists
	}
	if c.Sessions == nil {
		c.Sessions = map[string]*Session{}
	}
	s := &Session{Name: name}
	c.Sessions[id] = s
	return s, nil
}

// ValidateID checks that id is usable both as a directory and a git branch
// name.
func ValidateID(id string) error {
	if !idPattern.MatchString(id) || strings.HasSuffix(id, ".lock") || strings.Contains(id, "..") {
		return ErrInvalidID
	}
	return nil
}

// ExpandPath expands a leading "~" and environment variables, then makes the
// result absolute.
func ExpandPath(p string) (string, error) {
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}
