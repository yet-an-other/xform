package roster_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/users"
	"github.com/yet-an-other/xform/internal/xrayconfig"
	"github.com/yet-an-other/xform/internal/xraygrpc"
	"github.com/yet-an-other/xform/internal/xraystatus"
)

// --- fakes over the service's seams ---

type fakeStore struct {
	mu       sync.Mutex
	byMail   map[string]users.RosterRecord // lower(email) → record
	byID     map[string]string             // lower(client_id) → email
	disabled map[string]bool               // lower(email) → off the roster (ADR-0007)
	// vanishOnList makes the next RosterRecords flag every row disabled —
	// a Disable racing the caller between the listing and its next read.
	vanishOnList bool
	edits        int // writes EditRosterUser actually made
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byMail:   map[string]users.RosterRecord{},
		byID:     map[string]string{},
		disabled: map[string]bool{},
	}
}

func (f *fakeStore) AddRosterUser(_ context.Context, user users.NewRosterUser, now time.Time) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email := strings.ToLower(user.Email)
	if _, ok := f.byMail[email]; ok && !f.disabled[email] {
		return users.RosterRecord{}, users.ErrEmailTaken
	}
	if holder, ok := f.byID[strings.ToLower(user.ClientID)]; ok && !(f.disabled[email] && strings.EqualFold(holder, user.Email)) {
		return users.RosterRecord{}, users.ErrClientIDTaken
	}
	if user.Inbounds == nil {
		user.Inbounds = []string{}
	}
	created := now.Unix()
	if record, ok := f.byMail[email]; ok { // revive keeps the creation
		created = record.CreatedAt
	}
	record := users.RosterRecord{
		Email: user.Email, ClientID: user.ClientID, Inbounds: user.Inbounds,
		CreatedAt: created, UpdatedAt: now.Unix(),
	}
	f.byMail[email] = record
	f.byID[strings.ToLower(user.ClientID)] = user.Email
	delete(f.disabled, email)
	return record, nil
}

func (f *fakeStore) RosterRecord(_ context.Context, email string) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = strings.ToLower(email)
	if f.disabled[email] {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	record, ok := f.byMail[email]
	if !ok {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	return record, nil
}

func (f *fakeStore) RosterRecords(_ context.Context) ([]users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	emails := make([]string, 0, len(f.byMail))
	for email := range f.byMail {
		if !f.disabled[email] {
			emails = append(emails, email)
		}
	}
	sort.Strings(emails)
	records := make([]users.RosterRecord, 0, len(emails))
	for _, email := range emails {
		records = append(records, f.byMail[email])
	}
	if f.vanishOnList { // a Disable landing right after the listing
		for email := range f.byMail {
			f.disabled[email] = true
		}
	}
	return records, nil
}

func (f *fakeStore) DisableRosterUser(_ context.Context, email string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = strings.ToLower(email)
	if record, ok := f.byMail[email]; ok && !f.disabled[email] {
		record.UpdatedAt = now.Unix()
		f.byMail[email] = record
		f.disabled[email] = true
	}
	return nil
}

func (f *fakeStore) DisabledRosterRecord(_ context.Context, email string) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.byMail[strings.ToLower(email)]
	if !ok || !f.disabled[strings.ToLower(email)] {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	return record, nil
}

func (f *fakeStore) EnableRosterUser(_ context.Context, email string, now time.Time) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email = strings.ToLower(email)
	record, ok := f.byMail[email]
	if !ok {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	if !f.disabled[email] {
		return record, nil // already live — idempotent
	}
	record.UpdatedAt = now.Unix()
	f.byMail[email] = record
	delete(f.disabled, email)
	return record, nil
}

func (f *fakeStore) EditRosterUser(_ context.Context, email string, edit users.RosterEdit, now time.Time) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.ToLower(email)
	if f.disabled[key] {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	before, ok := f.byMail[key]
	if !ok {
		return users.RosterRecord{}, users.ErrRosterNotFound
	}
	if edit.ClientID != nil {
		holder, taken := f.byID[strings.ToLower(*edit.ClientID)]
		if taken && strings.ToLower(holder) != key {
			return users.RosterRecord{}, users.ErrClientIDTaken
		}
	}
	after := before
	if edit.ClientID != nil && !strings.EqualFold(before.ClientID, *edit.ClientID) {
		after.ClientID = *edit.ClientID
	}
	if edit.Inbounds != nil {
		after.Inbounds = edit.Inbounds
	}
	if after.ClientID == before.ClientID && slices.Equal(after.Inbounds, before.Inbounds) {
		return before, nil
	}
	after.UpdatedAt = now.Unix()
	f.byMail[key] = after
	f.byID[strings.ToLower(after.ClientID)] = before.Email
	f.edits++
	return after, nil
}

type fakeViews struct {
	mu   sync.Mutex
	view xrayconfig.View
}

func (f *fakeViews) View() xrayconfig.View {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

// set replaces the current view — a config change landing between two
// mutations (the vanished-inbound tests).
func (f *fakeViews) set(view xrayconfig.View) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.view = view
}

type fakeRenderer struct {
	mu     sync.Mutex
	events *[]string // shared with the pusher to assert apply order
	plans  []xrayconfig.RenderPlan
	err    error
	block  chan struct{}    // non-nil: Render waits on it (the slow-apply test)
	parses *fakeParseSource // non-nil: models the watcher re-parsing our writes
}

func (f *fakeRenderer) Render(_ context.Context, plan xrayconfig.RenderPlan) (bool, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	*f.events = append(*f.events, "render")
	f.plans = append(f.plans, plan)
	f.mu.Unlock()
	if f.parses != nil {
		f.parses.echo(plan)
	}
	return true, f.err
}

func (f *fakeRenderer) lastPlan() xrayconfig.RenderPlan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plans[len(f.plans)-1]
}

