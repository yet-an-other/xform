package xrayconfig_test

import (
	"strings"
	"testing"

	"github.com/yet-an-other/xform/internal/xrayconfig"
)

// RenderClients rewrites only the managed inbounds' clients arrays
// (user-management spec §4): everything else — comments, key order, unknown
// fields — stays byte-stable, existing clients keep their positions, and new
// clients append at the end.
func TestRenderClientsAppendsAndLeavesEverythingElseUntouched(t *testing.T) {
	document := `{
  // the panel manages this inbound
  "inbounds": [
    {
      "tag": "vless-vision",
      "protocol": "vless",
      "settings": {
        "decryption": "none",
        "clients": [
          {"email": "alice@example.com", "id": "uuid-alice"}
        ],
        "unknownSetting": {"keep": "me"}
      }
    },
    {
      "tag": "vless-xhttp",
      "protocol": "vless",
      "settings": {"clients": [{"email": "carol@example.com", "id": "uuid-carol"}]}
    },
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": [{"email": "dave@example.com", "password": "x"}]}}
  ],
  "routing": {"rules": [{"type": "field", "outboundTag": "direct"}]}
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {
				{Email: "bob@example.com", ID: "uuid-bob", Flow: "xtls-rprx-vision"},
				{Email: "erin@example.com", ID: "uuid-erin"},
			},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	want := strings.Replace(document,
		`{"email": "alice@example.com", "id": "uuid-alice"}`,
		`{"email": "alice@example.com", "id": "uuid-alice"},
          {"email": "bob@example.com", "id": "uuid-bob", "flow": "xtls-rprx-vision"},
          {"email": "erin@example.com", "id": "uuid-erin"}`,
		1)
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

// A client the array already carries is not appended again — retries and
// re-renders are byte-identical no-ops.
func TestRenderClientsSkipsClientsAlreadyThere(t *testing.T) {
	document := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [{"email": "alice@example.com", "id": "uuid-alice-old"}]}
  }]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "alice@example.com", ID: "uuid-alice-new"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if changed {
		t.Errorf("changed = true; the stored email must win and the document stay untouched:\n%s", rendered)
	}
	if string(rendered) != document {
		t.Error("an unchanged render must return the input verbatim")
	}
}

// An inbound with no clients key yet — the ansible template no longer renders
// one — gets the key inserted inside its settings object.
func TestRenderClientsInsertsAMissingClientsKey(t *testing.T) {
	document := `{
  "inbounds": [
    {
      "tag": "vless-vision",
      "protocol": "vless",
      "settings": {
        "decryption": "none"
      }
    }
  ]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "alice@example.com", ID: "uuid-alice", Flow: "xtls-rprx-vision"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := `{
  "inbounds": [
    {
      "tag": "vless-vision",
      "protocol": "vless",
      "settings": {
        "decryption": "none",
        "clients": [
          {"email": "alice@example.com", "id": "uuid-alice", "flow": "xtls-rprx-vision"}
        ]
      }
    }
  ]
}`
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

func TestRenderClientsFillsAnEmptyArray(t *testing.T) {
	document := `{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless", "settings": {"clients": []}}]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "alice@example.com", ID: "uuid-alice"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := `{
  "inbounds": [{"tag": "vless-vision", "protocol": "vless", "settings": {"clients": [{"email": "alice@example.com", "id": "uuid-alice"}]}}]
}`
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

// Comments inside a managed array survive an append — only a rewrite may
// drop them, and appends are not rewrites.
func TestRenderClientsKeepsCommentsAndToleratesTrailingCommas(t *testing.T) {
	document := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [
      {"email": "alice@example.com", "id": "uuid-alice"}, // the first user
    ]}
  }]
}`
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"vless-vision": {{Email: "bob@example.com", ID: "uuid-bob"}},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	want := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [
      {"email": "alice@example.com", "id": "uuid-alice"}, // the first user
      {"email": "bob@example.com", "id": "uuid-bob"}
    ]}
  }]
}`
	if string(rendered) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", rendered, want)
	}
}

// The tag decides the managed inbound: a tag naming a non-VLESS inbound is a
// render error (the mutation API validated VLESS; anything else is drift),
// and a tag the config does not have is skipped — the live push reports it.
func TestRenderClientsGuardsTheManagedSet(t *testing.T) {
	document := `{
  "inbounds": [
    {"tag": "trojan", "protocol": "trojan", "settings": {"clients": []}},
    {"tag": "vless-vision", "protocol": "vless", "settings": {"clients": []}}
  ]
}`
	_, _, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"trojan": {{Email: "alice@example.com", ID: "uuid-alice"}},
		},
	})
	if err == nil {
		t.Error("a trojan inbound must refuse a managed client")
	}

	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{
		Adds: map[string][]xrayconfig.ClientOp{
			"no-such-inbound": {{Email: "alice@example.com", ID: "uuid-alice"}},
		},
	})
	if err != nil {
		t.Fatalf("an unknown tag is drift, not a render error: %v", err)
	}
	if changed || string(rendered) != document {
		t.Errorf("unknown tag must leave the document untouched, changed = %v", changed)
	}
}

// The rendered document stays parseable by the panel's own strict parser —
// the watcher must keep reading what the renderer writes.
func TestRenderClientsOutputStaysParseable(t *testing.T) {
	document := `{
  "inbounds": [{
    "tag": "vless-vision", "protocol": "vless",
    "settings": {"clients": [
      {"email": "alice@example.com", "id": "dc9c2e62-df06-4b85-b13d-f3bbce7d3b8e"}
    ]}
  }]
}`
	adds := map[string][]xrayconfig.ClientOp{
		"vless-vision": {{Email: "bo\"b@example.com", ID: "0f28c9a2-5d0f-4e51-9b1e-5d07d0a1bbcc", Flow: "xtls-rprx-vision"}},
	}
	rendered, changed, err := xrayconfig.RenderClients([]byte(document), xrayconfig.RenderPlan{Adds: adds})
	if err != nil || !changed {
		t.Fatalf("render: changed = %v, err = %v", changed, err)
	}
	clients, err := xrayconfig.Parse(rendered)
	if err != nil {
		t.Fatalf("rendered document does not parse: %v\n%s", err, rendered)
	}
	if _, ok := clients[`bo"b@example.com`]; !ok {
		t.Errorf("the added email — escapes and all — must survive: %v", clients)
	}
	if _, ok := clients["alice@example.com"]; !ok {
		t.Errorf("the existing client must survive: %v", clients)
	}
}
