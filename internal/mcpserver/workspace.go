package mcpserver

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/zlx05/go-coding/internal/fsutil"
)

// sessionHeader is the HTTP header a client uses to identify its workspace
// session. The server mints one on first contact and returns it in the same
// header, so a host can echo it back on subsequent requests.
const sessionHeader = "X-Session-Id"

// WorkspaceManager gives every MCP client its own isolated workspace over
// HTTP. Each session id maps to a dedicated *Server rooted in a fresh temp
// directory — independent sandbox, symbol index and pending-edit store, so
// two editors/users working through the same mcp-server never touch each
// other's files. (stdio needs no manager: one client per process is already
// one workspace per process.)
//
// M8 story: stdio = one client, one workspace; HTTP + manager = N clients,
// N isolated workspaces.
type WorkspaceManager struct {
	mu        sync.Mutex
	workspace map[string]*Server
	baseRepo  string // optional template: each session starts as a copy of it
}

// NewWorkspaceManager builds a manager. baseRepo, when non-empty, is copied
// into every new session's workspace as a starting point (seeded project);
// otherwise sessions start empty.
func NewWorkspaceManager(baseRepo string) *WorkspaceManager {
	return &WorkspaceManager{
		workspace: make(map[string]*Server),
		baseRepo:  baseRepo,
	}
}

// Server returns the isolated *Server for a session, creating its workspace
// on first use.
func (wm *WorkspaceManager) Server(sessionID string) (*Server, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if s, ok := wm.workspace[sessionID]; ok {
		return s, nil
	}
	ws, err := os.MkdirTemp("", "go-coding-ws-*")
	if err != nil {
		return nil, err
	}
	if wm.baseRepo != "" {
		if err := fsutil.CopyDir(wm.baseRepo, ws); err != nil {
			return nil, err
		}
	}
	s, err := New(ws)
	if err != nil {
		return nil, err
	}
	wm.workspace[sessionID] = s
	return s, nil
}

// ServeHTTP is the multi-client HTTP transport: each request is routed to
// the Server for its session, minting a fresh session id when the client
// does not send one.
func (wm *WorkspaceManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed: use POST", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.Header.Get(sessionHeader)
	if sessionID == "" {
		sessionID = newSessionID()
	}
	s, err := wm.Server(sessionID)
	if err != nil {
		http.Error(w, "workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp := s.Handle(bytes.TrimSpace(body))
	if resp == nil {
		w.WriteHeader(http.StatusAccepted) // notification accepted
		return
	}
	w.Header().Set(sessionHeader, sessionID)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// newSessionID mints a cryptographically random session id.
func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sess-unknown"
	}
	return "sess-" + hex.EncodeToString(b[:])
}
