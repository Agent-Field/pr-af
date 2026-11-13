package orch

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// All credential material in these fixtures is a synthetic sentinel. The shim
// records only verdicts and a simulated origin, never a process environment.
func installAuthGit(t *testing.T) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	sentinel := "synthetic-git-fixture-token"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + sentinel))
	report := filepath.Join(tmp, "report")
	t.Setenv("GH_TOKEN", sentinel)
	t.Setenv("FIXTURE_AUTH", encoded)
	t.Setenv("FIXTURE_REPORT", report)
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "never")
	t.Setenv("GIT_CONFIG_KEY_1", "http.https://github.com/.extraheader")
	t.Setenv("GIT_CONFIG_VALUE_1", "fixture previous header")
	t.Setenv("PR_AF_WORKDIR", filepath.Join(tmp, "workspaces"))
	script := `#!/bin/sh
for arg do
 case "$arg" in *"$GH_TOKEN"*|*"$FIXTURE_AUTH"*) printf 'argv=bad\n' >> "$FIXTURE_REPORT";; esac
done
auth=no
headers=0
i=0
while [ "$i" -lt "${GIT_CONFIG_COUNT:-0}" ]; do
 eval "key=\${GIT_CONFIG_KEY_$i-}"
 eval "value=\${GIT_CONFIG_VALUE_$i-}"
 if [ "$key" = http.https://github.com/.extraheader ]; then
  if [ -z "$value" ]; then headers=0; else headers=$((headers+1)); fi
  if [ "$value" = "Authorization: Basic $FIXTURE_AUTH" ]; then auth=ok; fi
 fi
 i=$((i+1))
done
if [ "$headers" -ne 1 ]; then auth=duplicate; fi
printf 'auth=%s\n' "$auth" >> "$FIXTURE_REPORT"
if [ "$GIT_CONFIG_KEY_0" = protocol.file.allow ] && [ "$GIT_CONFIG_VALUE_0" = never ]; then printf 'inherited=ok\n' >> "$FIXTURE_REPORT"; fi
if [ "${FIXTURE_FAIL:-}" = 1 ]; then
 printf 'fixture diagnostic: %s %s\n' "$GH_TOKEN" "$FIXTURE_AUTH" >&2
 exit 1
fi
if [ "$1" = clone ]; then
 previous=
 for arg do url=$previous; previous=$arg; done
 mkdir -p "$previous/.git"
 printf '[remote "origin"]\n url = %s\n' "$url" > "$previous/.git/config"
fi
`
	if err := os.WriteFile(filepath.Join(tmp, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	return report, sentinel
}

func TestResolveRepoKeepsCredentialsInEnvironment(t *testing.T) {
	report, sentinel := installAuthGit(t)
	for _, name := range []string{"fresh", "reused"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			dir, err := ResolveRepo(ctx, "", "https://github.com/owner/repo/pull/7")
			if err != nil {
				t.Fatal("workspace resolution failed with synthetic authentication")
			}
			origin, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(origin), sentinel) {
				t.Error("persisted origin contains synthetic credential")
			}
			if !strings.Contains(string(origin), "https://github.com/owner/repo.git") {
				t.Error("persisted origin did not keep the plain remote")
			}
		})
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	status := string(b)
	if strings.Contains(status, "argv=bad") {
		t.Error("Git argv contained synthetic credential")
	}
	if strings.Contains(status, "auth=no") || strings.Count(status, "auth=ok") != 6 {
		t.Errorf("environment authentication missing on fresh/reused Git operations: %s", status)
	}
	if strings.Count(status, "inherited=ok") != 6 {
		t.Error("existing process-only Git configuration was lost")
	}
}

func TestResolveRepoRedactsCredentialDiagnostics(t *testing.T) {
	_, sentinel := installAuthGit(t)
	t.Setenv("FIXTURE_FAIL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := ResolveRepo(ctx, "", "https://github.com/owner/repo/pull/7")
	if err == nil {
		t.Fatal("expected fixture clone failure")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + sentinel))
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), encoded) {
		t.Fatal("clone error contained synthetic credential material")
	}
	if !strings.Contains(err.Error(), "fixture diagnostic") {
		t.Fatal("redaction lost nonsensitive error detail")
	}
}
