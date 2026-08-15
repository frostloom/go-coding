package verify

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/zlx05/go-coding/internal/sandbox"
)

var (
	// listenRe detects a port-binding server in the generated Go sources. It
	// also matches ListenAndServeTLS (the TLS form contains ListenAndServe).
	listenRe = regexp.MustCompile(`ListenAndServe`)
	// addrRe discovers listen addresses of the shape generated code produces:
	// "127.0.0.1:8090", "localhost:8080", ":8080", "0.0.0.0:3000".
	addrRe = regexp.MustCompile(`(?:127\.0\.0\.1|localhost|0\.0\.0\.0|::1)?:(\d{4,5})\b`)
)

// SmokeServer mirrors Claude Code's "run it and observe": when the task
// produced a port-binding server it builds that package, boots the binary
// inside the sandbox, probes GET / and every referenced asset over real HTTP,
// and reports anything that is not 200. Deterministic, zero LLM calls.
//
// touched is the set of files the task produced (paths relative to root, the
// sandbox baseline diff). Only those sources are scanned for a server — a
// pre-existing server elsewhere in the repository is never booted, because
// Claude Code verifies what it built, not the whole project. nil touched scans
// the whole workspace (the standalone primitive; the 12 library-fixture
// harness cases find no server and no-op either way).
func SmokeServer(ctx context.Context, root string, touched []string, sb *sandbox.Sandbox, timeout time.Duration) []string {
	if root == "" || sb == nil || timeout <= 0 {
		return nil
	}
	serverDir, ports := scanGoSources(root, touched)
	if serverDir == "" || len(ports) == 0 {
		return nil
	}

	bin, tmp, err := buildServer(ctx, sb, root, serverDir, timeout)
	if tmp != "" {
		defer os.RemoveAll(tmp)
	}
	if err != nil {
		return []string{"server build failed: " + err.Error()}
	}

	r, err := sb.Start(ctx, bin, nil)
	if err != nil {
		return []string{"server could not be started: " + err.Error()}
	}
	defer r.Kill()

	addrs := make([]string, 0, len(ports))
	for _, p := range ports {
		addrs = append(addrs, "127.0.0.1:"+p)
	}
	live := waitListening(r, addrs, pollBudget(timeout))
	if live == "" {
		return []string{"server did not start listening on any of " + strings.Join(addrs, ", ") +
			" within " + pollBudget(timeout).String() + outputTail(r)}
	}

	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := "http://" + live

	code, body, hops := getFollow(client, base+"/", maxRedirects)
	var issues []string
	switch {
	case code == 0:
		issues = append(issues, "GET / request failed")
	case code >= 300 && code < 400 && hops >= maxRedirects:
		issues = append(issues, fmt.Sprintf("GET / redirects forever (%d hops, last status %d) — a redirect loop", hops, code))
	case code != http.StatusOK:
		issues = append(issues, fmt.Sprintf("GET / returned %d, want 200", code))
	default:
		for _, ref := range extractAssets([]byte(body)) {
			u := resolveURL(base, ref)
			c, _, _ := getFollow(client, u, maxRedirects)
			if c != http.StatusOK {
				issues = append(issues, fmt.Sprintf("%s returned %d (referenced by /), want 200", ref, c))
			}
		}
	}
	return issues
}

// VerifyDeliverable combines the reference-integrity check and the server
// smoke check into one gate, scoped to touched — the files the task actually
// produced (relative paths from the sandbox baseline diff). A reference check
// that scanned the whole repository, or a smoke that booted an unrelated
// pre-existing server, would turn real edits on unrelated code into fake
// failures — the exact false-positive problem this scoping exists to avoid.
// Returns nil when the task produced no web assets and no port-binding server.
func VerifyDeliverable(ctx context.Context, root string, touched []string, sb *sandbox.Sandbox, timeout time.Duration) []string {
	if root == "" || sb == nil || timeout <= 0 || len(touched) == 0 {
		return nil
	}
	issues := CheckReferencesIn(root, touched)
	issues = append(issues, SmokeServer(ctx, root, touched, sb, timeout)...)
	return issues
}

