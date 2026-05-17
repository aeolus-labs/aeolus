package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// helper: build a minimal Config from upstream names + workspaces. No
// validation; the tests target the resolver, not Load.
func mkConfig(upstreams []string, workspaces ...Workspace) *Config {
	c := &Config{}
	for _, n := range upstreams {
		c.Upstreams = append(c.Upstreams, Upstream{Name: n, Transport: "stdio", Command: "true"})
	}
	c.Workspaces = workspaces
	return c
}

func TestResolveWorkspace_ExplicitWins(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b"},
		Workspace{Name: "alpha", Include: []string{"a"}},
		Workspace{Name: "beta", Include: []string{"b"}, CWDMatch: []string{"/tmp"}},
	)
	got := c.ResolveWorkspace("alpha", "/tmp", "")
	if got != "alpha" {
		t.Errorf("explicit should win, got %q", got)
	}
}

func TestResolveWorkspace_UnknownExplicitFallsThrough(t *testing.T) {
	c := mkConfig([]string{"a"}, Workspace{Name: "default", Include: []string{"a"}})
	got := c.ResolveWorkspace("nonsense", "", "")
	if got != "default" {
		t.Errorf("unknown explicit should fall through to default, got %q", got)
	}
}

func TestResolveWorkspace_CWDMatch(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "ws", Include: []string{"a"}, CWDMatch: []string{"/Users/me/code/proj"}},
	)
	if got := c.ResolveWorkspace("", "/Users/me/code/proj", ""); got != "ws" {
		t.Errorf("exact cwd match: got %q", got)
	}
	if got := c.ResolveWorkspace("", "/Users/me/code/other", ""); got != "" {
		t.Errorf("non-match should resolve to empty, got %q", got)
	}
}

func TestResolveWorkspace_CWDMatchDoubleStarSuffix(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "ws", Include: []string{"a"}, CWDMatch: []string{"/Users/me/code/proj/**"}},
	)
	cases := map[string]string{
		"/Users/me/code/proj":            "ws",
		"/Users/me/code/proj/services":   "ws",
		"/Users/me/code/proj/services/x": "ws",
		"/Users/me/code/proj-sibling":    "", // not a descendant
		"/Users/me/code":                 "", // parent
	}
	for cwd, want := range cases {
		if got := c.ResolveWorkspace("", cwd, ""); got != want {
			t.Errorf("/** match for %q: got %q, want %q", cwd, got, want)
		}
	}
}

func TestResolveWorkspace_LongestPrefixWins(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b"},
		Workspace{Name: "broad", Include: []string{"a"}, CWDMatch: []string{"/Users/me/code/**"}},
		Workspace{Name: "narrow", Include: []string{"b"}, CWDMatch: []string{"/Users/me/code/specific/**"}},
	)
	if got := c.ResolveWorkspace("", "/Users/me/code/specific/sub", ""); got != "narrow" {
		t.Errorf("longest prefix should win, got %q", got)
	}
	if got := c.ResolveWorkspace("", "/Users/me/code/other", ""); got != "broad" {
		t.Errorf("broader should still match when narrower doesn't, got %q", got)
	}
}

func TestResolveWorkspace_DefaultFallback(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "default", Include: []string{"a"}},
		Workspace{Name: "other", Include: []string{"a"}, CWDMatch: []string{"/somewhere"}},
	)
	if got := c.ResolveWorkspace("", "/elsewhere", ""); got != "default" {
		t.Errorf("should fall back to 'default' workspace, got %q", got)
	}
}

func TestResolveWorkspace_NoMatch(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "alpha", Include: []string{"a"}, CWDMatch: []string{"/somewhere"}},
	)
	if got := c.ResolveWorkspace("", "/elsewhere", ""); got != "" {
		t.Errorf("no match should resolve to empty string, got %q", got)
	}
}

func TestResolveWorkspace_ClientMatch(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "ws", Include: []string{"a"}, ClientMatch: []string{"claude"}},
	)
	// substring, case-insensitive
	if got := c.ResolveWorkspace("", "", "Claude Code 1.0.0"); got != "ws" {
		t.Errorf("client_match should match Claude Code, got %q", got)
	}
	if got := c.ResolveWorkspace("", "", "cursor 0.4"); got != "" {
		t.Errorf("non-matching client should not select ws, got %q", got)
	}
	// empty client identity must not auto-match a client-only ws
	if got := c.ResolveWorkspace("", "", ""); got != "" {
		t.Errorf("empty client should not match a client-only workspace, got %q", got)
	}
}

