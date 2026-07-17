package main

import "testing"

func TestEnsureBodyClosesIssue(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		branch string
		want   string
	}{
		{
			name:   "golemic branch without keyword appends Closes",
			body:   "Implements issue #6",
			branch: "golemic/issue-6",
			want:   "Implements issue #6\n\nCloses #6\n",
		},
		{
			name:   "golemic branch already has Closes keyword unchanged",
			body:   "Body text\n\nCloses #6",
			branch: "golemic/issue-6",
			want:   "Body text\n\nCloses #6",
		},
		{
			name:   "golemic branch with Fixes keyword unchanged",
			body:   "Fixes #6 now",
			branch: "golemic/issue-6",
			want:   "Fixes #6 now",
		},
		{
			name:   "keyword for different issue still appends",
			body:   "Closes #7",
			branch: "golemic/issue-6",
			want:   "Closes #7\n\nCloses #6\n",
		},
		{
			name:   "non-golemic branch left untouched",
			body:   "Description",
			branch: "feature/my-branch",
			want:   "Description",
		},
		{
			name:   "malformed golemic branch left untouched",
			body:   "Description",
			branch: "golemic/issue-abc",
			want:   "Description",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ensureBodyClosesIssue(c.body, c.branch)
			if got != c.want {
				t.Errorf("ensureBodyClosesIssue(%q, %q) = %q, want %q", c.body, c.branch, got, c.want)
			}
		})
	}
}
