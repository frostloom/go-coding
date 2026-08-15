package verify

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zlx05/go-coding/internal/sandbox"
)

// pickPort reserves a free TCP port for a fixture server to bind. Slightly
// racy (another process could grab it before the server starts) but fine for
// tests.
func pickPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		write(t, dir, name, content)
	}
	return dir
}

const goodMain = `package main

import "net/http"

const serverAddr = "127.0.0.1:PORT"

func main() {
	http.Handle("/", http.FileServer(http.Dir("static")))
	http.ListenAndServe(serverAddr, nil)
}
`

const loopMain = `package main

import "net/http"

const serverAddr = "127.0.0.1:PORT"

func main() {
	fs := http.FileServer(http.Dir("static"))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			r.URL.Path = "/index.html"
		}
		fs.ServeHTTP(w, r)
	})
	http.ListenAndServe(serverAddr, nil)
}
`

const indexHTML = `<html><head><link href="/style.css"><script src="/app.js"></script></head><body>calc</body></html>`

func TestScanGoSources(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go",
		`package main
import "net/http"
const serverAddr = "127.0.0.1:8090"
func main() { http.ListenAndServe(serverAddr, nil) }`)
	serverDir, ports := scanGoSources(dir, nil)
	if serverDir == "" {
		t.Fatal("expected a server dir")
	}
	if len(ports) != 1 || ports[0] != "8090" {
		t.Fatalf("ports = %v, want [8090]", ports)
	}
}

func TestScanGoSourcesNoServer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "lib.go", "package lib\nfunc Add(a, b int) int { return a + b }")
	if serverDir, ports := scanGoSources(dir, nil); serverDir != "" || ports != nil {
		t.Fatalf("expected no server, got dir=%q ports=%v", serverDir, ports)
	}
}

func TestSmokeServerGood(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":             "module smoke.test\n\ngo 1.25\n",
		"main.go":            strings.ReplaceAll(goodMain, "PORT", port),
		"static/index.html":  indexHTML,
		"static/app.js":      "console.log(1)",
		"static/style.css":   "body{}",
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := SmokeServer(context.Background(), dir, nil, sb, 30*time.Second)
	if len(issues) != 0 {
		t.Fatalf("expected clean smoke, got:\n%s", strings.Join(issues, "\n"))
	}
}

func TestSmokeServerRedirectLoop(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":            "module smoke.test\n\ngo 1.25\n",
		"main.go":           strings.ReplaceAll(loopMain, "PORT", port),
		"static/index.html": `<html><body>calc</body></html>`,
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := SmokeServer(context.Background(), dir, nil, sb, 30*time.Second)
	wantIssue(t, issues, "redirects forever")
}

func TestSmokeServerMissingAsset(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":            "module smoke.test\n\ngo 1.25\n",
		"main.go":           strings.ReplaceAll(goodMain, "PORT", port),
		"static/index.html": `<script src="/script.js"></script>`,
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := SmokeServer(context.Background(), dir, nil, sb, 30*time.Second)
	wantIssue(t, issues, "/script.js")
}

func TestSmokeServerNoServer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "lib.go", "package lib\nfunc Add(a, b int) int { return a + b }")
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if issues := SmokeServer(context.Background(), dir, nil, sb, 30*time.Second); issues != nil {
		t.Fatalf("expected no-op, got %v", issues)
	}
}

func TestVerifyDeliverableCombines(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":             "module smoke.test\n\ngo 1.25\n",
		"main.go":            strings.ReplaceAll(goodMain, "PORT", port),
		"static/index.html":  indexHTML,
		"static/style.css":   "body{}", // app.js referenced but missing
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The task produced the server and the page; the diff yields these files.
	touched := []string{"main.go", "static/index.html", "static/style.css"}
	issues := VerifyDeliverable(context.Background(), dir, touched, sb, 30*time.Second)
	var ref, smoke bool
	for _, s := range issues {
		if strings.Contains(s, "/app.js") {
			ref = true
		}
		if strings.Contains(s, "returned") {
			smoke = true
		}
	}
	if !ref || !smoke {
		t.Errorf("expected both reference check and smoke to flag app.js, got:\n%v", issues)
	}
}

// TestVerifyDeliverableIgnoresUntouchedServer is the false-positive guard the
// user asked for: the repository contains a server (even a broken one), but the
// task only touched a library file. The gate must return nil — Claude Code
// verifies what it built, so an unrelated pre-existing server is never booted
// and a real edit on unrelated code can't be turned into a fake failure.
func TestVerifyDeliverableIgnoresUntouchedServer(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":             "module smoke.test\n\ngo 1.25\n",
		"main.go":            strings.ReplaceAll(loopMain, "PORT", port), // broken, but untouched by the task
		"static/index.html":  `<html><body>calc</body></html>`,
		"calc/lib.go":        "package calc\nfunc Add(a, b int) int { return a + b }",
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if issues := VerifyDeliverable(context.Background(), dir, []string{"calc/lib.go"}, sb, 30*time.Second); issues != nil {
		t.Fatalf("expected no issues: the pre-existing server was never touched, got:\n%v", issues)
	}
}

// TestVerifyDeliverableBootsTouchedServer is the mirror: when the task DID
// produce the server, the same fixture fires the smoke gate and reports the
// redirect loop. Together the two tests pin the exact boundary — verify the
// deliverable, never the whole repository.
func TestVerifyDeliverableBootsTouchedServer(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":             "module smoke.test\n\ngo 1.25\n",
		"main.go":            strings.ReplaceAll(loopMain, "PORT", port),
		"static/index.html":  `<html><body>calc</body></html>`,
		"calc/lib.go":        "package calc\nfunc Add(a, b int) int { return a + b }",
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := VerifyDeliverable(context.Background(), dir, []string{"main.go"}, sb, 30*time.Second)
	wantIssue(t, issues, "redirects forever")
}

// TestVerifyDeliverableUntouchedHTMLNoRefs: the reference check is scoped to
// the files the task touched, so an untouched HTML page with a broken link is
// the previous task's business — the reference scan must not flag it. (The
// smoke check is deliberately not involved here: when the task DID touch the
// server, the page that server serves is fair game, and a 404 there is real.)
func TestVerifyDeliverableUntouchedHTMLNoRefs(t *testing.T) {
	port := pickPort(t)
	dir := writeModule(t, map[string]string{
		"go.mod":            "module smoke.test\n\ngo 1.25\n",
		"main.go":           strings.ReplaceAll(goodMain, "PORT", port),
		"static/index.html": `<script src="/script.js"></script>`, // broken, but untouched
		"static/style.css":  "body{}",
		"calc/lib.go":       "package calc\nfunc Add(a, b int) int { return a + b }",
	})
	sb, err := sandbox.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	issues := VerifyDeliverable(context.Background(), dir, []string{"calc/lib.go"}, sb, 30*time.Second)
	if issues != nil {
		t.Fatalf("expected no issues: no web asset was touched, got:\n%v", issues)
	}
}
