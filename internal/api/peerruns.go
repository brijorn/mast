package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A program run executes on the node that started it and is tracked in that
// node's store. When a run was started on a peer — an iOS program running on the
// mac that owns its phone — this node's store knows nothing about it. These
// helpers let the gateway aggregate and proxy peer runs so an operator reaches
// every run through one node, the same way device control already forwards.

var peerRunClient = &http.Client{Timeout: 20 * time.Second}

// peerAPIBases returns the API base URLs of every non-local node.
func (s *Server) peerAPIBases() []string {
	if s.node == nil {
		return nil
	}
	var bases []string
	for _, n := range s.node.ListNodes() {
		if n.Local || strings.TrimSpace(n.Addr) == "" {
			continue
		}
		port := strings.TrimPrefix(strings.TrimSpace(n.APIAddr), ":")
		if port == "" {
			port = "6271"
		}
		bases = append(bases, fmt.Sprintf("http://%s:%s", n.Addr, port))
	}
	return bases
}

// peerRuns fetches one peer's run list as raw JSON objects. The local=1 marker
// asks the peer for only the runs it owns, so a peer that also aggregates does
// not turn around and ask us back — that would recurse across the network.
func peerRuns(base string) []json.RawMessage {
	resp, err := peerRunClient.Get(base + "/api/runs?local=1")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var runs []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil
	}
	return runs
}

// mergedRuns returns this node's runs plus every peer's, de-duplicated by id.
func (s *Server) mergedRuns(local []json.RawMessage) []json.RawMessage {
	seen := make(map[string]bool, len(local))
	combined := make([]json.RawMessage, 0, len(local))
	add := func(runs []json.RawMessage) {
		for _, rm := range runs {
			var idOnly struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rm, &idOnly); err != nil || idOnly.ID == "" || seen[idOnly.ID] {
				continue
			}
			seen[idOnly.ID] = true
			combined = append(combined, rm)
		}
	}
	add(local)
	for _, base := range s.peerAPIBases() {
		add(peerRuns(base))
	}
	return combined
}

// proxyMarker names a request this node already forwarded. A peer that also
// proxies checks it and refuses to forward again, so a run that exists on no
// node fails on the second hop instead of bouncing between nodes forever.
const proxyMarker = "X-Mast-Run-Proxied"

// proxyToPeerWithRun forwards a run request to the first peer that owns the run,
// returning true when a peer handled it. It is used when this node's store does
// not have the run, meaning it is running on a peer.
func (s *Server) proxyToPeerWithRun(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.Header.Get(proxyMarker) != "" {
		return false
	}
	for _, base := range s.peerAPIBases() {
		if s.proxyRunRequest(w, r, base+path) {
			return true
		}
	}
	return false
}

// proxyRunRequest replays the request against one peer URL. A 404 means the peer
// does not own the run, so the caller tries the next; any other status is the
// peer's answer and is passed through.
func (s *Server) proxyRunRequest(w http.ResponseWriter, r *http.Request, target string) bool {
	target += queryString(r)
	var body io.Reader
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		return false
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set(proxyMarker, "1")
	resp, err := peerRunClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false
	}
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}

func queryString(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}

// isTruthy reads a query flag that may arrive as "1", "true", or bare presence.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
