package cloudserver

import "net/http"

// handleBrainIdentity serves GET /api/brain/identity.
//
// Reads the X-Forwarded-Email header injected by oauth2-proxy and returns the
// email as JSON {"email":"<value>"}. When the header is absent, returns
// {"email":""} with status 200 — empty is an acceptable sentinel because
// oauth2-proxy guarantees the caller is authenticated; the header is simply not
// forwarded in some proxy configurations.
//
// The endpoint is NOT behind withAuth because it must be reachable by the brain
// iframe JS (same-origin fetch) without a sync bearer token. Any authenticated
// viewer can call it — the response is non-sensitive (it only echoes a header
// the caller's own browser already provided).
func (s *CloudServer) handleBrainIdentity(w http.ResponseWriter, r *http.Request) {
	email := r.Header.Get("X-Forwarded-Email")
	jsonResponse(w, http.StatusOK, map[string]any{"email": email})
}
