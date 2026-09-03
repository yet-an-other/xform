package roster_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/roster"
	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// --- the delete slice (issue #59, ADR-0007) ---

// Delete is two-phase: the row is marked deleting (disabled with it), the
// removal applies like a disable — rendered out of every inbound and pushed
// off the running xray, file before live — and once it settles, every
// stored trace is purged and the collector is told to drop its memory of
// the user.
func TestDeleteDetachesEverywhereThenPurges(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	sync, deleted, err := h.service.Delete(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("deleted = false, want a real delete")
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
	if !slices.Equal(h.pusher.removedList(), []string{
		"alice@example.com off vless-vision",
		"alice@example.com off vless-ws",
	}) {
		t.Errorf("live removals = %v", h.pusher.removedList())
	}
	if !slices.Equal(h.purges.list(), []string{"alice@example.com"}) {
		t.Errorf("collector purges = %v, want alice dropped from the observation memory", h.purges.list())
	}
	if purged := h.store.purgedList(); !slices.Equal(purged, []string{"alice@example.com"}) {
		t.Errorf("store purges = %v, want alice erased", purged)
	}
	if states := h.service.UserStates(); len(states) != 0 {
		t.Errorf("user states = %v, want clean", states)
	}
	if h.service.Sync() != roster.Synced {
		t.Errorf("sync state = %q, want synced after the purge", h.service.Sync())
	}
}

// A disabled user deletes too (ADR-0007): their stored attachments are
// detached and the rows purge once the removal settles.
func TestDeleteADisabledUser(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.disable(t, "alice@example.com")
	before := len(h.pusher.removedList())

	sync, deleted, err := h.service.Delete(context.Background(), "Alice@Example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted || sync != roster.Synced {
		t.Fatalf("deleted = %t, sync = %q; want a settled delete", deleted, sync)
	}
	// The delete re-detaches the stored attachment — a remove of an
	// already-absent user reads as applied (§7) — and purges.
	removals := h.pusher.removedList()[before:]
	if !slices.Equal(removals, []string{"alice@example.com off vless-vision"}) {
		t.Errorf("live removals after the delete = %v", removals)
	}
	if purged := h.store.purgedList(); !slices.Equal(purged, []string{"alice@example.com"}) {
		t.Errorf("store purges = %v", purged)
	}
}

// Delete is idempotent: an unknown (never-known or already-purged) email
// deletes nothing and answers a plain success.
func TestDeleteUnknownEmailIsIdempotent(t *testing.T) {
	h := newHarness(t)
	renders := len(h.renderer.plans)

	sync, deleted, err := h.service.Delete(context.Background(), "never-was@example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted {
		t.Error("deleted = true, want an idempotent nothing")
	}
	if sync != roster.Synced {
		t.Errorf("sync = %q, want synced", sync)
	}
	if len(h.renderer.plans) != renders {
		t.Error("an idempotent delete renders nothing")
	}
	if purged := h.store.purgedList(); len(purged) != 0 {
		t.Errorf("store purges = %v, want none", purged)
	}
}

// A delete in progress claims the email: the store answers taken, so a
// re-add waits instead of resurrecting a row whose credential may still be
// live out there (issue #59).
func TestDeletePendingBlocksReaddAndEnable(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	// Hold the apply mid-flight so the delete stays pending.
	h.renderer.block = make(chan struct{})
	deleteDone := make(chan struct{})
	var sync roster.SyncState
	go func() {
		defer close(deleteDone)
		sync, _, _ = h.service.Delete(context.Background(), "alice@example.com")
	}()
	eventually(t, "the delete to queue", func() bool { return h.service.Sync() == roster.Pending })

	if _, err := h.service.Add(context.Background(), "alice@example.com", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", nil); err == nil {
		t.Fatal("re-add during a pending delete succeeded, want a conflict")
	}
	var conflict *roster.ConflictError
	if _, addErr := h.service.Add(context.Background(), "alice@example.com", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", nil); !errors.As(addErr, &conflict) || conflict.Reason != roster.ReasonEmailTaken {
		t.Errorf("re-add during a pending delete = %v, want %q", addErr, roster.ReasonEmailTaken)
	}
	if _, err := h.service.Enable(context.Background(), "alice@example.com"); err == nil {
		t.Error("enable during a pending delete = nil, want not-found")
	}

	close(h.renderer.block)
	<-deleteDone
	if sync != roster.Synced {
		t.Errorf("delete sync = %q, want synced once unblocked", sync)
	}
}

// An unfinished change's tags all join the delete's detach set — its pushes
// may have half-landed, and a failed apply never strands a live credential.
func TestDeleteCoversAnUnfinishedChange(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})

	// A client ID rotation on vision that never lands: the remove push fails.
	h.pusher.removeErr = errors.New("connect: connection refused")
	if _, err := h.service.Edit(context.Background(), "alice@example.com", roster.EditRequest{
		ClientID: "2d37a118-4f1b-4dc0-9e3c-3426b07518df",
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	h.pusher.removeErr = nil

	sync, deleted, err := h.service.Delete(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted || sync != roster.Synced {
		t.Fatalf("deleted = %t, sync = %q; want a settled delete", deleted, sync)
	}
	// The last pass removes her from both inbounds — vision twice is fine,
	// a remove of an absent user reads as applied (§7).
	removals := h.pusher.removedList()
	if !slices.Contains(removals, "alice@example.com off vless-vision") ||
		!slices.Contains(removals, "alice@example.com off vless-ws") {
		t.Errorf("live removals = %v, want both inbounds covered", removals)
	}
	if purged := h.store.purgedList(); !slices.Equal(purged, []string{"alice@example.com"}) {
		t.Errorf("store purges = %v", purged)
	}
}

// xray unreachable at delete time: the removal is stored, the answer is
// failed, and the usual retry machine converges it — the purge only lands
// once the removal does (§7, issue #59).
func TestDeleteFailureSurfacesAndRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	h.pusher.removeErr = errors.New("connect: connection refused")
	sync, deleted, err := h.service.Delete(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("deleted = false, want a stored delete")
	}
	if sync != roster.Failed {
		t.Fatalf("sync = %q, want failed", sync)
	}
	if purged := h.store.purgedList(); len(purged) != 0 {
		t.Fatalf("store purges = %v, want none while the removal is stuck", purged)
	}
	if !h.store.isDeleting("alice@example.com") {
		t.Fatal("the row must stay marked deleting while the removal retries")
	}

	h.pusher.removeErr = nil
	h.changes <- struct{}{}
	eventually(t, "the retry to converge the delete", func() bool {
		return len(h.store.purgedList()) == 1
	})
	if !slices.Equal(h.store.purgedList(), []string{"alice@example.com"}) {
		t.Errorf("store purges = %v", h.store.purgedList())
	}
	if h.service.Sync() != roster.Synced {
		t.Errorf("sync state = %q, want synced after the purge", h.service.Sync())
	}
}

// A purge that fails after the removal settled keeps retrying on the usual
// triggers — the rows stay deleting until the erase lands (issue #59).
func TestDeletePurgeFailureRetriesOnWatchFire(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})

	h.store.purgeErr = errors.New("database is locked")
	sync, _, err := h.service.Delete(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if sync != roster.Failed {
		t.Fatalf("sync = %q, want failed while the purge cannot write", sync)
	}

	h.store.purgeErr = nil
	h.changes <- struct{}{}
	eventually(t, "the purge retry to land", func() bool {
		return len(h.store.purgedList()) == 1
	})
	if h.service.Sync() != roster.Synced {
		t.Errorf("sync state = %q, want synced after the purge", h.service.Sync())
	}
}