func TestResolveWorkspace_ClientAndCWDAreAND(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{
			Name:        "ws",
			Include:     []string{"a"},
			CWDMatch:    []string{"/Users/me/code/proj"},
			ClientMatch: []string{"claude"},
		},
	)
	// both signals match → selected
	if got := c.ResolveWorkspace("", "/Users/me/code/proj", "Claude Code"); got != "ws" {
		t.Errorf("both signals matching should pick ws, got %q", got)
	}
	// cwd matches but client doesn't → no match
	if got := c.ResolveWorkspace("", "/Users/me/code/proj", "cursor"); got != "" {
		t.Errorf("client mismatch should fail AND, got %q", got)
	}
	// client matches but cwd doesn't → no match
	if got := c.ResolveWorkspace("", "/other", "Claude Code"); got != "" {
		t.Errorf("cwd mismatch should fail AND, got %q", got)
	}
}

func TestResolveWorkspace_LongerCWDBeatsClientOnly(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b"},
		Workspace{Name: "byClient", Include: []string{"a"}, ClientMatch: []string{"claude"}},
		Workspace{Name: "byCwd", Include: []string{"b"}, CWDMatch: []string{"/Users/me/code/proj/**"}},
	)
	// CWDMatch is much more specific than a generic client identity,
	// so the cwd-based workspace should win.
	if got := c.ResolveWorkspace("", "/Users/me/code/proj/sub", "Claude Code"); got != "byCwd" {
		t.Errorf("longer cwd prefix should outrank client-only, got %q", got)
	}
	// If only the client signal applies, that workspace wins.
	if got := c.ResolveWorkspace("", "/elsewhere", "Claude Code"); got != "byClient" {
		t.Errorf("client-only ws should win when no cwd matches, got %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no HOME on this system")
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(~) = %q, want %q", got, home)
	}
	if got, want := expandHome("~/code/proj"), filepath.Join(home, "code/proj"); got != want {
		t.Errorf("expandHome(~/code/proj) = %q, want %q", got, want)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path should be untouched, got %q", got)
	}
	if got := expandHome("relative/path"); got != "relative/path" {
		t.Errorf("relative path should be untouched, got %q", got)
	}
}

func TestUnscopedUpstreams(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b", "c", "d"},
		Workspace{Name: "ws1", Include: []string{"a", "b"}},
		Workspace{Name: "ws2", Include: []string{"b", "c"}},
	)
	got := c.UnscopedUpstreams()
	want := []string{"d"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnscopedUpstreams: got %v, want %v", got, want)
	}
}

func TestUnscopedUpstreams_None(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b"},
		Workspace{Name: "ws1", Include: []string{"a", "b"}},
	)
	got := c.UnscopedUpstreams()
	if len(got) != 0 {
		t.Errorf("expected zero unscoped, got %v", got)
	}
}

func TestUnscopedUpstreams_All(t *testing.T) {
	c := mkConfig([]string{"a", "b"})
	got := c.UnscopedUpstreams()
	sort.Strings(got)
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnscopedUpstreams: got %v, want %v", got, want)
	}
}

func TestVisibleUpstreams_Empty(t *testing.T) {
	// profileName == "" → unscoped set.
	c := mkConfig(
		[]string{"a", "b", "c"},
		Workspace{Name: "ws", Include: []string{"a"}},
	)
	got := c.VisibleUpstreams("")
	sort.Strings(got)
	want := []string{"b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VisibleUpstreams(\"\"): got %v, want %v", got, want)
	}
}

func TestVisibleUpstreams_Known(t *testing.T) {
	c := mkConfig(
		[]string{"a", "b"},
		Workspace{Name: "ws", Include: []string{"a"}},
	)
	got := c.VisibleUpstreams("ws")
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("VisibleUpstreams(\"ws\"): got %v, want [a]", got)
	}
}

func TestVisibleUpstreams_Unknown(t *testing.T) {
	c := mkConfig([]string{"a"})
	if got := c.VisibleUpstreams("nope"); got != nil {
		t.Errorf("unknown workspace should return nil, got %v", got)
	}
}

func TestWorkspaceByName(t *testing.T) {
	c := mkConfig([]string{}, Workspace{Name: "found"}, Workspace{Name: "also"})
	if w := c.WorkspaceByName("found"); w == nil || w.Name != "found" {
		t.Errorf("WorkspaceByName(found) returned %v", w)
	}
	if w := c.WorkspaceByName("missing"); w != nil {
		t.Errorf("WorkspaceByName(missing) should be nil, got %v", w)
	}
}

func TestConfigValidate_WorkspaceUniqueNames(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "dup", Include: []string{"a"}},
		Workspace{Name: "dup", Include: []string{"a"}},
	)
	if err := c.validate(); err == nil {
		t.Error("duplicate workspace names should fail validation")
	}
}

func TestConfigValidate_WorkspaceIncludeMustExist(t *testing.T) {
	c := mkConfig(
		[]string{"a"},
		Workspace{Name: "ws", Include: []string{"does-not-exist"}},
	)
	if err := c.validate(); err == nil {
		t.Error("workspace including a missing upstream should fail validation")
	}
}

func TestConfigValidate_WorkspaceEmptyName(t *testing.T) {
	c := mkConfig([]string{}, Workspace{Name: "", Include: []string{}})
	if err := c.validate(); err == nil {
		t.Error("workspace with empty name should fail validation")
	}
}
