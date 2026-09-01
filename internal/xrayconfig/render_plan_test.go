package xrayconfig_test

import (
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// The edit render operations (user-management spec §4): detaching removes
// the entry from the managed array, and a Client ID rotation rewrites the
// id in place. Only the managed spans change; everything else stays
// byte-stable.
func TestRenderPlanDetachesTheEntry(t *testing.T) {
	document := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [
      {"email": "alice@example.com", "id": "uuid-alice"},
      {"email": "bob@example.com", "id": "uuid-bob"}, // the middle user
      {"email": "carol@example.com", "id": "uuid-carol"}
    ]}
  }]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Removes: map[string][]string{"vless-vision": {"bob@example.com"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := strings.Replace(document,
		`      {"email": "alice@example.com", "id": "uuid-alice"},
      {"email": "bob@example.com", "id": "uuid-bob"}, // the middle user
      {"email": "carol@example.com", "id": "uuid-carol"}`,
		`      {"email": "alice@example.com", "id": "uuid-alice"},
      {"email": "carol@example.com", "id": "uuid-carol"}`,
		1)
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

// Detaching the first entry takes its separator; detaching the last takes
// the preceding comma — no dangling or trailing comma is introduced.
func TestRenderPlanDetachesFirstAndLastWithoutDanglingCommas(t *testing.T) {
	document := `{"inbounds": [{"tag": "v", "protocol": "vless",
    "settings": {"clients": [{"email": "a@x.com", "id": "uuid-a"}, {"email": "b@x.com", "id": "uuid-b"}, {"email": "c@x.com", "id": "uuid-c"}]}}]}`
	wantAfter := map[string]string{
		"a@x.com": `{"inbounds": [{"tag": "v", "protocol": "vless",
    "settings": {"clients": [{"email": "b@x.com", "id": "uuid-b"}, {"email": "c@x.com", "id": "uuid-c"}]}}]}`,
		"c@x.com": `{"inbounds": [{"tag": "v", "protocol": "vless",
    "settings": {"clients": [{"email": "a@x.com", "id": "uuid-a"}, {"email": "b@x.com", "id": "uuid-b"}]}}]}`,
	}
	for email, want := range wantAfter {
		rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
			Removes: map[string][]string{"v": {email}},
		})
		if err != nil || !changed {
			t.Fatalf("detach %s: changed = %v, err = %v", email, changed, err)
		}
		if string(rendered) != want {
			t.Errorf("detach %s:\n%s\nwant:\n%s", email, rendered, want)
		}
	}
}

// Detaching the only entry leaves an empty — still parseable — array.
func TestRenderPlanDetachesTheOnlyEntry(t *testing.T) {
	document := `{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [{"email": "a@x.com", "id": "uuid-a"}]}}]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Removes: map[string][]string{"vless-vision": {"a@x.com"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("the emptied array must stay parseable: %v\n%s", err, rendered)
	}
	if len(clients) != 0 {
		t.Errorf("clients = %v, want none", clients)
	}
}

// A removal the array does not carry — a retry after a successful pass —
// is a byte-identical no-op.
func TestRenderPlanRemoveIsIdempotent(t *testing.T) {
	document := `{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [{"email": "a@x.com", "id": "uuid-a"}]}}]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Removes: map[string][]string{"vless-vision": {"nobody@example.com"}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if changed || string(rendered) != document {
		t.Errorf("an absent removal must leave the document untouched, changed = %v", changed)
	}
}

// A Client ID rotation rewrites the id value in place: the entry keeps its
// position and its neighbours keep their bytes (spec §4 — remove+add is the
// live push; the file carries the same result without moving anyone).
func TestRenderPlanRotatesTheClientIDInPlace(t *testing.T) {
	document := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [
      {"email": "alice@example.com", "id": "uuid-alice-old", "flow": "xtls-rprx-vision"},
      {"email": "bob@example.com", "id": "uuid-bob"}
    ]}
  }]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Sets: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "alice@example.com", ID: "uuid-alice-new"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := strings.Replace(document, `"id": "uuid-alice-old"`, `"id": "uuid-alice-new"`, 1)
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

// Setting an id the entry already carries is a no-op; setting one on an
// email the array lost (drift) appends the entry back — the store wins.
func TestRenderPlanSetIsIdempotentAndAppendsTheMissing(t *testing.T) {
	document := `{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [{"email": "a@x.com", "id": "uuid-a"}]}}]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Sets: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "a@x.com", ID: "uuid-a"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if changed || string(rendered) != document {
		t.Errorf("an equal id must leave the document untouched, changed = %v", changed)
	}

	rendered, changed, err = xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Sets: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "b@x.com", ID: "uuid-b", Flow: "xtls-rprx-vision"}},
		},
	})
	if err != nil || !changed {
		t.Fatalf("append the missing: changed = %v, err = %v", changed, err)
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered document does not parse: %v\n%s", err, rendered)
	}
	if _, ok := clients["b@x.com"]; !ok {
		t.Errorf("the set email must be appended back:\n%s", rendered)
	}
}

// One pass composes adds, removals, and sets across inbounds — a detach
// and a rotate in the same save — and the result still parses.
func TestRenderPlanComposesAddRemoveAndSet(t *testing.T) {
	document := `{
  "inbounds": [
    {"tag": "vision", "protocol": "vless",
     "settings": {"clients": [
       {"email": "alice@example.com", "id": "uuid-alice-old"},
       {"email": "bob@example.com", "id": "uuid-bob"}
     ]}},
    {"tag": "ws", "protocol": "vless",
     "settings": {"clients": [{"email": "carol@example.com", "id": "uuid-carol"}]}}
  ]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"ws": {{Email: "alice@example.com", ID: "uuid-alice-new"}},
		},
		Removes: map[string][]string{"vision": {"bob@example.com"}},
		Sets: map[string][]xrayconfig.ClientOp{
			"vision": {{Email: "alice@example.com", ID: "uuid-alice-new"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered document does not parse: %v\n%s", err, rendered)
	}
	if _, ok := clients["bob@example.com"]; ok {
		t.Error("bob was detached and must be gone from the file")
	}
	if _, ok := clients["carol@example.com"]; !ok {
		t.Error("carol keeps her entry")
	}
	if !strings.Contains(string(rendered), `"id": "uuid-alice-new"`) {
		t.Errorf("the rotated id must be in the file:\n%s", rendered)
	}
	if strings.Contains(string(rendered), "uuid-alice-old") {
		t.Errorf("the old id must be gone everywhere:\n%s", rendered)
	}
}

// The managed-set guards apply to every operation kind: a removal or a set
// naming a non-VLESS inbound is a render error; a tag the config does not
// have is skipped (the live push reports the drift).
func TestRenderPlanGuardsTheManagedSet(t *testing.T) {
	document := `{
  "inbounds": [
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": []}},
    {"tag": "vless-vision", "protocol": "vless", "settings": {"clients": []}}
  ]
}`
	if _, _, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Removes: map[string][]string{"trojan": {"a@x.com"}},
	}); err == nil {
		t.Error("a trojan inbound must refuse a managed removal")
	}
	if _, _, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Sets: map[string][]xrayconfig.ClientOp{"trojan": {{Email: "a@x.com", ID: "uuid-a"}}},
	}); err == nil {
		t.Error("a trojan inbound must refuse a managed set")
	}

	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Removes: map[string][]string{"no-such-inbound": {"a@x.com"}},
	})
	if err != nil {
		t.Fatalf("an unknown tag is drift, not a render error: %v", err)
	}
	if changed || string(rendered) != document {
		t.Errorf("unknown tag must leave the document untouched, changed = %v", changed)
	}
}