// A restart between the deleting mark and the purge re-queues the removal:
// the recovery pass detaches the stored attachments and the purge lands
// once it settles (issue #59).
func TestDeleteSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision", "vless-ws"})
	if err := h.store.MarkRosterDeleting(context.Background(), "alice@example.com", time.Now()); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	// A fresh service over the same store — the restarted panel.
	restarted := roster.NewService(h.store, h.views, h.parses, h.renderer, h.pusher, h.status, h.changes).
		WithSettleWait(2 * time.Second).
		WithStatusPoll(10 * time.Millisecond).
		WithPurgeNotifier(h.purges)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	restarted.Start(ctx)

	eventually(t, "the recovery pass to purge alice", func() bool {
		return len(h.store.purgedList()) == 1
	})
	removals := h.pusher.removedList()
	if !slices.Contains(removals, "alice@example.com off vless-vision") ||
		!slices.Contains(removals, "alice@example.com off vless-ws") {
		t.Errorf("live removals = %v, want the stored attachments detached", removals)
	}
}

// Convergence never revives a deleting user: the row is out of the live
// set, so a parse missing her re-applies nothing and the purge proceeds
// (store wins applies to live members only — issue #59).
func TestConvergeLeavesDeletingRowsAlone(t *testing.T) {
	h := newHarness(t)
	h.add(t, "alice@example.com", "1d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-vision"})
	h.add(t, "bob@example.com", "2d37a118-4f1b-4dc0-9e3c-3426b07518df", []string{"vless-ws"})
	if err := h.store.MarkRosterDeleting(context.Background(), "alice@example.com", time.Now()); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}

	// A parse that lost both users: convergence re-applies bob (live) and
	// must leave alice (deleting) to her own apply.
	h.parses.set(map[string]xrayconfig.Client{
		"existing@example.com": {ClientID: "uuid-existing", Inbounds: []string{"vless-vision", "vless-ws"}},
	})
	h.changes <- struct{}{}
	before := len(h.pusher.pushedList())
	eventually(t, "convergence to re-apply bob", func() bool {
		return len(h.pusher.pushedList()) > before
	})
	for _, user := range h.pusher.pushedList()[before:] {
		if user.Email == "alice@example.com" {
			t.Errorf("a deleting user must not be re-pushed: %+v", user)
		}
	}
	if purged := h.store.purgedList(); len(purged) != 0 {
		t.Errorf("store purges = %v, want none — alice's purge is her own apply's business", purged)
	}
	if !h.store.isDeleting("alice@example.com") {
		t.Error("alice must stay marked deleting")
	}
}