type fakePusher struct {
	mu        sync.Mutex
	events    *[]string
	pushed    []xraygrpc.ManagedUser
	tags      []string
	removed   []string // "email off tag" records, in push order
	addErr    error
	removeErr error
}

func (f *fakePusher) AddUser(_ context.Context, tag string, user xraygrpc.ManagedUser) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, "push")
	f.tags = append(f.tags, tag)
	f.pushed = append(f.pushed, user)
	return f.addErr
}

func (f *fakePusher) lastAdd() xraygrpc.ManagedUser {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushed[len(f.pushed)-1]
}

func (f *fakePusher) removedList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

func (f *fakePusher) counts() (adds, removes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushed), len(f.removed)
}

func (f *fakePusher) RemoveUser(_ context.Context, tag, email string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, "remove")
	f.tags = append(f.tags, tag)
	f.removed = append(f.removed, email+" off "+tag)
	return f.removeErr
}

// fakeParseSource is the config watcher's roster-parse seam: the file's
// clients as the watcher last parsed them, plus a version that bumps on
// every parse (0 = nothing parsed yet).
type fakeParseSource struct {
	mu      sync.Mutex
	parse   xrayconfig.RosterParse
	version uint64
}

func (f *fakeParseSource) Roster() (xrayconfig.RosterParse, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parse, f.version
}

func (f *fakeParseSource) set(clients map[string]xrayconfig.Client) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.parse = xrayconfig.RosterParse{
		Labels:  map[string]xrayconfig.User{},
		Clients: clients,
	}
	f.version++
}

// echo models the watcher re-parsing the file after the renderer wrote it:
// the plan's appends and removals land in the parse, version bumped.
func (f *fakeParseSource) clientCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.parse.Clients)
}

func (f *fakeParseSource) echo(plan xrayconfig.RenderPlan) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clients := map[string]xrayconfig.Client{}
	for email, client := range f.parse.Clients {
		clients[email] = client
	}
	for tag, ops := range plan.Adds {
		for _, op := range ops {
			client := clients[op.Email]
			client.ClientID = op.ID
			if !slices.Contains(client.Inbounds, tag) {
				client.Inbounds = append(client.Inbounds, tag)
			}
			clients[op.Email] = client
		}
	}
	for tag, emails := range plan.Removes {
		for _, email := range emails {
			client := clients[email]
			client.Inbounds = slices.DeleteFunc(client.Inbounds, func(candidate string) bool { return candidate == tag })
			clients[email] = client
		}
	}
	f.parse = xrayconfig.RosterParse{Labels: f.parse.Labels, Clients: clients}
	f.version++
}

type fakeStatus struct {
	mu     sync.Mutex
	status string
}

func (f *fakeStatus) Latest(context.Context) (xraystatus.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return xraystatus.Status{Status: f.status}, nil
}

func (f *fakeStatus) set(status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// --- harness ---

const testDocument = `{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless", "port": 443,
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]},
     "streamSettings": {"network": "tcp", "security": "reality"}},
    {"tag": "vless-ws", "protocol": "vless", "port": 2053,
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing"}]},
     "streamSettings": {"network": "ws", "security": "tls"}},
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": []}},
    {"protocol": "vless", "settings": {"clients": []}}
  ]
}`

type harness struct {
	service  *roster.Service
	store    *fakeStore
	renderer *fakeRenderer
	pusher   *fakePusher
	views    *fakeViews
	parses   *fakeParseSource
	status   *fakeStatus
	changes  chan struct{}
	events   *[]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	view, err := xrayconfig.ParseView([]byte(testDocument))
	if err != nil {
		t.Fatalf("parse view: %v", err)
	}
	events := &[]string{}
	views := &fakeViews{view: view}
	// The parse mirrors the fixture document: the file's roster as the
	// watcher would have parsed it before any test drift.
	parses := &fakeParseSource{version: 1, parse: xrayconfig.RosterParse{
		Labels: map[string]xrayconfig.User{},
		Clients: map[string]xrayconfig.Client{
			"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
		},
	}}
	h := &harness{
		store:    newFakeStore(),
		renderer: &fakeRenderer{events: events, parses: parses},
		pusher:   &fakePusher{events: events},
		views:    views,
		parses:   parses,
		status:   &fakeStatus{status: "running"},
		changes:  make(chan struct{}, 1),
		events:   events,
	}
	h.service = roster.NewService(h.store, views, parses, h.renderer, h.pusher, h.status, h.changes).
		WithSettleWait(2 * time.Second).
		WithStatusPoll(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.service.Start(ctx)
	return h
}

func (h *harness) add(t *testing.T, email, clientID string, inbounds []string) roster.MutationResult {
	t.Helper()
	result, err := h.service.Add(context.Background(), email, clientID, inbounds)
	if err != nil {
		t.Fatalf("add %s: %v", email, err)
	}
	return result
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: still false after 2s", what)
}

// --- the slices ---

// The happy path (user-management spec §4): stored, rendered, pushed — file
// before live — and the mutation answers with the record and a synced state.
func TestAddStoresRendersPushes(t *testing.T) {
	h := newHarness(t)

	result := h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	if result.Sync != roster.Synced {
		t.Errorf("sync = %q, want synced once the apply settled", result.Sync)
	}
	if result.User.Email != "alice@example.com" || result.User.ClientID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("record = %+v", result.User)
	}
	if !slices.Equal(result.User.Inbounds, []string{"vless-vision", "vless-ws"}) {
		t.Errorf("record inbounds = %v", result.User.Inbounds)
	}

	// The render carries the attach-time flow — vision's first client sets
	// it; ws has no vision flow — and the push matches, per inbound.
	plan := h.renderer.lastPlan()
	if got := plan.Adds["vless-vision"]; len(got) != 1 || got[0].Flow != "xtls-rprx-vision" {
		t.Errorf("vision render add = %+v, want the copied vision flow", got)
	}
	if got := plan.Adds["vless-ws"]; len(got) != 1 || got[0].Flow != "" {
		t.Errorf("ws render add = %+v, want an empty flow", got)
	}
	if !slices.Equal(h.pusher.tags, []string{"vless-vision", "vless-ws"}) {
		t.Errorf("pushed tags = %v", h.pusher.tags)
	}
	if h.pusher.pushed[0].ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("pushed user = %+v", h.pusher.pushed[0])
	}

	// File render strictly before live push, per inbound.
	if !slices.Equal(*h.events, []string{"render", "push", "push"}) {
		t.Errorf("apply order = %v, want render then pushes", *h.events)
	}
}

