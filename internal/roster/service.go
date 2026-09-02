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
	"slices"
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

// NotFoundError marks a mutation naming an email the roster does not carry;
// the API answers it with 404.
type NotFoundError struct {
	Email string
}

func (e *NotFoundError) Error() string { return "roster record not found: " + e.Email }

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

// MutationResult is the mutation API's answer: the stored record plus the
// Roster sync state once the first apply settled (or the settle window
// elapsed), so the dialog can show "stored, applying…" without a second
// fetch.
type MutationResult struct {
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
	RosterRecord(ctx context.Context, email string) (users.RosterRecord, error)
	RosterRecords(ctx context.Context) ([]users.RosterRecord, error)
	EditRosterUser(ctx context.Context, email string, edit users.RosterEdit, now time.Time) (users.RosterRecord, error)
	RemoveRosterUser(ctx context.Context, email string, now time.Time) error
}

// ViewSource supplies the current parsed inbound view — the seam over the
// xray config watcher.
type ViewSource interface {
	View() xrayconfig.View
}

// RosterParseSource supplies the config file's roster as last parsed —
// clients with their inbound attachments — plus a version that bumps on
// every parse (0: nothing parsed yet). The seam for convergence over the
// xray config watcher.
type RosterParseSource interface {
	Roster() (xrayconfig.RosterParse, uint64)
}

// Renderer is the file half of the apply path: apply the plan to the config
// document and persist the result. FileRenderer in production.
type Renderer interface {
	Render(ctx context.Context, plan xrayconfig.RenderPlan) (changed bool, err error)
}

// Pusher is the live half of the apply path — xraygrpc.HandlerClient in
// production.
type Pusher interface {
	AddUser(ctx context.Context, tag string, user xraygrpc.ManagedUser) error
	RemoveUser(ctx context.Context, tag, email string) error
}

// StatusSource supplies the observed xray status — *xraystatus.Cache in
// production. Transitions to running are a retry trigger.
type StatusSource interface {
	Latest(context.Context) (xraystatus.Status, error)
}

// opKind is what one apply operation does to one inbound.
type opKind int

const (
	opAttach opKind = iota // the user joins the inbound
	opDetach               // the user leaves the inbound
	opRotate               // the user's credential changes on the inbound
)

// pendingChange is one stored-not-yet-applied mutation: the record's
// Client ID plus the ordered per-inbound operations. A rotate's remove+add
// pair sits adjacent in ops, per the spec's not-atomic auth gap. The gen
// guards the apply loop: a pass may only settle (or fail) the change it
// snapshotted — a newer edit that superseded it mid-pass stays queued for
// its own pass.
type pendingChange struct {
	id  string
	ops []pushOp
	gen uint64
}

type pushOp struct {
	kind opKind
	tag  string
	flow string // the attach-time flow, resolved when the op was planned
}

// Service is the Roster's write path. Add validates, stores, and kicks the
// apply; a single background loop applies — render into the config file,
// then push to the running xray — and retries on config-watch fires and xray
// status transitions to running.
type Service struct {
	store    Store
	views    ViewSource
	parses   RosterParseSource
	renderer Renderer
	pusher   Pusher
	status   StatusSource
	changes  <-chan struct{}

	now        func() time.Time
	settleWait time.Duration
	statusPoll time.Duration
	nextGen    uint64 // pending-change generation counter

	mu      sync.Mutex
	pending map[string]pendingChange // email → unapplied change
	failed  map[string]bool          // emails whose last apply failed
	settled chan struct{}            // closed after every apply pass — a generation broadcast
	kick    chan struct{}

	lastApplyErr string // last logged apply failure; "" when healthy
}

