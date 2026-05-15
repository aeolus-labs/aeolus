package proxy

import (
	"testing"

	"github.com/aeolus-labs/aeolus/internal/config"
)

func TestToolFilter_Allowed(t *testing.T) {
	tests := []struct {
		name    string
		allow   []string
		deny    []string
		tool    string
		want    bool
	}{
		// No rules → everything allowed.
		{"empty rules, allowed", nil, nil, "filesystem.read_file", true},

		// Exact matches.
		{"exact allow", []string{"filesystem.read_file"}, nil, "filesystem.read_file", true},
		{"exact allow miss", []string{"filesystem.read_file"}, nil, "filesystem.write_file", false},
		{"exact deny", nil, []string{"github.delete_repo"}, "github.delete_repo", false},
		{"exact deny miss", nil, []string{"github.delete_repo"}, "github.create_repo", true},

		// Globs.
		{"glob allow", []string{"filesystem.read_*"}, nil, "filesystem.read_file", true},
		{"glob allow miss", []string{"filesystem.read_*"}, nil, "filesystem.write_file", false},
		{"glob deny", nil, []string{"github.delete_*"}, "github.delete_branch", false},
		{"namespace glob", []string{"filesystem.*"}, nil, "filesystem.read_file", true},
		{"namespace glob other ns", []string{"filesystem.*"}, nil, "github.read_file", false},

		// Deny beats allow.
		{"deny overrides allow", []string{"filesystem.*"}, []string{"filesystem.write_*"}, "filesystem.write_file", false},
		{"deny overrides allow exact", []string{"filesystem.*"}, []string{"filesystem.read_file"}, "filesystem.read_file", false},

		// Empty allow with deny: everything not denied passes.
		{"deny-only, passes", nil, []string{"github.delete_*"}, "github.create_repo", true},
		{"deny-only, blocks", nil, []string{"github.delete_*"}, "github.delete_repo", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewToolFilter(config.Tools{Allow: tt.allow, Deny: tt.deny})
			got := f.Allowed(tt.tool)
			if got != tt.want {
				t.Errorf("Allowed(%q) with allow=%v deny=%v = %v, want %v",
					tt.tool, tt.allow, tt.deny, got, tt.want)
			}
		})
	}
}
