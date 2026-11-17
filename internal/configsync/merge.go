package configsync

import (
	"bytes"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/JLugagne/aw/internal/config"
)

// Choice resolves a Conflict.
type Choice int

// Possible resolutions of a Conflict.
const (
	KeepLocal Choice = iota
	TakeRemote
)

// Conflict is a unit modified differently on both sides since the base
// revision: "settings" (everything but sessions) or "session <id>". Each side
// is its YAML encoding, nil when absent on that side.
type Conflict struct {
	Key    string
	Base   []byte
	Local  []byte
	Remote []byte
}

// Resolver chooses a side for a Conflict; an error aborts the merge and is
// returned as is.
type Resolver func(Conflict) (Choice, error)

// Merge performs a three-way merge of the shared configuration, unit by unit:
// a unit changed on one side only takes that side, a unit changed identically
// on both is kept, anything else is handed to resolve. base is the content at
// the last synced revision (an empty configuration on first sync). It
// returns the merged configuration and one human-readable line per unit that
// differed between local and remote.
func Merge(base, local, remote *config.Config, resolve Resolver) (*config.Config, []string, error) {
	var report []string
	out := settingsOf(local)
	pick, line, err := merge3("settings", encode(settingsOf(base)), encode(settingsOf(local)), encode(settingsOf(remote)), resolve)
	if err != nil {
		return nil, nil, err
	}
	if pick == TakeRemote {
		out = settingsOf(remote)
	}
	if line != "" {
		report = append(report, line)
	}
	out.Sessions = map[string]*config.Session{}
	keys := map[string]bool{}
	for _, c := range []*config.Config{base, local, remote} {
		for k := range c.Sessions {
			keys[k] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(keys)) {
		pick, line, err := merge3("session "+id, encodeSession(base, id), encodeSession(local, id), encodeSession(remote, id), resolve)
		if err != nil {
			return nil, nil, err
		}
		src := local
		if pick == TakeRemote {
			src = remote
		}
		if s, ok := src.Sessions[id]; ok {
			out.Sessions[id] = s
		}
		if line != "" {
			report = append(report, line)
		}
	}
	return out, report, nil
}

func merge3(key string, b, l, r []byte, resolve Resolver) (Choice, string, error) {
	switch {
	case bytes.Equal(l, r):
		return KeepLocal, "", nil
	case bytes.Equal(l, b):
		return TakeRemote, key + ": changed remotely", nil
	case bytes.Equal(r, b):
		return KeepLocal, key + ": changed locally", nil
	}
	choice, err := resolve(Conflict{Key: key, Base: b, Local: l, Remote: r})
	if err != nil {
		return 0, "", err
	}
	side := "local"
	if choice == TakeRemote {
		side = "remote"
	}
	return choice, key + ": changed on both sides, kept " + side, nil
}

func settingsOf(c *config.Config) *config.Config {
	return &config.Config{
		Base:       c.Base,
		Workspaces: c.Workspaces,
		Tool:       c.Tool,
		Editor:     c.Editor,
		Protocol:   c.Protocol,
		Tools:      c.Tools,
		Sync:       c.Sync,
	}
}

func encode(v any) []byte {
	data, err := yaml.Dump(v, yaml.WithIndent(2))
	if err != nil {
		return []byte(err.Error())
	}
	return data
}

func encodeSession(c *config.Config, id string) []byte {
	s, ok := c.Sessions[id]
	if !ok {
		return nil
	}
	return encode(s)
}

// Diff renders a line diff of a and b, prefixing lines with "  ", "- " or
// "+ ". It is meant for small inputs such as one session.
func Diff(a, b []byte) string {
	la, lb := lines(a), lines(b)
	lcs := make([][]int, len(la)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(lb)+1)
	}
	for i := len(la) - 1; i >= 0; i-- {
		for j := len(lb) - 1; j >= 0; j-- {
			if la[i] == lb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var sb strings.Builder
	i, j := 0, 0
	for i < len(la) || j < len(lb) {
		switch {
		case i < len(la) && j < len(lb) && la[i] == lb[j]:
			sb.WriteString("  " + la[i] + "\n")
			i++
			j++
		case i < len(la) && (j == len(lb) || lcs[i+1][j] >= lcs[i][j+1]):
			sb.WriteString("- " + la[i] + "\n")
			i++
		default:
			sb.WriteString("+ " + lb[j] + "\n")
			j++
		}
	}
	return sb.String()
}

func lines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