// An absent Client ID is generated server-side (user-management spec §5).
func TestAddGeneratesTheClientID(t *testing.T) {
	h := newHarness(t)
	result := h.add(t, "alice@example.com", "", []string{"vless-vision"})
	if result.User.ClientID == "" {
		t.Fatal("a generated Client ID must be stored")
	}
	if !looksLikeUUID(result.User.ClientID) {
		t.Errorf("generated Client ID %q is not a UUID", result.User.ClientID)
	}
	if h.pusher.pushed[0].ID != result.User.ClientID {
		t.Error("the generated ID is what xray gets")
	}
}

func looksLikeUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
		default:
			return false
		}
	}
	return true
}

// Every validation violation is a 409-class conflict with a machine-readable
// reason, and writes nothing (user-management spec §5).
func TestAddValidationConflicts(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", nil)

	for _, test := range []struct {
		name     string
		email    string
		clientID string
		inbounds []string
		reason   string
	}{
		{"empty email", "", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", nil, roster.ReasonEmailInvalid},
		{"email taken case-insensitively", "Alice@Example.COM", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", nil, roster.ReasonEmailTaken},
		{"invalid client ID", "bob@example.com", "not-a-uuid", nil, roster.ReasonClientIDInvalid},
		{"client ID taken", "bob@example.com", "1D37A118-4F1B-4DC0-9E3C-3426B07518DF", nil, roster.ReasonClientIDTaken},
		{"unknown inbound", "bob@example.com", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"no-such"}, roster.ReasonUnknownInbound},
		{"non-vless inbound", "bob@example.com", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"trojan"}, roster.ReasonUnknownInbound},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.service.Add(context.Background(), test.email, test.clientID, test.inbounds)
			var conflict *roster.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want a ConflictError", err)
			}
			if conflict.Reason != test.reason {
				t.Errorf("reason = %q, want %q", conflict.Reason, test.reason)
			}
		})
	}

	if got := len(h.store.byMail); got != 1 {
		t.Errorf("stored users = %d, want 1 — a conflict writes nothing", got)
	}
}

// A profile-less user — zero inbounds — is stored with nothing to apply:
// the file and xray change only for attachments (user-management spec §3).
func TestAddWithZeroInboundsAppliesNothing(t *testing.T) {
	h := newHarness(t)
	result := h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", nil)
	if result.Sync != roster.Synced {
		t.Errorf("sync = %q, want synced — there is nothing to apply", result.Sync)
	}
	if len(h.renderer.plans) != 0 || len(h.pusher.pushed) != 0 {
		t.Error("no render and no push for a profile-less user")
	}
}

// xray down at add time: the change is stored, the mutation answers failed,
// the user carries the failed mark, and a config-watch fire retries the
// apply (user-management spec §7).
func TestApplyFailureSurfacesAndRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.status.set("stopped")
	h.pusher.addErr = errors.New("connect: connection refused")

	result := h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	if result.Sync != roster.Failed {
		t.Errorf("sync = %q, want failed — the store succeeded but the push did not", result.Sync)
	}
	if states := h.service.UserStates(); states["alice@example.com"] != roster.ApplyFailed {
		t.Errorf("user states = %v, want alice apply-failed", states)
	}
	if h.service.Sync() != roster.Failed {
		t.Errorf("roster sync = %q, want failed", h.service.Sync())
	}

	// xray returns; the next config-watch fire retries and settles.
	h.pusher.addErr = nil
	h.changes <- struct{}{}
	eventually(t, "synced after the retry", func() bool {
		return h.service.Sync() == roster.Synced
	})
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean after the retry", states)
	}
	if got := len(h.pusher.pushed); got != 2 {
		t.Errorf("pushes = %d, want the failed attempt and the retry", got)
	}
}

// A failed render (the config file is unwritable) never reaches the push,
// and an xray status transition to running retries the apply (§7).
func TestRenderFailureSkipsPushAndRetriesOnXrayRecovery(t *testing.T) {
	h := newHarness(t)
	h.status.set("stopped")
	h.renderer.err = errors.New("write /usr/local/etc/xray: permission denied")

	result := h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	if result.Sync != roster.Failed {
		t.Errorf("sync = %q, want failed", result.Sync)
	}
	if len(h.pusher.pushed) != 0 {
		t.Error("the push must not run when the file render failed — file before live")
	}

	h.renderer.err = nil
	h.status.set("running") // the status transition itself is the retry trigger
	eventually(t, "synced after the status transition", func() bool {
		return h.service.Sync() == roster.Synced
	})
	if len(h.pusher.pushed) != 1 {
		t.Errorf("pushes = %d, want exactly the retry", len(h.pusher.pushed))
	}
}

// The mutation API succeeds once stored: an apply that outlasts the settle
// window answers pending, and the background pass still lands it (§4, §7).
func TestAddAnswersPendingWhileTheApplyRuns(t *testing.T) {
	h := newHarness(t)
	h.renderer.block = make(chan struct{})
	h.service.WithSettleWait(50 * time.Millisecond)

	result := h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	if result.Sync != roster.Pending {
		t.Errorf("sync = %q, want pending while the apply runs", result.Sync)
	}
	if states := h.service.UserStates(); states["alice@example.com"] != roster.ApplyPending {
		t.Errorf("user states = %v, want alice pending", states)
	}

	close(h.renderer.block)
	eventually(t, "the apply lands after the mutation returned", func() bool {
		return h.service.Sync() == roster.Synced
	})
}

