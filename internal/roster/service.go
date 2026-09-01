// Package roster is the write side of the Roster (user-management spec
// §4–§7): mutations enter through the Service, land in the panel-held store
// first, and then apply — rendered into the xray config file and pushed to
// the running xray — with the Roster sync state tracking how store, file,
// and server agree.
package roster

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
	"github.com/yet-an-other/xform/internal/xraygrpc"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

// SyncState is the Roster sync state (CONTEXT.md): synced when store, config
// file, and running xray agree; pending when a change is stored but not yet
// applied; failed when the last apply failed. Retries continue on watch
// fires and xray status transitions.
type SyncState string

const (
	Synced  SyncState = "synced"
	Pending SyncState = "pending"
	Failed  SyncState = "failed"
)

// ApplyState is one user's write-side mark: ApplyPending while its change
// is stored but not yet applied, ApplyFailed when the last apply failed.
// Absent means applied.
type ApplyState string

const (
	ApplyPending ApplyState = "pending"
	ApplyFailed  ApplyState = "failed"
)

// ConflictError is a mutation validation violation (user-management spec
// §5). The Reason is machine-readable and reaches the dialog.
type ConflictError struct {
	Reason string
}

func (e *ConflictError) Error() string { return "roster conflict: " + e.Reason }

// The conflict reasons the mutation API reports.
const (
	ReasonEmailInvalid    = "email_invalid"
	ReasonEmailTaken      = "email_taken"
	ReasonClientIDInvalid = "client_id_invalid"
	ReasonClientIDTaken   = "client_id_taken"
	ReasonUnknownInbound  = "unknown_inbound"
)

// Record is one stored roster row, returned by mutations.
type Record = users.RosterRecord

// AddResult is the mutation API's answer: the stored record plus the Roster
// sync state once the first apply settled (or the settle window elapsed), so
// the dialog can show "stored, applying…" without a second fetch.
type AddResult struct {
	User Record
	Sync SyncState
}

// InboundOption is one attachable inbound for the add dialog's multi-select
// (user-management spec §6): the tag, plus the protocol · security ·
// transport :port label.
type InboundOption struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

// Store is the roster persistence seam — *users.Store in production.
type Store interface {
	AddRosterUser(ctx context.Context, user users.NewRosterUser, now time.Time) (users.RosterRecord, error)
}

// ViewSource supplies the current parsed inbound view — the seam over the
// xray config watcher.
type ViewSource interface {
	View() xrayconfig.View
}

// Renderer is the file half of the apply path: render the additions into the
// config document and persist the result. FileRenderer in production.
type Renderer interface {
	Render(ctx context.Context, adds map[string][]xrayconfig.NewClient) (changed bool, err error)
}

// Pusher is the live half of the apply path — xraygrpc.HandlerClient in
// production.
type Pusher interface {
	AddUser(ctx context.Context, tag string, user xraygrpc.ManagedUser) error
}

// StatusSource supplies the observed xray status — *xraystatus.Cache in
// production. Transitions to running are a retry trigger.
type StatusSource interface {
	Latest(context.Context) (xraystatus.Status, error)
}

// pendingAdd is one stored-not-yet-applied add: the Client ID and the
// per-inbound push operations (tag plus the flow resolved at attach time).
type pendingAdd struct {
	id  string
	ops []pushOp
}

type pushOp struct {
	tag  string
	flow string
}

// Service is the Roster's write path. Add validates, stores, and kicks the
// apply; a single background loop applies — render into the config file,
// then push to the running xray — and retries on config-watch fires and xray
// status transitions to running.
type Service struct {
	store    Store
	views    ViewSource
	renderer Renderer
	pusher   Pusher
	status   StatusSource
	changes  <-chan struct{}

	now        func() time.Time
	settleWait time.Duration
	statusPoll time.Duration

	mu      sync.Mutex
	pending map[string]pendingAdd // email → unapplied add
	failed  map[string]bool       // emails whose last apply failed
	settled chan struct{}         // closed after every apply pass — a generation broadcast
	kick    chan struct{}

	lastApplyErr string // last logged apply failure; "" when healthy
}

// NewService creates the roster write path. changes is the config watcher's
// successful-load channel — a retry trigger.
func NewService(store Store, views ViewSource, renderer Renderer, pusher Pusher, status StatusSource, changes <-chan struct{}) *Service {
	return &Service{
		store:    store,
		views:    views,
		renderer: renderer,
		pusher:   pusher,
		status:   status,
		changes:  changes,
		now:      time.Now,
		// Longer than the pusher's own timeout, so a black-holed xray still
		// answers failed rather than pending.
		settleWait: 5 * time.Second,
		statusPoll: time.Second,
		pending:    map[string]pendingAdd{},
		failed:     map[string]bool{},
		settled:    make(chan struct{}),
		kick:       make(chan struct{}, 1),
	}
}

// WithSettleWait bounds how long Add waits for the first apply to settle
// before answering pending (tests shrink it).
func (s *Service) WithSettleWait(wait time.Duration) *Service {
	s.settleWait = wait
	return s
}