// NewService creates the roster write path. changes is the config watcher's
// successful-load channel — a retry and convergence trigger.
func NewService(store Store, views ViewSource, parses RosterParseSource, renderer Renderer, pusher Pusher, status StatusSource, changes <-chan struct{}) *Service {
	return &Service{
		store:    store,
		views:    views,
		parses:   parses,
		renderer: renderer,
		pusher:   pusher,
		status:   status,
		changes:  changes,
		now:      time.Now,
		// Longer than the pusher's own timeout, so a black-holed xray still
		// answers failed rather than pending.
		settleWait: 5 * time.Second,
		statusPoll: time.Second,
		pending:    map[string]pendingChange{},
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
func (s *Service) Add(ctx context.Context, email, clientID string, inbounds []string) (MutationResult, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return MutationResult{}, &ConflictError{Reason: ReasonEmailInvalid}
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = uuid.NewString()
	} else {
		parsed, err := uuid.Parse(clientID)
		if err != nil {
			return MutationResult{}, &ConflictError{Reason: ReasonClientIDInvalid}
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
			return MutationResult{}, &ConflictError{Reason: ReasonUnknownInbound}
		}
		ops = append(ops, pushOp{kind: opAttach, tag: tag, flow: xrayconfig.DefaultFlow(inbound)})
	}

	record, err := s.store.AddRosterUser(ctx, users.NewRosterUser{
		Email: email, ClientID: clientID, Inbounds: tags,
		Protocol: labelProtocol(view, ops), Security: labelSecurity(view, ops),
	}, s.now())
	if errors.Is(err, users.ErrEmailTaken) {
		return MutationResult{}, &ConflictError{Reason: ReasonEmailTaken}
	}
	if errors.Is(err, users.ErrClientIDTaken) {
		return MutationResult{}, &ConflictError{Reason: ReasonClientIDTaken}
	}
	if err != nil {
		return MutationResult{}, err
	}

	// A profile-less user attaches nowhere: nothing renders, nothing pushes.
	if len(ops) == 0 {
		return MutationResult{User: record, Sync: s.Sync()}, nil
	}
	return s.queueOps(record, ops), nil
}

// EditRequest is one edit mutation (user-management spec §5): an empty
// ClientID keeps the stored credential; nil Inbounds keeps the stored
// attachment set while an empty (non-nil) set detaches every inbound.
type EditRequest struct {
	ClientID string
	Inbounds []string
}

// Edit validates and stores one roster edit, then applies it live the same
// way Add does (user-management spec §4): attach/detach per inbound, and a
// changed Client ID as remove+add on every attached inbound. Idempotent — a
// save carrying the stored state applies nothing. Violations are
// ConflictErrors; an unknown email is a NotFoundError.
func (s *Service) Edit(ctx context.Context, email string, req EditRequest) (MutationResult, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return MutationResult{}, &NotFoundError{Email: email}
	}
	clientID := ""
	if strings.TrimSpace(req.ClientID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(req.ClientID))
		if err != nil {
			return MutationResult{}, &ConflictError{Reason: ReasonClientIDInvalid}
		}
		clientID = parsed.String()
	}

	before, err := s.store.RosterRecord(ctx, email)
	if errors.Is(err, users.ErrRosterNotFound) {
		return MutationResult{}, &NotFoundError{Email: email}
	}
	if err != nil {
		return MutationResult{}, err
	}

	// The final attachment set, validated against the current inbound view
	// with each attach flow resolved. A kept selection (no inbounds in the
	// request) is pruned of tags the config no longer carries — a vanished
	// inbound must not block the rest of the edit; the store simply stops
	// claiming it (the inbound is gone from xray too).
	view := s.views.View()
	selection := req.Inbounds
	if selection == nil {
		selection = before.Inbounds // keep — pruned below of vanished tags
	}
	tags := make([]string, 0, len(selection))
	flows := make(map[string]string, len(selection))
	finalOps := make([]pushOp, 0, len(selection))
	for _, tag := range selection {
		inbound, ok := findManaged(view, tag)
		if !ok {
			if req.Inbounds == nil {
				continue // a stored attachment the config dropped: prune it
			}
			return MutationResult{}, &ConflictError{Reason: ReasonUnknownInbound}
		}
		flow := xrayconfig.DefaultFlow(inbound)
		tags = append(tags, tag)
		flows[tag] = flow
		finalOps = append(finalOps, pushOp{kind: opAttach, tag: tag, flow: flow})
	}

	var idPtr *string
	if clientID != "" {
		idPtr = &clientID
	}
	// The pruned before-record — only on the keep path: detached ops are
	// diffed against what the config still carries, so a vanished tag never
	// queues a push to an inbound xray no longer has. On the set path the
	// diff needs the store's real before.
	if req.Inbounds == nil {
		before.Inbounds = tags
	}
	edit := users.RosterEdit{
		ClientID: idPtr,
		Inbounds: tags,
		Protocol: labelProtocol(view, finalOps), Security: labelSecurity(view, finalOps),
	}
	after, err := s.store.EditRosterUser(ctx, email, edit, s.now())
	if errors.Is(err, users.ErrClientIDTaken) {
		return MutationResult{}, &ConflictError{Reason: ReasonClientIDTaken}
	}
	if errors.Is(err, users.ErrRosterNotFound) {
		return MutationResult{}, &NotFoundError{Email: email}
	}
	if err != nil {
		return MutationResult{}, err
	}

	ops := diffOps(before, after, flows, s.takePending(email))
	if len(ops) == 0 { // an idempotent save: nothing to apply
		return MutationResult{User: after, Sync: s.Sync()}, nil
	}
	return s.queueOps(after, ops), nil
}