// The add dialog's inbound options (user-management spec §6): the tagged
// VLESS inbounds, each labelled tag + protocol · security · transport :port.
func TestInboundOptions(t *testing.T) {
	h := newHarness(t)
	options := h.service.InboundOptions()
	if len(options) != 2 {
		t.Fatalf("options = %+v, want the two tagged VLESS inbounds", options)
	}
	if options[0].Tag != "vless-vision" || options[0].Label != "VLESS · Reality · tcp :443" {
		t.Errorf("option[0] = %+v", options[0])
	}
	if options[1].Tag != "vless-ws" || options[1].Label != "VLESS · TLS · ws :2053" {
		t.Errorf("option[1] = %+v", options[1])
	}
}

// --- the edit slices (issue #54) ---

func (h *harness) edit(t *testing.T, email string, req roster.EditRequest) roster.MutationResult {
	t.Helper()
	result, err := h.service.Edit(context.Background(), email, req)
	if err != nil {
		t.Fatalf("edit %s: %v", email, err)
	}
	return result
}

func (h *harness) disable(t *testing.T, email string) roster.SyncState {
	t.Helper()
	sync, disabled, err := h.service.Disable(context.Background(), email)
	if err != nil {
		t.Fatalf("disable %s: %v", email, err)
	}
	if !disabled {
		t.Fatalf("disable %s: disabled = false, want a live disable", email)
	}
	return sync
}

// Changing the inbound selection applies live (user-management spec §4):
// the detached inbound loses the entry — file and running xray — and the
// newly attached one gains it, with the attach-time flow.
func TestEditAttachAndDetachApplyLive(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	result := h.edit(t, "alice@example.com", roster.EditRequest{Inbounds: []string{"vless-ws"}})
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced once the apply settled", result.Sync)
	}
	if !slices.Equal(result.User.Inbounds, []string{"vless-ws"}) {
		t.Errorf("record inbounds = %v", result.User.Inbounds)
	}

	plan := h.renderer.lastPlan()
	if got := plan.Removes["vless-vision"]; len(got) != 1 || got[0] != "alice@example.com" {
		t.Errorf("file removals = %v, want alice off vision", got)
	}
	if got := plan.Adds["vless-ws"]; len(got) != 1 || got[0].ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" || got[0].Flow != "" {
		t.Errorf("file adds = %+v, want alice onto ws with no flow", got)
	}
	if !slices.Equal(h.pusher.removed, []string{"alice@example.com off vless-vision"}) {
		t.Errorf("live removals = %v", h.pusher.removed)
	}
	// The add-time push on vision, then the edit's push on ws — the final
	// add carries the unchanged credential onto the new inbound.
	if len(h.pusher.pushed) != 2 {
		t.Fatalf("live adds = %+d entries, want the add-time and the edit's", len(h.pusher.pushed))
	}
	final := h.pusher.pushed[len(h.pusher.pushed)-1]
	if final.Email != "alice@example.com" || final.ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" || final.Flow != "" {
		t.Errorf("the edit's live add = %+v, want alice onto ws with no flow", final)
	}
}

// A Client ID rotation is remove + add on every attached inbound (§4): the
// running xray drops the old credential and takes the new one per inbound,
// and the file carries the new id in place.
func TestEditRotatesTheClientIDEverywhere(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	result := h.edit(t, "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
	})
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced", result.Sync)
	}
	if result.User.ClientID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("record Client ID = %q", result.User.ClientID)
	}

	// Per attached inbound: remove the old credential, add the new one —
	// the spec's remove+add pair, adjacent per inbound.
	if !slices.Equal(h.pusher.removed, []string{
		"alice@example.com off vless-vision",
		"alice@example.com off vless-ws",
	}) {
		t.Errorf("live removals = %v", h.pusher.removed)
	}
	// Two adds from the add itself, then the rotation's two — the final two
	// both carry the rotated credential.
	if len(h.pusher.pushed) != 4 {
		t.Fatalf("live adds = %d, want the add-time pair and the rotation pair", len(h.pusher.pushed))
	}
	for _, push := range h.pusher.pushed[2:] {
		if push.ID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
			t.Errorf("pushed = %+v, want the rotated credential", push)
		}
	}

	// The file rewrites the id in place on both inbounds.
	plan := h.renderer.lastPlan()
	if got := plan.Sets["vless-vision"]; len(got) != 1 || got[0].ID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("file sets vision = %+v", got)
	}
	if got := plan.Sets["vless-ws"]; len(got) != 1 {
		t.Errorf("file sets ws = %+v", got)
	}
	if len(plan.Adds) != 0 || len(plan.Removes) != 0 {
		t.Errorf("a pure rotation adds and removes nothing else: %+v", plan)
	}
}

// PATCH is idempotent (§5): repeating the same save stores nothing, renders
// nothing, pushes nothing — the same state.
func TestEditIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	renders := len(h.renderer.plans)
	adds := len(h.pusher.pushed)

	same := "1d37a118-4f1b-4dc0-9e3c-3426b07518df"
	result := h.edit(t, "alice@example.com", roster.EditRequest{
		ClientID: same,
		Inbounds: []string{"vless-vision"},
	})
	if result.Sync != roster.Synced {
		t.Errorf("sync = %q, want synced", result.Sync)
	}
	if len(h.renderer.plans) != renders || len(h.pusher.pushed) != adds {
		t.Error("a repeated save must render and push nothing")
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
}

