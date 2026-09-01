package roster_test

import (
	"context"
	"errors"
	"slices"
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
	mu     sync.Mutex
	byMail map[string]users.RosterRecord // lower(email) → record
	byID   map[string]string             // lower(client_id) → email
}

func newFakeStore() *fakeStore {
	return &fakeStore{byMail: map[string]users.RosterRecord{}, byID: map[string]string{}}
}

func (f *fakeStore) AddRosterUser(_ context.Context, user users.NewRosterUser, now time.Time) (users.RosterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email := strings.ToLower(user.Email)
	if _, ok := f.byMail[email]; ok {
		return users.RosterRecord{}, users.ErrEmailTaken
	}
	if _, ok := f.byID[strings.ToLower(user.ClientID)]; ok {
		return users.RosterRecord{}, users.ErrClientIDTaken
	}
	record := users.RosterRecord{
		Email: user.Email, ClientID: user.ClientID, Inbounds: user.Inbounds,
		CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}
	f.byMail[email] = record
	f.byID[strings.ToLower(user.ClientID)] = user.Email
	return record, nil
}

type fakeViews struct{ view xrayconfig.View }

func (f fakeViews) View() xrayconfig.View { return f.view }

type fakeRenderer struct {
	mu     sync.Mutex
	events *[]string // shared with the pusher to assert apply order
	adds   []map[string][]xrayconfig.NewClient
	err    error
	block  chan struct{} // non-nil: Render waits on it (the slow-apply test)
}

func (f *fakeRenderer) Render(_ context.Context, adds map[string][]xrayconfig.NewClient) (bool, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, "render")
	f.adds = append(f.adds, adds)
	return true, f.err
}

func (f *fakeRenderer) lastAdds() map[string][]xrayconfig.NewClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adds[len(f.adds)-1]
}

type fakePusher struct {
	mu     sync.Mutex
	events *[]string
	pushed []xraygrpc.ManagedUser
	tags   []string
	err    error
}

func (f *fakePusher) AddUser(_ context.Context, tag string, user xraygrpc.ManagedUser) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.events = append(*f.events, "push")
	f.tags = append(f.tags, tag)
	f.pushed = append(f.pushed, user)
	return f.err
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
	h := &harness{
		store:    newFakeStore(),
		renderer: &fakeRenderer{events: events},
		pusher:   &fakePusher{events: events},
		status:   &fakeStatus{status: "running"},
		changes:  make(chan struct{}, 1),
		events:   events,
	}
	h.service = roster.NewService(h.store, fakeViews{view: view}, h.renderer, h.pusher, h.status, h.changes).
		WithSettleWait(2 * time.Second).
		WithStatusPoll(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.service.Start(ctx)
	return h
}

func (h *harness) add(t *testing.T, email, clientID string, inbounds []string) roster.AddResult {
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
	adds := h.renderer.lastAdds()
	if got := adds["vless-vision"]; len(got) != 1 || got[0].Flow != "xtls-rprx-vision" {
		t.Errorf("vision render add = %+v, want the copied vision flow", got)
	}
	if got := adds["vless-ws"]; len(got) != 1 || got[0].Flow != "" {
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
	if len(h.renderer.adds) != 0 || len(h.pusher.pushed) != 0 {
		t.Error("no render and no push for a profile-less user")
	}
}

// xray down at add time: the change is stored, the mutation answers failed,
// the user carries the failed mark, and a config-watch fire retries the
// apply (user-management spec §7).
func TestApplyFailureSurfacesAndRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.status.set("stopped")
	h.pusher.err = errors.New("connect: connection refused")

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
	h.pusher.err = nil
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
