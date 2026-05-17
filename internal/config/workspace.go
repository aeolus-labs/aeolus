package config

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkspace picks a workspace name for an MCP client based on
// (in order):
//
//  1. explicit name (from the bridge's --workspace flag)
//  2. auto-match: walk workspaces and pick the best match across
//     CWDMatch + ClientMatch. A workspace matches when every signal
//     it defines matches the supplied value (AND across cwd & client).
//     Workspaces with no auto-match signals at all never auto-match.
//  3. a workspace literally named "default" if defined
//
// Returns the empty string when nothing matches — callers treat that
// as "no workspace selected, expose only unscoped upstreams".
func (c *Config) ResolveWorkspace(explicit, cwd, client string) string {
	if explicit != "" {
		if w := c.WorkspaceByName(explicit); w != nil {
			return w.Name
		}
		// Explicit workspace that doesn't exist falls through to defaulting.
	}
	if w := c.autoMatchWorkspace(cwd, client); w != nil {
		return w.Name
	}
	if w := c.WorkspaceByName("default"); w != nil {
		return w.Name
	}
	return ""
}

// autoMatchWorkspace finds the best-scoring workspace that satisfies
// every signal it defines. A workspace with CWDMatch alone is matched
// by cwd; with ClientMatch alone, by client; with both, by both (AND).
// A workspace with neither never auto-matches.
//
// Scoring rewards specificity:
//   - CWDMatch contributes the matched prefix length.
//   - ClientMatch contributes a small constant bump so a client-only
//     workspace can still win against an empty workspace, but a more
//     specific path beats a generic "any Cursor session" rule.
func (c *Config) autoMatchWorkspace(cwd, client string) *Workspace {
	clean := ""
	if cwd != "" {
		clean = filepath.Clean(cwd)
	}
	var best *Workspace
	bestScore := -1
	for i := range c.Workspaces {
		w := &c.Workspaces[i]
		if len(w.CWDMatch) == 0 && len(w.ClientMatch) == 0 {
			continue
		}

		cwdScore := -1
		if len(w.CWDMatch) > 0 {
			if clean == "" {
				continue // workspace needs a cwd, caller gave us none
			}
			cwdScore = bestCWDScore(w.CWDMatch, clean)
			if cwdScore < 0 {
				continue
			}
		}

		clientScore := -1
		if len(w.ClientMatch) > 0 {
			if client == "" {
				continue
			}
			if !anyClientMatch(w.ClientMatch, client) {
				continue
			}
			clientScore = 1
		}

		score := 0
		if cwdScore >= 0 {
			score += cwdScore
		}
		if clientScore > 0 {
			// Small bonus so client-only matches beat zero, but a
			// long cwd prefix still wins over a generic client match.
			score += 1
		}
		if score > bestScore {
			bestScore = score
			best = w
		}
	}
	return best
}

func bestCWDScore(patterns []string, cwd string) int {
	best := -1
	for _, pat := range patterns {
		if s := matchCWD(expandHome(pat), cwd); s > best {
			best = s
		}
	}
	return best
}

func anyClientMatch(patterns []string, client string) bool {
	cl := strings.ToLower(client)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.Contains(cl, p) {
			return true
		}
	}
	return false
}

// matchCWD returns a match score (length of matched prefix) or -1 if
// no match. Used to pick the most specific pattern across workspaces.
func matchCWD(pattern, cwd string) int {
	pattern = filepath.Clean(pattern)
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		base = filepath.Clean(base)
		if cwd == base || strings.HasPrefix(cwd, base+string(filepath.Separator)) {
			return len(base)
		}
		return -1
	}
	if cwd == pattern {
		return len(pattern)
	}
	return -1
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// VisibleUpstreams returns the names of upstreams a session bound to
// workspaceName should see.
//
//   - workspaceName == ""  → only upstreams that aren't included in
//                            any workspace (the "unscoped" set). The
//                            point of scoping is to box a server off
//                            from the global view, so a scoped server
//                            should never appear here.
//   - workspaceName matches → that workspace's Include list.
//   - workspaceName unknown → nil (caller treats as "expose every
//                             upstream" — permissive fallback so a
//                             broken config doesn't leave the user
//                             with zero tools).
func (c *Config) VisibleUpstreams(workspaceName string) []string {
	if workspaceName == "" {
		return c.UnscopedUpstreams()
	}
	w := c.WorkspaceByName(workspaceName)
	if w == nil {
		return nil
	}
	out := make([]string, 0, len(w.Include))
	out = append(out, w.Include...)
	return out
}

// UnscopedUpstreams returns the names of upstreams that aren't members
// of any workspace. These are the servers a "global" MCP client (no
// --workspace flag, no cwd_match) is allowed to see — scoped servers
// stay invisible globally so scoping actually achieves isolation.
func (c *Config) UnscopedUpstreams() []string {
	scoped := make(map[string]struct{})
	for _, w := range c.Workspaces {
		for _, n := range w.Include {
			scoped[n] = struct{}{}
		}
	}
	out := make([]string, 0, len(c.Upstreams))
	for _, u := range c.Upstreams {
		if _, isScoped := scoped[u.Name]; !isScoped {
			out = append(out, u.Name)
		}
	}
	return out
}

func (c *Config) upstreamNames() []string {
	out := make([]string, 0, len(c.Upstreams))
	for _, u := range c.Upstreams {
		out = append(out, u.Name)
	}
	return out
}