// Detaching every inbound keeps the user in the roster — profile-less, not
// disabled (CONTEXT.md) — and takes them off the one inbound they had.
func TestEditCanDetachEveryInbound(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	result := h.edit(t, "alice@example.com", roster.EditRequest{Inbounds: []string{}})
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced — nothing to apply to xray", result.Sync)
	}
	if len(result.User.Inbounds) != 0 {
		t.Errorf("record inbounds = %v, want none", result.User.Inbounds)
	}
	if !slices.Equal(h.pusher.removed, []string{"alice@example.com off vless-vision"}) {
		t.Errorf("live removals = %v", h.pusher.removed)
	}
	record, err := h.store.RosterRecord(context.Background(), "alice@example.com")
	if err != nil || len(record.Inbounds) != 0 {
		t.Errorf("record after detach-all = %+v, err %v — the roster keeps her", record, err)
	}
}

// Validation matches add (§5): unknown inbound, invalid or taken Client ID
// are conflicts with the machine-readable reason, and nothing is stored.
func TestEditValidationConflicts(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.add(t, "bob@example.com", "3d37a118-4f1b-4dc0-9e3c-3426b07518df", nil)
	edits := h.store.edits

	for _, test := range []struct {
		name string
		req  roster.EditRequest
		want string
	}{
		{"unknown inbound", roster.EditRequest{Inbounds: []string{"no-such"}}, roster.ReasonUnknownInbound},
		{"non-vless inbound", roster.EditRequest{Inbounds: []string{"trojan"}}, roster.ReasonUnknownInbound},
		{"invalid client ID", roster.EditRequest{ClientID: "not-a-uuid"}, roster.ReasonClientIDInvalid},
		{"client ID taken", roster.EditRequest{ClientID: "3D37A118-4F1B-4DC0-9E3C-3426B07518DF"}, roster.ReasonClientIDTaken},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.service.Edit(context.Background(), "alice@example.com", test.req)
			var conflict *roster.ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want a ConflictError", err)
			}
			if conflict.Reason != test.want {
				t.Errorf("reason = %q, want %q", conflict.Reason, test.want)
			}
		})
	}
	if h.store.edits != edits {
		t.Errorf("store edits = %d, want %d — a conflict writes nothing", h.store.edits, edits)
	}
}

// Editing an email the roster does not carry is a not-found the API answers
// with 404.
func TestEditUnknownEmailIsNotFound(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.Edit(context.Background(), "ghost@example.com", roster.EditRequest{Inbounds: []string{}})
	var missing *roster.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want a NotFoundError", err)
	}
}

// A second edit while the first change is still applying replaces the
// pending change but keeps converging the first edit's tags (§7): whatever
// partially landed, the final pass pushes the final record everywhere it
// belongs.
func TestEditWhilePendingConvergesBothChanges(t *testing.T) {
	h := newHarness(t)
	h.renderer.block = make(chan struct{})
	h.service.WithSettleWait(50 * time.Millisecond)

	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.edit(t, "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
		Inbounds: []string{"vless-vision", "vless-ws"},
	})

	close(h.renderer.block)
	eventually(t, "synced with the final state everywhere", func() bool {
		return h.service.Sync() == roster.Synced
	})

	// The final pass carries the rotated credential on both inbounds —
	// vision from the first change, ws from the second.
	if len(h.pusher.pushed) != 2 {
		t.Fatalf("live adds = %+v, want the final credential on both inbounds", h.pusher.pushed)
	}
	for _, push := range h.pusher.pushed {
		if push.ID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
			t.Errorf("pushed = %+v, want the rotated credential", push)
		}
	}
	for _, tag := range []string{"vless-vision", "vless-ws"} {
		if !slices.Contains(h.pusher.tags, tag) {
			t.Errorf("pushed tags = %v, want %s covered", h.pusher.tags, tag)
		}
	}
}