// WithStatusPoll sets how often the apply loop checks for xray status
// transitions (tests shrink it).
func (s *Service) WithStatusPoll(interval time.Duration) *Service {
	s.statusPoll = interval
	return s
}

// WithClock overrides the store timestamp source (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// Add validates and stores one roster user, then kicks the apply and waits —
// bounded by the settle window — for the first pass to settle, answering
// with the stored record and the Roster sync state (user-management spec
// §4–§5). The mutation succeeds once stored: a failed apply answers failed,
// never an error. Violations are ConflictErrors.
func (s *Service) Add(ctx context.Context, email, clientID string, inbounds []string) (AddResult, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return AddResult{}, &ConflictError{Reason: ReasonEmailInvalid}
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = uuid.NewString()
	} else {
		parsed, err := uuid.Parse(clientID)
		if err != nil {
			return AddResult{}, &ConflictError{Reason: ReasonClientIDInvalid}
		}
		// Canonical form, so the store's uniqueness check cannot be dodged
		// by an equivalent-but-different spelling of the same UUID.
		clientID = parsed.String()
	}

	// Validate the selection against the current inbound view and resolve
	// the per-inbound attach flow (user-management spec §4).
	view := s.views.View()
	tags := dedupeOrder(inbounds)
	ops := make([]pushOp, 0, len(tags))
	for _, tag := range tags {
		inbound, ok := findManaged(view, tag)
		if !ok {
			return AddResult{}, &ConflictError{Reason: ReasonUnknownInbound}
		}
		ops = append(ops, pushOp{tag: tag, flow: xrayconfig.DefaultFlow(inbound)})
	}

	record, err := s.store.AddRosterUser(ctx, users.NewRosterUser{
		Email: email, ClientID: clientID, Inbounds: tags,
		Protocol: labelProtocol(view, ops), Security: labelSecurity(view, ops),
	}, s.now())
	if errors.Is(err, users.ErrEmailTaken) {
		return AddResult{}, &ConflictError{Reason: ReasonEmailTaken}
	}
	if errors.Is(err, users.ErrClientIDTaken) {
		return AddResult{}, &ConflictError{Reason: ReasonClientIDTaken}
	}
	if err != nil {
		return AddResult{}, err
	}

	// A profile-less user attaches nowhere: nothing renders, nothing pushes.
	if len(ops) == 0 {
		return AddResult{User: record, Sync: s.Sync()}, nil
	}

	s.mu.Lock()
	s.pending[email] = pendingAdd{id: clientID, ops: ops}
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}

	return AddResult{User: record, Sync: s.waitSettled(email)}, nil
}

// Sync is the current Roster sync state.
func (s *Service) Sync() SyncState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked()
}

func (s *Service) syncLocked() SyncState {
	if len(s.failed) > 0 {
		return Failed
	}
	if len(s.pending) > 0 {
		return Pending
	}
	return Synced
}

// UserStates maps each user with an unapplied change to its write-side mark
// — pending or failed. Applied users are absent.
func (s *Service) UserStates() map[string]ApplyState {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := make(map[string]ApplyState, len(s.pending))
	for email := range s.pending {
		states[email] = ApplyPending
	}
	for email := range s.failed {
		states[email] = ApplyFailed
	}
	return states
}

// InboundOptions is the add dialog's attachable set: the managed inbounds,
// in config order, with their option labels.
func (s *Service) InboundOptions() []InboundOption {
	options := []InboundOption{}
	for _, inbound := range s.views.View().Inbounds() {
		if !xrayconfig.Managed(inbound) {
			continue
		}
		options = append(options, InboundOption{Tag: inbound.Tag, Label: optionLabel(inbound)})
	}
	return options
}

// Start runs the apply loop until the context is cancelled: kicks from
// mutations, retries on every config-watch fire, and retries on every xray
// status transition to running (user-management spec §7).
func (s *Service) Start(ctx context.Context) {
	changes := s.changes
	var lastRunning bool
	if status, err := s.status.Latest(ctx); err == nil {
		lastRunning = status.Status == "running"
	}
	ticker := time.NewTicker(s.statusPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
			s.apply(ctx)
		case _, ok := <-changes:
			if !ok {
				changes = nil
				continue
			}
			s.retry(ctx)
		case <-ticker.C:
			status, err := s.status.Latest(ctx)
			if err != nil {
				continue
			}
			running := status.Status == "running"
			if running && !lastRunning {
				s.retry(ctx)
			}
			lastRunning = running
		}
	}
}

// retry re-applies when there is anything unapplied — the two failure-mode
// triggers both land here, and a synced roster never re-renders.
func (s *Service) retry(ctx context.Context) {
	s.mu.Lock()
	empty := len(s.pending) == 0
	s.mu.Unlock()
	if !empty {
		s.apply(ctx)
	}
}

