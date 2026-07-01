package cmd

import "testing"

// TestValidateManagedOrigin_Accepts confirms the canonical constructed origin (the
// only shape ferry ever builds) passes: https://github.com/<owner>/<name>.git.
func TestValidateManagedOrigin_Accepts(t *testing.T) {
	owner, name := "octocat", "myrepo"
	ok := []string{
		"https://github.com/octocat/myrepo.git",
		"https://github.com/octocat/myrepo", // .git-less canonical is also valid
	}
	for _, u := range ok {
		if err := validateManagedOrigin(u, owner, name); err != nil {
			t.Errorf("validateManagedOrigin rejected a canonical origin %q: %v", u, err)
		}
	}
}

// TestValidateManagedOrigin_Rejects is the defense-in-depth regression: a
// userinfo-embedded token, a wrong-owner path, a non-github host, and a URL with a
// query must ALL be rejected — none may ever be set as origin. All tokens FAKE.
func TestValidateManagedOrigin_Rejects(t *testing.T) {
	owner, name := "octocat", "myrepo"
	bad := map[string]string{
		"userinfo-token":  "https://ghp_deadbeefFAKE0123@github.com/octocat/myrepo.git",
		"userinfo-userpw": "https://user:pass@github.com/octocat/myrepo.git",
		"wrong-owner":     "https://github.com/someoneelse/myrepo.git",
		"wrong-repo":      "https://github.com/octocat/other.git",
		"non-github-host": "https://evil.example.com/octocat/myrepo.git",
		"host-with-port":  "https://github.com:8443/octocat/myrepo.git",
		"has-query":       "https://github.com/octocat/myrepo.git?token=x",
		"has-fragment":    "https://github.com/octocat/myrepo.git#frag",
		"http-scheme":     "http://github.com/octocat/myrepo.git",
		"ssh-scheme":      "ssh://git@github.com/octocat/myrepo.git",
	}
	for label, u := range bad {
		if err := validateManagedOrigin(u, owner, name); err == nil {
			t.Errorf("validateManagedOrigin[%s]: accepted a bad origin %q (must reject)", label, u)
		}
	}
}