// A failed rotate re-saved with the same (already stored) Client ID must
// still push remove+add (§7 re-saving retries): xray holds the old
// credential under the email, and a plain add would read "already exists"
// as success — leaving the old ID authenticating forever (issue #54
// review).
func TestResaveOfAFailedRotateKeepsTheRemoveAddPair(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	// The first rotate fails at the remove half — xray unreachable — so the
	// old credential stays live under the email.
	h.pusher.removeErr = errors.New("connect: connection refused")
	first := h.edit(t, "alice@example.com", roster.EditRequest{ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df"})
	if first.Sync != roster.Failed {
		t.Fatalf("first rotate sync = %q, want failed", first.Sync)
	}

	// xray returns; the admin re-saves the same values.
	h.pusher.removeErr = nil
	again := h.edit(t, "alice@example.com", roster.EditRequest{ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df"})
	if again.Sync != roster.Synced {
		t.Fatalf("re-save sync = %q, want synced", again.Sync)
	}

	// The re-save's pass must remove before adding again — a bare add would
	// be swallowed by xray's "already exists" while the old credential
	// keeps authenticating. The events log carries both attempts' removes.
	removes := 0
	for _, event := range *h.events {
		if event == "remove" {
			removes++
		}
	}
	if removes != 2 {
		t.Errorf("remove events = %d (events %v), want the failed attempt and the re-save's remove-before-add", removes, *h.events)
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
}

// A failed detach re-saved with the same (already stored) empty set must
// re-queue the detach (§7): consuming the pending change without applying
// it would strand the roster failed forever and leave xray serving the
// detached user (issue #54 review).
func TestResaveOfAFailedDetachRetriesTheRemoval(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	h.pusher.removeErr = errors.New("connect: connection refused")
	first := h.edit(t, "alice@example.com", roster.EditRequest{Inbounds: []string{}})
	if first.Sync != roster.Failed {
		t.Fatalf("first detach sync = %q, want failed", first.Sync)
	}

	h.pusher.removeErr = nil
	again := h.edit(t, "alice@example.com", roster.EditRequest{Inbounds: []string{}})
	if again.Sync != roster.Synced {
		t.Fatalf("re-save sync = %q, want synced — the detach retries", again.Sync)
	}

	count := 0
	for _, removed := range h.pusher.removed {
		if removed == "alice@example.com off vless-vision" {
			count++
		}
	}
	if count < 2 {
		t.Errorf("removals = %d, want the failed attempt and the re-save's retry", count)
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
}

// A PATCH that keeps the stored selection (no inbounds field) must not be
// blocked by a stored attachment the config has since dropped (§5 body
// fields optional): the vanished tag is pruned from the record — the
// inbound is gone from xray too — and the rest of the edit applies.
func TestEditKeptSelectionPrunesVanishedInbounds(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	pushes := len(h.pusher.pushed)
	removes := len(h.pusher.removed)

	// The config loses vless-vision between the add and the edit — an empty
	// inbound view is the cleanest stand-in.
	h.views.view = xrayconfig.View{}

	result := h.edit(t, "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df", // rotate only, inbounds kept
	})
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced — nothing remains to apply", result.Sync)
	}
	if len(result.User.Inbounds) != 0 {
		t.Errorf("record inbounds = %v, want the vanished tag pruned", result.User.Inbounds)
	}
	if result.User.ClientID != "2d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("record Client ID = %q, want the rotated one", result.User.ClientID)
	}
	if len(h.pusher.pushed) != pushes || len(h.pusher.removed) != removes {
		t.Errorf("pushes = %+v removes = %v — a vanished inbound is not a push target", h.pusher.pushed, h.pusher.removed)
	}
}

// --- the disable and enable slices (issues #55, #58, ADR-0007) ---

// The remove acceptance path (user-management spec §4): stored first, then
// rendered out of every attached inbound's clients array and pushed off the
// running xray — file before live, per inbound, idempotent on retry.
func TestDisableDetachesEverywhere(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	sync, disabled, err := h.service.Disable(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !disabled {
		t.Fatal("disabled = false, want a live disable")
	}
	if sync != roster.Synced {
		t.Fatalf("sync = %q, want synced once the apply settled", sync)
	}

	plan := h.renderer.lastPlan()
	if got := plan.Removes["vless-vision"]; len(got) != 1 || got[0] != "alice@example.com" {
		t.Errorf("file removals vision = %v", got)
	}
	if got := plan.Removes["vless-ws"]; len(got) != 1 || got[0] != "alice@example.com" {
		t.Errorf("file removals ws = %v", got)
	}
	if !slices.Equal(h.pusher.removed, []string{
		"alice@example.com off vless-vision",
		"alice@example.com off vless-ws",
	}) {
		t.Errorf("live removals = %v", h.pusher.removed)
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
	if _, err := h.store.RosterRecord(context.Background(), "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("record after disable = %v, want her out of the roster", err)
	}
}

// Enable re-applies the stored attachments like any roster change (§4,
// ADR-0007): the file gains the entries again and the running xray is
// pushed the stored credential on every attached inbound.
func TestEnableReappliesStoredAttachments(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.disable(t, "alice@example.com")
	adds := len(h.pusher.pushed)

	result, err := h.service.Enable(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if result.Sync != roster.Synced {
		t.Fatalf("sync = %q, want synced once the apply settled", result.Sync)
	}
	if result.User.Email != "alice@example.com" || result.User.ClientID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("record = %+v, want alice's stored credential", result.User)
	}
	if len(h.pusher.pushed) <= adds {
		t.Fatalf("live adds = %+v, want the stored credential pushed again", h.pusher.pushed)
	}
	latest := h.pusher.lastAdd()
	if latest.Email != "alice@example.com" || latest.ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("latest push = %+v, want alice's stored credential", latest)
	}
	plan := h.renderer.lastPlan()
	if got := plan.Adds["vless-vision"]; len(got) != 1 || got[0].ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("file adds = %+v, want alice's stored credential", got)
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
	record, err := h.store.RosterRecord(context.Background(), "alice@example.com")
	if err != nil || len(record.Inbounds) != 1 {
		t.Errorf("record after enable = %+v, err %v", record, err)
	}
}

// Enable prunes attachments to inbounds the config no longer carries — a
// push to a dead tag would only answer "failed to get handler" — and
// enables the rest.
func TestEnablePrunesVanishedInbounds(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})
	h.disable(t, "alice@example.com")

	// The ws inbound leaves the config while alice is disabled.
	h.views.view, _ = xrayconfig.ParseView([]byte(`{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless", "port": 443,
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]}},
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": []}}
  ]
}`))

	result, err := h.service.Enable(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !slices.Equal(result.User.Inbounds, []string{"vless-vision"}) {
		t.Errorf("record inbounds = %v, want vision only", result.User.Inbounds)
	}
	record, err := h.store.RosterRecord(context.Background(), "alice@example.com")
	if err != nil || !slices.Equal(record.Inbounds, []string{"vless-vision"}) {
		t.Errorf("stored record = %+v, err %v, want the pruned set", record, err)
	}
	latest := h.pusher.lastAdd()
	if latest.Email != "alice@example.com" || latest.ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" {
		t.Errorf("latest push = %+v, want alice pushed onto vision only", latest)
	}
	if len(h.pusher.tags) == 0 || h.pusher.tags[len(h.pusher.tags)-1] != "vless-vision" {
		t.Errorf("push tags = %v, want the last onto vless-vision", h.pusher.tags)
	}
}

// Enable is idempotent: a live user enables as a plain success with
// nothing to apply; an unknown email is a not-found.
func TestEnableIsIdempotentAndUnknownEmailIsNotFound(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	renders := len(h.renderer.plans)
	adds := len(h.pusher.pushed)

	if _, err := h.service.Enable(context.Background(), "Alice@Example.com"); err != nil {
		t.Errorf("enable a live user: %v, want an idempotent success", err)
	}
	if len(h.renderer.plans) != renders || len(h.pusher.pushed) != adds {
		t.Error("an idempotent enable renders and pushes nothing")
	}

	_, err := h.service.Enable(context.Background(), "never-was@example.com")
	var missing *roster.NotFoundError
	if !errors.As(err, &missing) {
		t.Errorf("enable a stranger = %v, want a NotFoundError", err)
	}
}

// xray down at enable time: the revival is stored, the answer is failed,
// and the usual retry machine converges it (§7).
func TestEnableFailureSurfacesAndRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.status.set("stopped")
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.disable(t, "alice@example.com")

	h.pusher.addErr = errors.New("connect: connection refused")
	h.service.WithSettleWait(50 * time.Millisecond)
	result, err := h.service.Enable(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if result.Sync != roster.Failed {
		t.Fatalf("sync = %q, want failed — stored, but the push did not land", result.Sync)
	}

	h.pusher.addErr = nil
	h.changes <- struct{}{}
	eventually(t, "synced after the retry", func() bool {
		return h.service.Sync() == roster.Synced
	})
	record, err := h.store.RosterRecord(context.Background(), "alice@example.com")
	if err != nil || len(record.Inbounds) != 1 {
		t.Errorf("record after the retry = %+v, err %v", record, err)
	}
}

// xray down at disable time: the disable is stored, the answer is failed,
// the row carries the failed mark, and a config-watch fire retries (§7).
func TestDisableFailureSurfacesAndRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.status.set("stopped")
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	h.pusher.removeErr = errors.New("connect: connection refused")
	sync := h.disable(t, "alice@example.com")
	if sync != roster.Failed {
		t.Fatalf("sync = %q, want failed — stored, but the push did not land", sync)
	}
	if states := h.service.UserStates(); states["alice@example.com"] != roster.ApplyFailed {
		t.Errorf("user states = %v, want alice apply-failed", states)
	}

	h.pusher.removeErr = nil
	h.changes <- struct{}{}
	eventually(t, "synced after the retry", func() bool {
		return h.service.Sync() == roster.Synced
	})
	if _, err := h.store.RosterRecord(context.Background(), "alice@example.com"); !errors.Is(err, users.ErrRosterNotFound) {
		t.Errorf("record after the retry = %v, want gone", err)
	}
}

// Removing a user whose previous change never finished applying detaches
// from every tag that might still hold them live (§7 merge).
func TestRemoveWhilePendingDetachesEveryTouchedTag(t *testing.T) {
	h := newHarness(t)
	h.renderer.block = make(chan struct{})
	h.service.WithSettleWait(50 * time.Millisecond)

	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	// A still-pending edit attaching ws — its ops must feed the removal.
	if _, err := h.service.Edit(context.Background(), "alice@example.com", roster.EditRequest{
		Inbounds: []string{"vless-vision", "vless-ws"},
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}

	h.disable(t, "alice@example.com")
	close(h.renderer.block)
	eventually(t, "synced after the merged removal", func() bool {
		return h.service.Sync() == roster.Synced
	})

	removes := map[string]bool{}
	for _, removed := range h.pusher.removed {
		removes[removed] = true
	}
	for _, tag := range []string{"vless-vision", "vless-ws"} {
		if !removes["alice@example.com off "+tag] {
			t.Errorf("live removals = %v, want %s covered", h.pusher.removed, tag)
		}
	}
}

// --- the convergence slices (issue #56) ---

func rosterParseOf(clients map[string]xrayconfig.Client) xrayconfig.RosterParse {
	return xrayconfig.RosterParse{Labels: map[string]xrayconfig.User{}, Clients: clients}
}

// A store user hand-deleted from the config is restored — file render and
// live push — on the next watch tick, with the store untouched (user-
// management spec §4: store wins, with adoption; an ansible re-run that
// renders no clients leaves the roster alone).
func TestConvergeRestoresAUserDeletedFromTheConfig(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	// The hand edit: alice gone from the file, everything else unchanged.
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
	})
	h.changes <- struct{}{}

	eventually(t, "the file restored and the pushes landed", func() bool {
		renders := 0
		h.renderer.mu.Lock()
		for _, plan := range h.renderer.plans {
			if got := plan.Adds["vless-vision"]; len(got) > 0 && got[0].Email == "alice@example.com" {
				renders++
			}
		}
		h.renderer.mu.Unlock()
		return renders > 0 && h.service.Sync() == roster.Synced
	})

	plan := h.renderer.lastPlan()
	if got := plan.Adds["vless-vision"]; len(got) != 1 || got[0].ID != "1d37a118-4f1b-4dc0-9e3c-3426b07518df" || got[0].Flow != "xtls-rprx-vision" {
		t.Errorf("converged adds vision = %+v, want alice with the attach-time flow", got)
	}
	if got := plan.Adds["vless-ws"]; len(got) != 1 {
		t.Errorf("converged adds ws = %+v", got)
	}
	if got := plan.Removes; len(got) != 0 {
		t.Errorf("convergence removes nothing: %v", got)
	}
	// The roster store was never touched by the drift.
	record, err := h.store.RosterRecord(context.Background(), "alice@example.com")
	if err != nil || !slices.Equal(record.Inbounds, []string{"vless-vision", "vless-ws"}) {
		t.Errorf("record after converge = %+v / %v, want untouched", record, err)
	}
}

