package today

import "testing"

func TestRealmBase(t *testing.T) {
	cases := []struct{ realm, app, api, auth string }{
		{"global", GlobalAppBase, GlobalAPIBase, GlobalAuthBase},
		{"", GlobalAppBase, GlobalAPIBase, GlobalAuthBase},
		{"GLOBAL", GlobalAppBase, GlobalAPIBase, GlobalAuthBase},
		{"cn", CNAppBase, CNAPIBase, CNAuthBase},
		{"CN", CNAppBase, CNAPIBase, CNAuthBase},
	}
	for _, c := range cases {
		app, api, auth := RealmBase(c.realm)
		if app != c.app || api != c.api || auth != c.auth {
			t.Fatalf("RealmBase(%q) = %q/%q/%q", c.realm, app, api, auth)
		}
	}
}

func TestNewClientWithBaseOverrides(t *testing.T) {
	cred := Credential{Realm: "cn"}
	c := NewClientWithBase(cred, "http://mock:1/", "http://mock:2/")
	if c.AppBase != "http://mock:1" {
		t.Fatalf("AppBase = %q (trailing slash must be trimmed)", c.AppBase)
	}
	if c.APIBase != "http://mock:2" {
		t.Fatalf("APIBase = %q", c.APIBase)
	}
	// AuthBase 不从覆盖项推导，仍按 realm 走。
	if c.AuthBase != CNAuthBase {
		t.Fatalf("AuthBase = %q", c.AuthBase)
	}
}

func TestNewClientWithBaseEmptyKeepsRealmDefault(t *testing.T) {
	c := NewClientWithBase(Credential{Realm: "global"}, "", "")
	if c.AppBase != GlobalAppBase || c.APIBase != GlobalAPIBase {
		t.Fatalf("got %q/%q", c.AppBase, c.APIBase)
	}
}

func TestCookieHeaderPrefersRaw(t *testing.T) {
	c := NewClient(Credential{CookieRaw: "a=1; b=2", Session: "ignored"})
	if got := c.cookieHeader(); got != "a=1; b=2" {
		t.Fatalf("got %q", got)
	}
	c2 := NewClient(Credential{Session: "tok"})
	if got := c2.cookieHeader(); got != "__Secure-better-auth.session_token=tok" {
		t.Fatalf("got %q", got)
	}
	c3 := NewClient(Credential{})
	if got := c3.cookieHeader(); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestConversationMessageText(t *testing.T) {
	var m ConversationMessage
	m.Body.Parts = append(m.Body.Parts, struct {
		MessageType string `json:"messageType"`
		Content     struct {
			Text string `json:"text"`
		} `json:"content"`
	}{MessageType: "text", Content: struct {
		Text string `json:"text"`
	}{Text: "正文"}})
	if got := m.Text(); got != "正文" {
		t.Fatalf("got %q", got)
	}
}

func TestModelCacheLifecycle(t *testing.T) {
	c := NewClient(Credential{})
	if c.ModelCheckedRecently() {
		t.Fatal("fresh client must not report a cached model")
	}
	c.MarkModelChecked("model.glm-5.3")
	if !c.ModelCheckedRecently() {
		t.Fatal("model should be cached after MarkModelChecked")
	}
	if c.ModelOptionID() != "model.glm-5.3" {
		t.Fatalf("ModelOptionID = %q", c.ModelOptionID())
	}
}

func TestRandomIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := RandomID()
		if len(id) != 24 {
			t.Fatalf("RandomID length = %d, want 24", len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate RandomID: %s", id)
		}
		seen[id] = true
	}
}
