package mcpserver

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
)

// ServeIO runs the stdio transport: read newline-delimited JSON-RPC messages
// from r, write responses (also newline-delimited) to w. This is the MCP
// local transport — the host (VSCode, Claude Desktop, ...) spawns this binary
// and talks to it over the pipes.
func (s *Server) ServeIO(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	// File content can be large; grow the scanner buffer well past the 64KB
	// default so read_file/edit_file with big bodies still work.
	sc.Buffer(make([]byte, 1<<20), 64<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.Handle(line)
		if resp == nil {
			continue // notification — no response expected
		}
		if _, err := w.Write(append(resp, '\n')); err != nil {
			return err
		}
		if f, ok := w.(*bufio.Writer); ok {
			if err := f.Flush(); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// ServeHTTP handles MCP Streamable-HTTP-style requests on /mcp. Notifications
// get an empty 202; requests get a JSON-RPC response.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed: use POST", http.StatusMethodNotAllowed)
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
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}
