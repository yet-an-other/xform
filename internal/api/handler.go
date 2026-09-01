// Package api exposes xform's HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yet-an-other/xform/internal/configsnapshot"
	"github.com/yet-an-other/xform/internal/hoststats"
	"github.com/yet-an-other/xform/internal/journal"
	"github.com/yet-an-other/xform/internal/profiles"
	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/session"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

type hostStatsSnapshots interface {
	Latest(context.Context) (hoststats.Stats, error)
}

type sessionManager interface {
	Login(password string) (token string, ok bool, err error)
	Validate(token string) bool
	Logout(token string)
}

type xrayStatuses interface {
	Latest(context.Context) (xraystatus.Status, error)
}

type usersSnapshots interface {
	Latest(context.Context) (users.Snapshot, error)
}

// rosterMutations is the Roster's write side behind the mutation API
// (user-management spec §5): the add and edit mutations, plus the state the
// users endpoint merges into its reads — the Roster sync state, the per-user
// apply marks, and the add dialog's inbound options.
type rosterMutations interface {
	Add(ctx context.Context, email, clientID string, inbounds []string) (roster.MutationResult, error)
	Edit(ctx context.Context, email string, req roster.EditRequest) (roster.MutationResult, error)
	Sync() roster.SyncState
	UserStates() map[string]roster.ApplyState
	InboundOptions() []roster.InboundOption
}

type connectionProfileSources interface {
	Current() profiles.Sources
}

// logSnapshots is the Log snapshot module's one collection operation
// (SPEC §8). The handler's only choice is the fixed source, so no
// unit, count, filter, cursor, or time range can reach journalctl through the
// HTTP surface.
type logSnapshots interface {
	Collect(ctx context.Context, source journal.Source) (journal.Snapshot, error)
}

// configSnapshots is the Config snapshot module's one bounded read
// (SPEC §8).
type configSnapshots interface {
	Read(ctx context.Context) (configsnapshot.Snapshot, error)
}

const sessionCookieName = "xform_session"

// PanelInfo is the panel's own identity, exposed through the API: the
// release version (ldflags-stamped at build time) and the configured xray
// gRPC endpoint, which the dashboard names in its degraded banner. Uptime
// reports elapsed whole seconds since the panel process started (OP-1);
// UptimeSeconds builds it from a monotonic start time.
type PanelInfo struct {
	Version         string
	XrayAPIEndpoint string
	Uptime          func() int64
}

// UptimeSeconds returns a Uptime source counting whole seconds elapsed
// since start. start carries a monotonic reading in production (a time.Now
// captured at process start), so restarts reset it. now is the clock seam:
// production passes time.Now, tests pass a controlled clock.
func UptimeSeconds(start time.Time, now func() time.Time) func() int64 {
	return func() int64 {
		return int64(now().Sub(start).Seconds())
	}
}

