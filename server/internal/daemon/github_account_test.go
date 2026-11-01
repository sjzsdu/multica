package daemon

import "testing"

func TestGitHubAccountForTask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		repos []RepoData
		want  string
		fail  bool
	}{
		{name: "no preference", repos: []RepoData{{URL: "git@github.com:org/repo.git"}}},
		{name: "one preference", repos: []RepoData{{URL: "git@github-work:org/repo.git", GitHubAccount: "work-user"}}, want: "work-user"},
		{name: "same preference across repos", repos: []RepoData{{GitHubAccount: "work-user"}, {GitHubAccount: "work-user"}}, want: "work-user"},
		{name: "conflicting preferences", repos: []RepoData{{GitHubAccount: "work-user"}, {GitHubAccount: "personal-user"}}, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := githubAccountForTask(tt.repos)
			if tt.fail {
				if err == nil {
					t.Fatal("expected conflict error")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("githubAccountForTask() = (%q, %v), want (%q, nil)", got, err, tt.want)
			}
		})
	}
}
