package main

import "testing"

func TestDeriveWorkspaceName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/vercel/next.js", "next.js"},
		{"https://github.com/owner/repo.git", "repo"},
		{"git@github.com:owner/my-project.git", "my-project"},
		{"https://github.com/owner/repo", "repo"},
		{"https://gitlab.com/group/subgroup/project", "project"},
		{"simple-name", "simple-name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := deriveWorkspaceName(tt.input)
			if got != tt.want {
				t.Errorf("deriveWorkspaceName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