// An inbound removed from the config drops that attachment from the store;
// other attachments keep working and nothing is pushed to the dead tag.
// A user left with none stays in the store, profile-less and manageable
// (user-management spec §4).
func TestConvergePrunesAttachmentsToVanishedInbounds(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})
	h.add(t, "bob@example.com", "3d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-ws"})

	// vless-ws disappears from the config: the view loses it, alice's parse
	// carries vision only, bob carried nothing else.
	h.views.view, _ = xrayconfig.ParseView([]byte(`{
  "inbounds": [
    {"tag": "vless-vision", "protocol": "vless", "port": 443,
     "settings": {"clients": [{"email": "existing@example.com", "id": "uuid-existing", "flow": "xtls-rprx-vision"}]}},
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": []}}
  ]
}`))
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision"}},
		"alice@example.com":    {ClientID: "1d37a118-4f1b-4dc0-9e3c-3426b07518df", Inbounds: []string{"vless-vision"}},
	})
	h.changes <- struct{}{}

	eventually(t, "the store pruned", func() bool {
		alice, err := h.store.RosterRecord(context.Background(), "alice@example.com")
		return err == nil && slices.Equal(alice.Inbounds, []string{"vless-vision"})
	})
	if sync := h.service.Sync(); sync != roster.Synced {
		t.Errorf("sync = %q, want synced — pruning is a store write, not an apply", sync)
	}
	if len(h.pusher.removed) != 0 {
		t.Errorf("pushes = %v — a vanished inbound is not a push target", h.pusher.removed)
	}
	bob, err := h.store.RosterRecord(context.Background(), "bob@example.com")
	if err != nil || len(bob.Inbounds) != 0 {
		t.Errorf("bob = %+v / %v, want pruned to profile-less and still listed", bob, err)
	}
}