// apply runs one pass on one snapshot of the pending adds — the render and
// the pushes work the same set, so the file always leads the live server.
// A render failure marks every pending user failed — without the file the
// push would create a restart-losing split. Push failures mark only their
// own user; xray's duplicate-email answer is the pusher's success, so
// retries converge.
func (s *Service) apply(ctx context.Context) {
	s.mu.Lock()
	pending := make(map[string]pendingAdd, len(s.pending))
	adds := map[string][]xrayconfig.NewClient{}
	for email, add := range s.pending {
		pending[email] = add
		for _, op := range add.ops {
			adds[op.tag] = append(adds[op.tag], xrayconfig.NewClient{Email: email, ID: add.id, Flow: op.flow})
		}
	}
	s.mu.Unlock()
	if len(adds) == 0 {
		s.broadcast()
		return
	}

	if _, err := s.renderer.Render(ctx, adds); err != nil {
		s.logFailure("cannot render the roster into the xray config; the apply stays failed", err)
		s.mu.Lock()
		for email := range pending {
			s.failed[email] = true
		}
		s.mu.Unlock()
		s.broadcast()
		return
	}

	for email, add := range pending {
		failed := false
		for _, op := range add.ops {
			if err := s.pusher.AddUser(ctx, op.tag, xraygrpc.ManagedUser{Email: email, ID: add.id, Flow: op.flow}); err != nil {
				s.logFailure("cannot push a roster add to xray; the apply stays failed", err)
				failed = true
				break
			}
		}
		s.mu.Lock()
		if failed {
			s.failed[email] = true
		} else {
			delete(s.pending, email)
			delete(s.failed, email)
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	if len(s.pending) == 0 && len(s.failed) == 0 {
		s.lastApplyErr = "" // healthy again: the next failure logs fresh
	}
	s.mu.Unlock()
	s.broadcast()
}

// waitSettled blocks until the email's add has applied (or failed), or the
// settle window elapses — after which pending is the honest answer.
func (s *Service) waitSettled(email string) SyncState {
	timer := time.NewTimer(s.settleWait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		_, pending := s.pending[email]
		failed := s.failed[email]
		settled := s.settled
		s.mu.Unlock()
		switch {
		case failed:
			return Failed
		case !pending:
			return s.Sync()
		}
		select {
		case <-settled:
		case <-timer.C:
			return Pending
		}
	}
}

// broadcast wakes every waiter after an apply pass, replacing the generation
// channel.
func (s *Service) broadcast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.settled)
	s.settled = make(chan struct{})
}

// logFailure logs a persistent apply failure once — when its message
// changes — instead of on every retry.
func (s *Service) logFailure(msg string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastApplyErr == err.Error() {
		return
	}
	s.lastApplyErr = err.Error()
	slog.Warn(msg, "error", err)
}

// findManaged resolves the tag to a managed inbound — tagged and VLESS.
func findManaged(view xrayconfig.View, tag string) (xrayconfig.Inbound, bool) {
	for _, inbound := range view.Inbounds() {
		if inbound.Tag == tag && xrayconfig.Managed(inbound) {
			return inbound, true
		}
	}
	return xrayconfig.Inbound{}, false
}

// dedupeOrder dedupes the selection preserving its order.
func dedupeOrder(inbounds []string) []string {
	seen := map[string]bool{}
	ordered := make([]string, 0, len(inbounds))
	for _, tag := range inbounds {
		if !seen[tag] {
			seen[tag] = true
			ordered = append(ordered, tag)
		}
	}
	return ordered
}

// labelProtocol / labelSecurity seed the users-row labels until the next
// config parse resyncs them: the first attached inbound in config order,
// exactly as the parser's labels rule. No attachment means no labels — the
// store writes NULLs.
func labelProtocol(view xrayconfig.View, ops []pushOp) string {
	if inbound, ok := firstAttached(view, ops); ok {
		return strings.ToUpper(inbound.Protocol)
	}
	return ""
}

func labelSecurity(view xrayconfig.View, ops []pushOp) string {
	inbound, ok := firstAttached(view, ops)
	if !ok {
		return ""
	}
	flows := make(map[string]string, len(ops))
	for _, op := range ops {
		flows[op.tag] = op.flow
	}
	return xrayconfig.SecurityLabel(inbound.Security.Type, flows[inbound.Tag])
}

// firstAttached finds the first attached inbound in config order.
func firstAttached(view xrayconfig.View, ops []pushOp) (xrayconfig.Inbound, bool) {
	attached := make(map[string]bool, len(ops))
	for _, op := range ops {
		attached[op.tag] = true
	}
	for _, inbound := range view.Inbounds() {
		if attached[inbound.Tag] && xrayconfig.Managed(inbound) {
			return inbound, true
		}
	}
	return xrayconfig.Inbound{}, false
}

// optionLabel composes the multi-select option text (user-management
// prototype): protocol · security · transport :port, with the transport in
// its modern spelling.
func optionLabel(inbound xrayconfig.Inbound) string {
	network := inbound.Transport.Type
	switch network {
	case "raw":
		network = "tcp"
	case "splithttp":
		network = "xhttp"
	}
	label := strings.ToUpper(inbound.Protocol) + " · " + xrayconfig.SecurityLabel(inbound.Security.Type, "") + " · " + network
	if inbound.Port != "" {
		label += " :" + inbound.Port
	}
	return label
}