const maxRedirects = 5

// scanGoSources finds the directory containing a ListenAndServe call and the
// set of candidate ports the produced code binds. Returns ("", nil) when no
// port-binding server is found. With touched == nil it scans the whole
// workspace (the standalone primitive); otherwise only the task's own files
// are considered, so a pre-existing server the task never touched is ignored.
func scanGoSources(root string, touched []string) (string, []string) {
	seenPort := map[string]bool{}
	var ports []string
	var serverDir string
	consider := func(path string) {
		if strings.ToLower(filepath.Ext(path)) != ".go" {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if !listenRe.Match(data) {
			return
		}
		if serverDir == "" {
			serverDir = filepath.Dir(path)
		}
		for _, m := range addrRe.FindAllSubmatch(data, -1) {
			p := string(m[1])
			if !seenPort[p] {
				seenPort[p] = true
				ports = append(ports, p)
			}
		}
	}
	if touched == nil {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			consider(path)
			return nil
		})
	} else {
		for _, rel := range touched {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if !within(root, p) {
				continue
			}
			consider(p)
		}
	}
	sort.Strings(ports)
	return serverDir, ports
}

// buildServer compiles the package containing the server out of the workspace
// root (so `git add -A` snapshots never sweep the binary in). It tries the
// server's own directory first, then the whole module as a fallback.
func buildServer(ctx context.Context, sb *sandbox.Sandbox, root, serverDir string, timeout time.Duration) (bin, tmp string, err error) {
	tmp, err = os.MkdirTemp("", "go-coding-smoke-*")
	if err != nil {
		return "", "", fmt.Errorf("temp dir: %v", err)
	}
	bin = filepath.Join(tmp, "smoke-server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	rel, rerr := filepath.Rel(root, serverDir)
	target := "."
	if rerr == nil && rel != "." {
		target = "./" + filepath.ToSlash(rel)
	}
	for _, t := range []string{target, "./..."} {
		out, berr := sb.Run(ctx, "go", []string{"build", "-o", bin, t}, minDur(timeout, 60*time.Second))
		if berr == nil {
			return bin, tmp, nil
		}
		err = fmt.Errorf("go build %s: %v\n%s", t, berr, out)
	}
	return "", tmp, err
}

// waitListening polls the candidate addresses until one accepts a TCP
// connection or the deadline passes. It also aborts early if the server
// process already exited (e.g. "bind: address already in use").
func waitListening(r *sandbox.Running, addrs []string, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if r.Exited() {
			return ""
		}
		for _, addr := range addrs {
			conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
			if err == nil {
				conn.Close()
				return addr
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ""
}

// getFollow issues a GET and, when the response is a 3xx, follows Location
// manually (CheckRedirect above stops the client's own following) for up to
// maxHops. Returns the final status code, the last response body, and how many
// hops were taken. code == 0 means the request failed outright.
func getFollow(client *http.Client, u string, maxHops int) (int, string, int) {
	hops := 0
	cur := u
	for {
		resp, err := client.Get(cur)
		if err != nil {
			return 0, "", hops
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && hops < maxHops {
			loc := resp.Header.Get("Location")
			if loc == "" {
				return resp.StatusCode, string(body), hops
			}
			cur = resolveURL(cur, loc)
			hops++
			continue
		}
		return resp.StatusCode, string(body), hops
	}
}

// extractAssets pulls the src/href file references out of an HTML body.
func extractAssets(html []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range htmlAttrRe.FindAllSubmatch(html, -1) {
		ref := string(m[1])
		if !referenceIsFile(ref) {
			continue
		}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func outputTail(r *sandbox.Running) string {
	if r.Exited() {
		return "\nserver output:\n" + truncate(r.Output(), 500)
	}
	return ""
}

func pollBudget(timeout time.Duration) time.Duration {
	if b := timeout / 3; b < 10*time.Second {
		return b
	}
	return 10 * time.Second
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…[truncated]"
}
