package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	registryBaseURL = "https://registry.modelcontextprotocol.io/v0"
	registryTimeout = 30 * time.Second
)

// CatalogEntry describes one MCP server the UI can offer to add.
// Env / Header values use {{PLACEHOLDER}} syntax for fields the user must
// fill in. Transport defaults to "stdio" when Command is set; "http" when
// URL is set.
type CatalogEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Transport   string `json:"transport,omitempty"`

	// stdio
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// http
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	Notes string `json:"notes,omitempty"`
}

// Wire types matching the registry response.

type registryListResponse struct {
	Servers  []registryEntry `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
		Count      int    `json:"count"`
	} `json:"metadata"`
}

type registryEntry struct {
	Server registryServer            `json:"server"`
	Meta   map[string]map[string]any `json:"_meta"`
}

type registryServer struct {
	Name        string              `json:"name"`
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Version     string              `json:"version"`
	Repository  *registryRepository `json:"repository,omitempty"`
	WebsiteURL  string              `json:"websiteUrl,omitempty"`
	Packages    []registryPackage   `json:"packages,omitempty"`
	Remotes     []registryRemote    `json:"remotes,omitempty"`
}

type registryRepository struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type registryPackage struct {
	RegistryType         string                  `json:"registryType"`
	Identifier           string                  `json:"identifier"`
	Version              string                  `json:"version"`
	Transport            registryTransport       `json:"transport"`
	EnvironmentVariables []registryEnvVar        `json:"environmentVariables,omitempty"`
}

type registryTransport struct {
	Type string `json:"type"`
}

type registryEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

type registryRemote struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// fetchRegistry walks the MCP registry pagination and returns a CatalogEntry
// per latest-version server that has an npm/stdio package we can spawn.
// Returns an error only if the first page fails; partial fetches are tolerated.
func fetchRegistry(ctx context.Context, client *http.Client) ([]CatalogEntry, error) {
	if client == nil {
		client = &http.Client{Timeout: registryTimeout}
	}
	ctx, cancel := context.WithTimeout(ctx, registryTimeout)
	defer cancel()

	cursor := ""
	entries := make([]CatalogEntry, 0, 256)
	seenNames := make(map[string]bool)
	pages := 0

	for {
		pages++
		page, err := fetchRegistryPage(ctx, client, cursor)
		if err != nil {
			if pages == 1 {
				return nil, err
			}
			break // tolerate partial failure on later pages
		}
		for _, e := range page.Servers {
			if !isLatest(e) {
				continue
			}
			ce, ok := mapEntry(e.Server)
			if !ok {
				continue
			}
			if seenNames[ce.ID] {
				continue
			}
			seenNames[ce.ID] = true
			entries = append(entries, ce)
		}
		if page.Metadata.NextCursor == "" {
			break
		}
		cursor = page.Metadata.NextCursor
		if pages > 200 {
			break // safety net against runaway pagination
		}
	}
	return entries, nil
}

func fetchRegistryPage(ctx context.Context, client *http.Client, cursor string) (*registryListResponse, error) {
	if client == nil {
		client = &http.Client{Timeout: registryTimeout}
	}
	u, err := url.Parse(registryBaseURL + "/servers")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("limit", "100")
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "aeolus/0.3.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry %s: %s", u.String(), resp.Status)
	}
	var out registryListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("registry decode: %w", err)
	}
	return &out, nil
}

// isLatest returns true if the entry is the latest version of its server,
// using the official registry metadata. Older versions are skipped to keep
// the catalog clean.
func isLatest(e registryEntry) bool {
	official, ok := e.Meta["io.modelcontextprotocol.registry/official"]
	if !ok {
		return true // no metadata, include
	}
	latest, ok := official["isLatest"].(bool)
	if !ok {
		return true
	}
	return latest
}

// mapEntry converts a registry server into a CatalogEntry. Prefers an
// npm/stdio package (most reliable to spawn locally); falls back to a
// streamable-http remote when no npm package is available. Returns false
// when neither is usable.
func mapEntry(s registryServer) (CatalogEntry, bool) {
	pkg, ok := pickPackage(s.Packages)
	if !ok {
		for _, r := range s.Remotes {
			if r.Type == "streamable-http" && r.URL != "" {
				return CatalogEntry{
					ID:          s.Name,
					Name:        displayName(s),
					Description: s.Description,
					Transport:   "http",
					URL:         r.URL,
				}, true
			}
		}
		return CatalogEntry{}, false
	}

	// Use the bare package identifier so npx pulls whatever is latest on npm.
	// Pinning registry-supplied versions sometimes points at refactored or
	// non-executable revisions; latest tends to be the actively maintained
	// runnable artifact.
	identifier := pkg.Identifier

	env := map[string]string{}
	for _, ev := range pkg.EnvironmentVariables {
		placeholder := "{{" + ev.Name + "}}"
		env[ev.Name] = placeholder
	}

	notes := ""
	if len(pkg.EnvironmentVariables) > 0 {
		var required []string
		for _, ev := range pkg.EnvironmentVariables {
			if ev.IsRequired {
				required = append(required, ev.Name)
			}
		}
		if len(required) > 0 {
			notes = "Required env: " + strings.Join(required, ", ")
		}
	}

	return CatalogEntry{
		ID:          s.Name,
		Name:        displayName(s),
		Description: s.Description,
		Transport:   "stdio",
		Command:     "npx",
		Args:        []string{"-y", identifier},
		Env:         env,
		Notes:       notes,
	}, true
}

func pickPackage(pkgs []registryPackage) (registryPackage, bool) {
	for _, p := range pkgs {
		if p.RegistryType == "npm" && (p.Transport.Type == "" || p.Transport.Type == "stdio") {
			return p, true
		}
	}
	return registryPackage{}, false
}

func displayName(s registryServer) string {
	if s.Title != "" {
		return s.Title
	}
	// io.github.foo/bar-server  →  bar-server
	if s.Name != "" {
		base := path.Base(s.Name)
		if base != "" && base != "." {
			return base
		}
	}
	return s.Name
}
