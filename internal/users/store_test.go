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
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 100, Down: 1_000, SeenNow: true},
		{Email: "bob@example.com", Up: 50, Down: 500, SeenNow: true},
	}, nil, nil, now); err != nil {
		t.Fatalf("apply first poll: %v", err)
	}
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 25, Down: 250, SeenNow: true},
	}, nil, nil, now.Add(5*time.Second)); err != nil {
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
	// The traffic-delta heuristic (SPEC.md §3): movement inside the poll's
	// window marks the user seen now. alice moved bytes in both polls; bob
	// only in the first.
	if alice.LastSeen == nil || *alice.LastSeen != now.Add(5*time.Second).Unix() {
		t.Errorf("alice last_seen = %v, want the second poll %d", alice.LastSeen, now.Add(5*time.Second).Unix())
	}
	if bob := list[1]; bob.LastSeen == nil || *bob.LastSeen != now.Unix() {
		t.Errorf("bob last_seen = %v, want the first poll %d", bob.LastSeen, now.Unix())
	}
	if alice.Protocol != nil || alice.Security != nil || alice.IPs != nil {
		t.Errorf("alice = %+v, want config fields null until their slices land", alice)
	}
	if alice.Disabled {
		t.Error("alice disabled = true, want false until the roster-sync slice")
	}
	if bob := list[1]; bob.UpBytesTotal != 50 || bob.DownBytesTotal != 500 {
		t.Errorf("bob totals = %d/%d, want 50/500 (no new delta, unchanged)", bob.UpBytesTotal, bob.DownBytesTotal)
	}
}

func TestStorePersistsPresenceAndDurableLastSeen(t *testing.T) {
	store, err := users.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Unix(1_780_000_000, 0)

	// bob is online from two IPs; xray reports the most recent per-IP
	// last_seen. bob has never moved a byte — presence alone creates his row.
	if err := store.ApplyPoll(ctx, nil, []users.Presence{
		{Email: "bob@example.com", IPs: []string{"203.0.113.10", "203.0.113.11"}, LastSeen: now.Unix() - 7},
	}, nil, now); err != nil {
		t.Fatalf("apply presence: %v", err)
	}

	list, err := store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("users = %d, want 1 (presence upserts the row)", len(list))
	}
	bob := list[0]
	if bob.LastSeen == nil || *bob.LastSeen != now.Unix()-7 {
		t.Errorf("bob last_seen = %v, want the per-IP max %d", bob.LastSeen, now.Unix()-7)
	}
	if len(bob.IPs) != 2 || bob.IPs[0] != "203.0.113.10" || bob.IPs[1] != "203.0.113.11" {
		t.Errorf("bob last_ips = %v, want the persisted online set", bob.IPs)
	}
	if bob.FirstSeen != now.Unix() {
		t.Errorf("bob first_seen = %d, want %d", bob.FirstSeen, now.Unix())
	}

	// Next poll: bob is still online but his report is older than what the
	// store holds — last_seen never regresses; last_ips tracks the latest
	// observation. A zero-delta poll for alice does not count as seen.
	if err := store.ApplyPoll(ctx, []users.Delta{
		{Email: "alice@example.com", Up: 0, Down: 0},
	}, []users.Presence{
		{Email: "bob@example.com", IPs: []string{"198.51.100.7"}, LastSeen: now.Unix() - 100},
	}, nil, now.Add(5*time.Second)); err != nil {
		t.Fatalf("apply second poll: %v", err)
	}

	list, err = store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	byEmail := userByEmail(list)
	if got := byEmail["bob@example.com"].LastSeen; got == nil || *got != now.Unix()-7 {
		t.Errorf("bob last_seen = %v after an older report, want %d (never regresses)", got, now.Unix()-7)
	}
	if ips := byEmail["bob@example.com"].IPs; len(ips) != 1 || ips[0] != "198.51.100.7" {
		t.Errorf("bob last_ips = %v, want the latest observation [198.51.100.7]", ips)
	}
	if got := byEmail["alice@example.com"].LastSeen; got != nil {
		t.Errorf("alice last_seen = %v, want null — a zero delta is not activity", got)
	}

	// bob disconnects: no presence row, no delta. Last seen and last-known
	// IPs persist untouched.
	if err := store.ApplyPoll(ctx, nil, nil, nil, now.Add(10*time.Second)); err != nil {
		t.Fatalf("apply third poll: %v", err)
	}
	list, err = store.Users(ctx)
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	bob = userByEmail(list)["bob@example.com"]
	if bob.LastSeen == nil || *bob.LastSeen != now.Unix()-7 {
		t.Errorf("bob last_seen = %v after disconnect, want %d (durable)", bob.LastSeen, now.Unix()-7)
	}
	if len(bob.IPs) != 1 || bob.IPs[0] != "198.51.100.7" {
		t.Errorf("bob last_ips = %v after disconnect, want the last-known [198.51.100.7]", bob.IPs)
	}
}

func userByEmail(list []users.User) map[string]users.User {
	byEmail := map[string]users.User{}
	for _, user := range list {
		byEmail[user.Email] = user
	}
	return byEmail
}