// Convergence skips users whose own change is still unapplied — pending or
// failed, the apply loop is already converging them — and never touches
// gone users (adoption handles foreign clients).
func TestConvergeSkipsPendingGoneAndForeign(t *testing.T) {
	h := newHarness(t)
	h.pusher.addErr = errors.New("connect: connection refused")

	// The add failed at the push: stored, failed, pending — retrying.
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	eventually(t, "the failed add settled", func() bool {
		return h.service.UserStates()["alice@example.com"] == roster.ApplyFailed
	})
	renders := len(h.renderer.plans)
	pushes := len(h.pusher.pushed)

	// The file lost alice meanwhile. The watch fire retries the failed
	// apply, and convergence must not pile a second change on top of it.
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
	})
	h.changes <- struct{}{}
	eventually(t, "the retry attempt ran", func() bool {
		adds, _ := h.pusher.counts()
		return adds > pushes
	})
	h.renderer.mu.Lock()
	rendersAfterFire := len(h.renderer.plans)
	h.renderer.mu.Unlock()
	if rendersAfterFire > renders+1 {
		t.Errorf("renders = %d, want the retry only — convergence skips pending users", rendersAfterFire)
	}

	// The push recovers; the next watch fire retries and settles.
	h.pusher.addErr = nil
	h.changes <- struct{}{}
	eventually(t, "synced after the retry", func() bool {
		return h.service.Sync() == roster.Synced
	})

	// A gone user in the parse is nobody's business: still gone, no ops.
	h.disable(t, "alice@example.com")
	h.renderer.mu.Lock()
	renders = len(h.renderer.plans)
	h.renderer.mu.Unlock()
	pushes, _ = h.pusher.counts()
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
		"alice@example.com":    {ClientID: "1d37a118-4f1b-4dc0-9e3c-3426b07518df", Inbounds: []string{"vless-vision"}},
	})
	h.changes <- struct{}{}
	eventually(t, "the watch fire settled with no ops", func() bool {
		h.renderer.mu.Lock()
		defer h.renderer.mu.Unlock()
		return len(h.renderer.plans) == renders && h.service.Sync() == roster.Synced
	})
	adds, _ := h.pusher.counts()
	if adds != pushes {
		t.Errorf("pushes = %d, want %d — a gone user must not be re-applied", adds, pushes)
	}
}

// Convergence also runs once at startup: a file that drifted while the
// panel was down is restored without waiting for a watch fire.
func TestConvergeRunsAtStartup(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	// The file drifted while the panel was down: alice is gone from it.
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
	})

	// A fresh service over the drifted parse converges on its own.
	events := &[]string{}
	service := roster.NewService(h.store, h.views, h.parses, &fakeRenderer{events: events, parses: h.parses}, &fakePusher{events: events}, h.status, h.changes).
		WithSettleWait(2 * time.Second).
		WithStatusPoll(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.Start(ctx)

	eventually(t, "startup convergence restored alice", func() bool {
		return service.Sync() == roster.Synced && h.parses.clientCount() == 1
	})
	plan := h.renderer.lastPlan()
	if got := plan.Adds["vless-vision"]; len(got) != 1 || got[0].Email != "alice@example.com" {
		t.Errorf("startup converge plan = %+v, want alice restored", plan.Adds)
	}
}

// A user removed between the roster listing and the restore must not come
// back: convergence re-checks liveness before it enqueues (§5 — DELETE is
// final; a ghost with working credentials is the worst outcome).
func TestConvergeDoesNotResurrectARemovedUser(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	// The listing still carries alice; the removal lands right after it.
	h.store.vanishOnList = true
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
	})
	h.changes <- struct{}{}

	// The resurrection, if it happened, would render within the tick —
	// give the loop a fair window, then hold the line at the add's pass.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.renderer.mu.Lock()
		renders := len(h.renderer.plans)
		h.renderer.mu.Unlock()
		if renders != 1 {
			t.Fatalf("renders = %d, want just the add's — a removed user came back", renders)
		}
		adds, _ := h.pusher.counts()
		if adds != 1 {
			t.Fatalf("pushes = %d, want just the add's — a removed user came back", adds)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An unchanged parse converges to a no-op — the panel's own renders echo
// back through the watcher without re-rendering (no loops).
func TestConvergeIsQuietWhenTheFileMatches(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	renders := len(h.renderer.plans)

	// The parse matches the store (the panel's own render echoed back).
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
		"alice@example.com":    {ClientID: "1d37a118-4f1b-4dc0-9e3c-3426b07518df", Inbounds: []string{"vless-vision"}},
	})
	h.changes <- struct{}{}
	eventually(t, "the watch fire settled", func() bool {
		h.renderer.mu.Lock()
		defer h.renderer.mu.Unlock()
		return len(h.renderer.plans) == renders && h.service.Sync() == roster.Synced
	})
	if len(h.renderer.plans) != renders {
		t.Error("a matching parse must render nothing")
	}
}
