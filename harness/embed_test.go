package harnessassets

import (
	"strings"
	"testing"
)

func TestCodexProfileAllowsLocalBindingButNotGitHub(t *testing.T) {
	profile := string(CodexProfile)
	// 既存のテストは httptest でループバックに待ち受けるので、実装担当の検証で必要になる。
	if !strings.Contains(profile, "allow_local_binding = true") {
		t.Error("the profile must allow local binding for tests")
	}
	for _, line := range []string{`"github.com" = "deny"`, `"*.github.com" = "deny"`, `"*.githubusercontent.com" = "deny"`} {
		if !strings.Contains(profile, line) {
			t.Errorf("missing %s", line)
		}
	}
}
