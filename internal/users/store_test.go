package users_test

import (
	"context"
	"testing"
	"time"

	"github.com/yet-an-other/xform/internal/users"
)

func TestStoreAccumulatesDurableTotals(t *testing.T) {
	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// One transaction per poll (SPEC.md §4): the first poll inserts the
	// roster, later polls accumulate onto it.
	if err := store.ApplyDeltas(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000},
		{Email: "bob@example.com", Up: 50, Down: 500},
	}, now); err != nil {
		t.Fatalf("apply first poll: %v", err)
	}
	if err := store.ApplyDeltas(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 25, Down: 250},
	}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("apply second poll: %v", err)
	}

	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("users = %d, want 2", len(list))
	}
	alice := list[0] // busiest first
	if alice.Email != "alice@example.com" {
		t.Fatalf("users[0] = %s, want alice@example.com (heaviest traffic first)", alice.Email)
	}
	if alice.UpBytesTotal != 125 || alice.DownBytesTotal != 1_250 {
		t.Errorf("alice totals = %d/%d, want 125/1250", alice.UpBytesTotal, alice.DownBytesTotal)
	}
	if alice.FirstSeen != now.Unix() {
		t.Errorf("alice first_seen = %d, want %d", alice.FirstSeen, now.Unix())
	}
	if alice.Protocol != nil || alice.Security != nil || alice.LastSeen != nil || alice.IPs != nil {
		t.Errorf("alice = %+v, want presence/config fields null until their slices land", alice)
	}
	if alice.Gone {
		t.Error("alice gone = true, want false until the roster-sync slice")
	}
	if bob := list[1]; bob.UpBytesTotal != 50 || bob.DownBytesTotal != 500 {
		t.Errorf("bob totals = %d/%d, want 50/500 (no new delta, unchanged)", bob.UpBytesTotal, bob.DownBytesTotal)
	}
}