// panelResponse is GET /api/v1/panel: the panel identity plus the current
// process uptime, re-read on every request — the dashboard polls it every
// five seconds instead of extrapolating in the browser.
type panelResponse struct {
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// xrayResponse is GET /api/v1/xray: the observed Status plus the
// configured gRPC endpoint (config, not observation, so it can't live on
// the Status itself).
type xrayResponse struct {
	xraystatus.Status
	APIEndpoint string `json:"api_endpoint"`
}

// New returns the HTTP handler for the API and dashboard. Every /api/ route
// except login and healthz requires a session (SPEC.md §5); the dashboard
// itself loads openly and lets the SPA route to its login page on 401.
// Mutations additionally reject cross-site requests (user-management spec §5).
func New(snapshots hostStatsSnapshots, xray xrayStatuses, usersSource usersSnapshots, profileSources connectionProfileSources, rosterSource rosterMutations, operational OperationalSources, sessions sessionManager, dashboard http.Handler, panel PanelInfo) http.Handler {
	noStore := func(next http.HandlerFunc) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Cache-Control", "no-store")
			next(response, request)
		}
	}
	requireSession := func(next http.HandlerFunc) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			cookie, err := request.Cookie(sessionCookieName)
			if err != nil || !sessions.Validate(cookie.Value) {
				writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
				return
			}
			// Slide both sides of the 24h expiry: the store entry (via
			// Validate) and the cookie's Max-Age.
			setSessionCookie(response, cookie.Value)
			next(response, request)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/login", func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Password == "" {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		token, ok, err := sessions.Login(body.Password)
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "login unavailable"})
			return
		}
		if !ok {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
			return
		}
		setSessionCookie(response, token)
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/logout", requireSession(func(response http.ResponseWriter, request *http.Request) {
		cookie, _ := request.Cookie(sessionCookieName) // requireSession guarantees it
		sessions.Logout(cookie.Value)
		http.SetCookie(response, &http.Cookie{
			Name: sessionCookieName, MaxAge: -1, Path: "/",
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
		response.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/v1/healthz", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/panel", noStore(requireSession(func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, panelResponse{Version: panel.Version, UptimeSeconds: panel.Uptime()})
	})))
	mux.HandleFunc("GET /api/v1/server", requireSession(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		stats, err := snapshots.Latest(request.Context())
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "host stats unavailable"})
			return
		}

		writeJSON(response, http.StatusOK, stats)
	}))
	mux.HandleFunc("GET /api/v1/xray", requireSession(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		status, err := xray.Latest(request.Context())
		if err != nil {
			// Reachable only when the cache was never primed AND the source
			// failed — observation failures are data (status: unreachable),
			// so this branch means a panel-internal failure, hence 500.
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "xray status unavailable"})
			return
		}

		writeJSON(response, http.StatusOK, xrayResponse{Status: status, APIEndpoint: panel.XrayAPIEndpoint})
	}))
	mux.HandleFunc("GET /api/v1/users", requireSession(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		snapshot, err := usersSource.Latest(request.Context())
		if err != nil {
			// Reachable only when the cache was never primed AND the source
			// failed — a panel-internal failure, hence 500.
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "users unavailable"})
			return
		}

		// The write side rides the same payload: the Roster sync state, the
		// per-user apply marks, and the add dialog's inbound options.
		states := rosterSource.UserStates()
		rows := make([]userRow, len(snapshot.Users))
		for index, user := range snapshot.Users {
			rows[index] = userRow{User: user, ApplyState: string(states[user.Email])}
		}
		writeJSON(response, http.StatusOK, usersResponse{
			CollectedAt: snapshot.CollectedAt,
			Stale:       snapshot.Stale,
			Users:       rows,
			RosterSync:  rosterSource.Sync(),
			Inbounds:    rosterSource.InboundOptions(),
		})
	}))
	mux.HandleFunc("POST /api/v1/users", noStore(requireSession(sameSiteOnly(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Email    string   `json:"email"`
			ClientID string   `json:"client_id"`
			Inbounds []string `json:"inbounds"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		result, err := rosterSource.Add(request.Context(), body.Email, body.ClientID, body.Inbounds)
		var conflict *roster.ConflictError
		if errors.As(err, &conflict) {
			writeJSON(response, http.StatusConflict, map[string]string{"error": conflict.Reason})
			return
		}
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "roster unavailable"})
			return
		}
		writeJSON(response, http.StatusCreated, mutationResponse{User: result.User, RosterSync: result.Sync})
	}))))
	mux.HandleFunc("PATCH /api/v1/users/{email}", noStore(requireSession(sameSiteOnly(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			ClientID string   `json:"client_id"` // absent keeps the stored credential
			Inbounds []string `json:"inbounds"`  // absent keeps; an explicit array sets (empty detaches all)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		result, err := rosterSource.Edit(request.Context(), request.PathValue("email"), roster.EditRequest{
			ClientID: body.ClientID, Inbounds: body.Inbounds,
		})
		var conflict *roster.ConflictError
		if errors.As(err, &conflict) {
			writeJSON(response, http.StatusConflict, map[string]string{"error": conflict.Reason})
			return
		}
		var missing *roster.NotFoundError
		if errors.As(err, &missing) {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "roster unavailable"})
			return
		}
		writeJSON(response, http.StatusOK, mutationResponse{User: result.User, RosterSync: result.Sync})
	}))))
	mux.HandleFunc("GET /api/v1/users/{email}", noStore(requireSession(func(response http.ResponseWriter, request *http.Request) {
		if malformedUserEmailEscape(request.RequestURI) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		snapshot, err := usersSource.Latest(request.Context())
		if err != nil {
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "user detail unavailable"})
			return
		}

		email := request.PathValue("email")
		for _, user := range snapshot.Users {
			if user.Email != email {
				continue
			}
			collection := profiles.Evaluate(email, user.Gone, profileSources.Current())
			writeJSON(response, http.StatusOK, userDetailResponse{
				CollectedAt:        snapshot.CollectedAt,
				Stale:              snapshot.Stale,
				User:               user,
				ConnectionProfiles: connectionProfilesJSON(collection),
			})
			return
		}
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not_found"})
	})))
	mux.HandleFunc("GET /api/v1/logs/panel", noStore(requireSession(logSnapshotHandler(operational.Logs, journal.SourcePanel))))
	mux.HandleFunc("GET /api/v1/logs/xray", noStore(requireSession(logSnapshotHandler(operational.Logs, journal.SourceXray))))
	mux.HandleFunc("GET /api/v1/xray/config", noStore(requireSession(configSnapshotHandler(operational.Config))))
	mux.Handle("/api/", requireSession(func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not found"})
	}))
	mux.Handle("/", dashboard)
	return mux
}

// userRow is one users-table row plus its write-side mark: pending while
// its change applies, failed when the last apply failed; absent when applied.
type userRow struct {
	users.User
	ApplyState string `json:"apply_state,omitempty"`
}

// usersResponse is GET /api/v1/users: the observed snapshot plus the Roster
// write side (user-management spec §5–§6).
type usersResponse struct {
	CollectedAt int64                  `json:"collected_at"`
	Stale       bool                   `json:"stale"`
	Users       []userRow              `json:"users"`
	RosterSync  roster.SyncState       `json:"roster_sync"`
	Inbounds    []roster.InboundOption `json:"inbounds"`
}

// mutationResponse is POST and PATCH /api/v1/users: the stored roster record
// plus the Roster sync state once the first apply settled.
type mutationResponse struct {
	User       roster.Record    `json:"user"`
	RosterSync roster.SyncState `json:"roster_sync"`
}

// sameSiteOnly is the mutation CSRF guard (user-management spec §5): the
// SameSite=Lax session cookie already keeps cross-site POSTs from carrying a
// session, and mutations additionally reject requests whose Origin or
// Sec-Fetch-Site says cross-site. No token ceremony.
func sameSiteOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if origin := request.Header.Get("Origin"); origin != "" {
			if !originMatchesHost(origin, request.Host) {
				writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
		}
		switch site := request.Header.Get("Sec-Fetch-Site"); site {
		case "", "same-origin", "same-site", "none":
		default:
			writeJSON(response, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next(response, request)
	}
}

// originMatchesHost reports whether Origin names the same host the request
// was addressed to. Ports are compared only when both sides carry one: a
// TLS proxy forwarding nginx's $host convention drops the public port while
// the browser's Origin keeps it (panel.example.com:9443 vs panel.example.com
// behind a proxy on a non-default port), which is still the same origin —
// not a cross-site probe. Different ports on both sides stay a mismatch.
func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost, originPort := splitHostPort(parsed.Host)
	requestHost, requestPort := splitHostPort(host)
	if !strings.EqualFold(originHost, requestHost) {
		return false
	}
	return originPort == "" || requestPort == "" || originPort == requestPort
}

// splitHostPort tolerates a host without a port (and keeps IPv6 literals
// whole); net.SplitHostPort errors on both.
func splitHostPort(hostPort string) (string, string) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort, ""
	}
	return host, port
}

// setSessionCookie issues the session cookie per SPEC.md §5: HttpOnly,
// SameSite=Lax, Secure always (browsers exempt localhost, and every other
// access path is TLS-terminated).
func setSessionCookie(response http.ResponseWriter, token string) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/",
		MaxAge:   int(session.TTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
