package orch

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise real Git's HTTPS authentication and stored origin against a private
// loopback fixture. The CONNECT proxy only tunnels to our local TLS server;
// github.com is never contacted. No credential is recorded in diagnostics.
func TestResolveRepoPrivateHTTPSAuthentication(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	upstream := filepath.Join(root, "owner", "repo.git")
	makeUpstream(t, upstream)
	publishPRRef(t, upstream, 7, "private fixture")
	token := "dummy-credential-for-test"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GITHUB_APP_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("PR_AF_WORKDIR", filepath.Join(root, "workspaces"))
	var authenticated atomic.Int32
	backend := &cgi.Handler{Path: realGit, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1",
	}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Basic " + encoded
		legacy := "Basic " + base64.StdEncoding.EncodeToString([]byte(token+":"))
		if r.Header.Get("Authorization") != want && r.Header.Get("Authorization") != legacy {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	destination := server.Listener.Addr().String()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "github.com:443" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		remote, err := net.DialTimeout("tcp", destination, time.Second)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer remote.Close()
		client, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		_ = remote.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buf.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, buf); _ = remote.Close(); close(done) }()
		_, _ = io.Copy(client, remote)
		_ = client.Close()
		<-done
	}))
	defer proxy.Close()
	// Isolate the fixture from system credential helpers, URL rewrites and proxies.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "3")
	t.Setenv("GIT_CONFIG_KEY_0", "http.proxy")
	t.Setenv("GIT_CONFIG_VALUE_0", proxy.URL)
	t.Setenv("GIT_CONFIG_KEY_1", "http.sslVerify")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")
	t.Setenv("GIT_CONFIG_KEY_2", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_2", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	shim := filepath.Join(root, "shim")
	if err := os.Mkdir(shim, 0700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root, "argument-verdict")
	t.Setenv("FIXTURE_REAL_GIT", realGit)
	t.Setenv("FIXTURE_ARG_REPORT", report)
	script := `#!/bin/sh
for arg do
 case "$arg" in *"$GH_TOKEN"*|*x-access-token:*|*://*@*) printf 'unsafe\n' >> "$FIXTURE_ARG_REPORT";; esac
done
exec "$FIXTURE_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir, err := ResolveRepo(ctx, "", "https://github.com/owner/repo/pull/7")
	if err != nil {
		t.Fatal("private HTTPS fixture clone or checkout failed")
	}
	if authenticated.Load() == 0 {
		t.Fatal("private clone did not authenticate")
	}
	if got := readFile(t, filepath.Join(dir, "marker.txt")); got != "private fixture\n" {
		t.Fatal("private PR checkout content changed")
	}
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || strings.Contains(string(config), encoded) || strings.Contains(string(config), "x-access-token:") {
		t.Error("private clone stored credential material")
	}
	if _, err := os.Stat(report); err == nil {
		t.Error("private clone Git arguments contained credential material")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