// Remove stores one user's removal from the Roster — the row is flagged
// gone, history kept (user-management spec §3–§4) — then applies it live:
// rendered out of every inbound that might still carry them (including an
// unfinished change's tags) and pushed off the running xray. Established
// connections close naturally; xray has no disconnect op. Idempotent: an
// already-removed or unknown email is a plain success that removed nothing.
// The mutation succeeds once stored: a failed apply answers failed.
func (s *Service) Remove(ctx context.Context, email string) (sync SyncState, removed bool, err error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return s.Sync(), false, nil
	}
	before, err := s.store.RosterRecord(ctx, email)
	if errors.Is(err, users.ErrRosterNotFound) {
		return s.Sync(), false, nil // gone already — idempotent success
	}
	if err != nil {
		return "", false, err
	}

	// Every tag that might hold them live: the record's attachments plus
	// an unfinished change's attach/rotate targets (its push may have
	// landed even though the change never settled).
	prev := s.takePending(email)
	tags := slices.Clone(before.Inbounds)
	for _, op := range prev.ops {
		if (op.kind == opAttach || op.kind == opRotate) && !slices.Contains(tags, op.tag) {
			tags = append(tags, op.tag)
		}
	}

	if err := s.store.RemoveRosterUser(ctx, email, s.now()); err != nil {
		return "", false, err
	}

	if len(tags) == 0 {
		return s.Sync(), true, nil // profile-less: nothing to apply
	}
	ops := make([]pushOp, 0, len(tags))
	for _, tag := range tags {
		ops = append(ops, pushOp{kind: opDetach, tag: tag})
	}
	return s.queueOps(before, ops).Sync, true, nil
}

// takePending removes and returns the email's unapplied change, if any —
// the previous change's still-unapplied operations feed the merge.
func (s *Service) takePending(email string) pendingChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.pending[email]
	delete(s.pending, email)
	return prev
}

// diffOps plans the edit's operations: everything the running xray and the
// file must do to carry the after record, given the before record and — for
// merges — the previous change that never finished applying. With no
// previous change the plan is the pure before/after diff. With one, tags
// the unfinished change touched get the remove+add treatment (their push
// may have half-landed) and its unfinished detaches are re-queued — a
// remove of an absent user and an add of a present one both read as
// applied, so retries converge (user-management spec §4, §7).
func diffOps(before, after users.RosterRecord, flows map[string]string, prev pendingChange) []pushOp {
	idChanged := before.ClientID != after.ClientID
	attached := make(map[string]bool, len(before.Inbounds))
	for _, tag := range before.Inbounds {
		attached[tag] = true
	}
	kept := make(map[string]bool, len(after.Inbounds))
	for _, tag := range after.Inbounds {
		kept[tag] = true
	}

	prevAttached := make(map[string]bool, len(prev.ops))
	for _, op := range prev.ops {
		if op.kind == opAttach || op.kind == opRotate {
			prevAttached[op.tag] = true
		}
	}

	var ops []pushOp
	// First the leaves: tags the before record had — or an unfinished
	// change still owed a change to — and the after record does not keep.
	for _, tag := range before.Inbounds {
		if !kept[tag] {
			ops = append(ops, pushOp{kind: opDetach, tag: tag})
		}
	}
	for _, op := range prev.ops {
		// An unfinished detach is still owed (§7: re-saving retries): a
		// remove of an already-gone user reads as applied, so re-queuing it
		// converges whether or not the earlier attempt landed.
		if op.kind == opDetach && !kept[op.tag] {
			ops = append(ops, pushOp{kind: opDetach, tag: op.tag})
		}
	}
	// Then the stays and joins. A rotate (the spec's remove+add pair) is
	// due wherever the running xray may hold a credential other than the
	// after record's: an id change on a kept tag, or any tag an unfinished
	// change touched — its push may have half-landed, and a bare add would
	// be swallowed by xray's "already exists" while the old credential
	// keeps authenticating.
	merge := len(prev.ops) > 0
	for _, tag := range after.Inbounds {
		switch {
		case (idChanged && attached[tag]) || prevAttached[tag]:
			ops = append(ops, pushOp{kind: opRotate, tag: tag, flow: flows[tag]})
		case !attached[tag] && !prevAttached[tag]:
			ops = append(ops, pushOp{kind: opAttach, tag: tag, flow: flows[tag]})
		case merge: // a stayed tag whose earlier change never finished: re-push
			ops = append(ops, pushOp{kind: opAttach, tag: tag, flow: flows[tag]})
		}
	}
	return ops
}

// queueOps queues the operations, kicks the apply loop, and waits for the
// first pass to settle (§4–§5).
func (s *Service) queueOps(record users.RosterRecord, ops []pushOp) MutationResult {
	s.enqueue(record, ops)
	return MutationResult{User: record, Sync: s.waitSettled(record.Email)}
}

// enqueue queues the operations and kicks the apply loop without waiting —
// the background path (convergence) never blocks the loop it runs in.
func (s *Service) enqueue(record users.RosterRecord, ops []pushOp) {
	email := record.Email
	s.mu.Lock()
	s.nextGen++
	s.pending[email] = pendingChange{id: record.ClientID, ops: ops, gen: s.nextGen}
	delete(s.failed, email) // a fresh change starts a fresh apply
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
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

// Start runs the apply loop in its own goroutine until the context is
// cancelled, and returns — the panel's Start convention: callers sequence
// startups, they never join loops. The loop applies on kicks from
// mutations, retries on every config-watch fire, and retries on every
// xray status transition to running (user-management spec §7).
func (s *Service) Start(ctx context.Context) {
	go s.run(ctx)
}

func (s *Service) run(ctx context.Context) {
	changes := s.changes
	var lastRunning bool
	if status, err := s.status.Latest(ctx); err == nil {
		lastRunning = status.Status == "running"
	}
	s.converge(ctx) // a file that drifted while the panel was down
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
			s.converge(ctx)
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

// converge reconciles one config parse against the Roster (user-management
// spec §4, store wins with adoption): a store user the file lost is
// re-rendered and re-pushed — a hand edit or an ansible re-run that strips
// clients never deletes; an inbound the file lost has its attachments
// pruned from the store (the inbound is gone from xray too), users left
// with none staying profile-less. Extra config clients are adoption's
// business and gone users stay gone; users with an unapplied change of
// their own are left to it — the file catching up settles them.
func (s *Service) converge(ctx context.Context) {
	parse, version := s.parses.Roster()
	if version == 0 {
		return // nothing parsed yet: nothing to reconcile against
	}
	view := s.views.View()
	live := make(map[string]bool)
	for _, inbound := range view.Inbounds() {
		if xrayconfig.Managed(inbound) {
			live[inbound.Tag] = true
		}
	}

	records, err := s.store.RosterRecords(ctx)
	if err != nil {
		s.logFailure("cannot read the roster for convergence; the pass retries on the next watch fire", err)
		return
	}

	for _, record := range records {
		if s.hasPending(record.Email) {
			continue // their own change is the convergence in flight
		}

		// Attachments to inbounds the config dropped: pruned from the store
		// — a store-only write, never a push to a dead tag.
		kept := make([]string, 0, len(record.Inbounds))
		for _, tag := range record.Inbounds {
			if live[tag] {
				kept = append(kept, tag)
			}
		}
		if len(kept) < len(record.Inbounds) {
			attachOps := make([]pushOp, 0, len(kept))
			for _, tag := range kept {
				if inbound, ok := findManaged(view, tag); ok {
					attachOps = append(attachOps, pushOp{kind: opAttach, tag: tag, flow: xrayconfig.DefaultFlow(inbound)})
				}
			}
			record, err = s.store.EditRosterUser(ctx, record.Email, users.RosterEdit{
				Inbounds: kept,
				Protocol: labelProtocol(view, attachOps),
				Security: labelSecurity(view, attachOps),
			}, s.now())
			if errors.Is(err, users.ErrRosterNotFound) {
				continue // removed while we worked
			}
			if err != nil {
				s.logFailure("cannot prune drifted attachments; the pass retries on the next watch fire", err)
				continue
			}
		}

		// Presence drift: attachments the file lost come back — store wins.
		fileTags := make(map[string]bool, len(record.Inbounds))
		for _, tag := range parse.Clients[record.Email].Inbounds {
			fileTags[tag] = true
		}
		var ops []pushOp
		for _, tag := range record.Inbounds {
			if fileTags[tag] || !live[tag] {
				continue // present in the file, or a vanished inbound pruned above
			}
			inbound, ok := findManaged(view, tag)
			if !ok {
				continue // raced with a config change; the next fire settles it
			}
			ops = append(ops, pushOp{kind: opAttach, tag: tag, flow: xrayconfig.DefaultFlow(inbound)})
		}
		if len(ops) > 0 {
			s.enqueue(record, ops)
		}
	}
}

// hasPending reports whether the email carries an unapplied change —
// convergence leaves those to their own apply.
func (s *Service) hasPending(email string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pending[email]
	return ok
}

// apply runs one pass on one snapshot of the pending changes — the render
// and the pushes work the same set, so the file always leads the live
// server. A render failure marks every pending user failed — without the
// file the push would create a restart-losing split. Push failures mark
// only their own user; xray's duplicate-email answer to an add and
// missing-email answer to a remove are the pusher's success, so retries
// converge.
func (s *Service) apply(ctx context.Context) {
	s.mu.Lock()
	pending := make(map[string]pendingChange, len(s.pending))
	plan := xrayconfig.RenderPlan{
		Adds:    map[string][]xrayconfig.ClientOp{},
		Removes: map[string][]string{},
		Sets:    map[string][]xrayconfig.ClientOp{},
	}
	for email, change := range s.pending {
		pending[email] = change
		for _, op := range change.ops {
			switch op.kind {
			case opAttach:
				plan.Adds[op.tag] = append(plan.Adds[op.tag], xrayconfig.ClientOp{Email: email, ID: change.id, Flow: op.flow})
			case opDetach:
				plan.Removes[op.tag] = append(plan.Removes[op.tag], email)
			case opRotate:
				plan.Sets[op.tag] = append(plan.Sets[op.tag], xrayconfig.ClientOp{Email: email, ID: change.id, Flow: op.flow})
			}
		}
	}
	s.mu.Unlock()
	if len(pending) == 0 {
		s.broadcast()
		return
	}

	if _, err := s.renderer.Render(ctx, plan); err != nil {
		s.logFailure("cannot render the roster into the xray config; the apply stays failed", err)
		s.mu.Lock()
		for email, change := range pending {
			if !s.supersededLocked(email, change.gen) {
				s.failed[email] = true
			}
		}
		s.mu.Unlock()
		s.broadcast()
		return
	}

	for email, change := range pending {
		if s.superseded(email, change.gen) {
			continue // a newer edit replaced this change; its own pass applies it
		}
		failed := false
		for _, op := range change.ops {
			if err := s.pushOp(ctx, email, change, op); err != nil {
				s.logFailure("cannot push a roster change to xray; the apply stays failed", err)
				failed = true
				break
			}
		}
		s.mu.Lock()
		if failed {
			if !s.supersededLocked(email, change.gen) {
				s.failed[email] = true
			}
		} else if current, still := s.pending[email]; still && current.gen == change.gen {
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

// superseded reports whether the email's queued change left the given
// generation behind — a newer mutation replaced it mid-pass.
func (s *Service) superseded(email string, gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.supersededLocked(email, gen)
}

func (s *Service) supersededLocked(email string, gen uint64) bool {
	current, ok := s.pending[email]
	return !ok || current.gen != gen
}

// pushOp performs one operation's live push — file before live already
// held by the render pass. A rotate is the spec's remove+add pair: remove
// whatever credential the inbound holds for the email, then add the new
// one.
func (s *Service) pushOp(ctx context.Context, email string, change pendingChange, op pushOp) error {
	if _, ok := findManaged(s.views.View(), op.tag); !ok {
		// The inbound left the config between planning and pushing — the
		// render already skipped it, and convergence prunes the attachment.
		// A push would only answer "failed to get handler".
		return nil
	}
	switch op.kind {
	case opAttach:
		return s.pusher.AddUser(ctx, op.tag, xraygrpc.ManagedUser{Email: email, ID: change.id, Flow: op.flow})
	case opDetach:
		return s.pusher.RemoveUser(ctx, op.tag, email)
	case opRotate:
		if err := s.pusher.RemoveUser(ctx, op.tag, email); err != nil {
			return err
		}
		return s.pusher.AddUser(ctx, op.tag, xraygrpc.ManagedUser{Email: email, ID: change.id, Flow: op.flow})
	}
	return nil
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
