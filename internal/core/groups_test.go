package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func TestGroups(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}

	if g, err := h.core.GroupInfo(ctx, bot, "Team"); err != nil || g.Name != "Team" {
		t.Errorf("GroupInfo = %+v, %v", g, err)
	}
	if _, err := h.core.GroupInfo(ctx, Caller{Session: "other"}, "Team"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("GroupInfo for a session without the chat = %v", err)
	}
	if _, err := h.core.GroupInfo(ctx, Owner, "Sam"); err == nil {
		t.Error("GroupInfo of a direct chat gave no error")
	}

	// The owner's change runs at once, with the people resolved to JIDs.
	if out, err := h.core.ChangeGroup(ctx, Owner, "Team", model.GroupChange{Action: "add", Members: []string{"+1 555 000 1111", "Sam"}}); err != nil || out != "add done" {
		t.Fatalf("ChangeGroup = %q, %v", out, err)
	}
	if added := h.expectKind("add"); added.text != "15550001111@s.whatsapp.net,sam@s.whatsapp.net" {
		t.Errorf("added %q", added.text)
	}

	// An agent's change waits for a yes, even in a chat where it may answer at once.
	h.converse("bot", "g@g.us", model.RoleOwner)
	if out, _ := h.core.ChangeGroup(ctx, bot, "Team", model.GroupChange{Action: "rename", Text: "Crew"}); !strings.Contains(out, "waiting for the owner's yes") {
		t.Fatalf("agent ChangeGroup = %q", out)
	}
	code := h.expectCode(`change the group "Team" (group): rename it to "Crew"`)
	h.receive("g@g.us", "yes "+code, true)
	h.expectKind("rename")
	_, _ = h.core.ChangeGroup(ctx, bot, "Team", model.GroupChange{Action: "announce", On: true})
	h.expectCode("turn announce on")
	_, _ = h.core.ChangeGroup(ctx, bot, "Team", model.GroupChange{Action: "invite_link"})
	h.expectCode(`change the group "Team" (group): invite link`)

	for name, change := range map[string]model.GroupChange{
		"an unknown action": {Action: "explode"},
		"a rename to empty": {Action: "rename", Text: " "},
		"a long name":       {Action: "rename", Text: strings.Repeat("x", 26)},
		"no members":        {Action: "remove"},
		"a group as member": {Action: "add", Members: []string{"g@g.us"}},
		"an unknown member": {Action: "add", Members: []string{"Nobody"}},
	} {
		if _, err := h.core.ChangeGroup(ctx, Owner, "Team", change); err == nil {
			t.Errorf("%s gave no error", name)
		}
	}
	if _, err := h.core.ChangeGroup(ctx, Owner, "Sam", model.GroupChange{Action: "leave"}); err == nil {
		t.Error("a change to a direct chat gave no error")
	}
	if err := h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "*", Permission: model.Deny}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.core.ChangeGroup(ctx, bot, "Team", model.GroupChange{Action: "leave"}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a change that a rule denies = %v", err)
	}

	// Creating and joining: the owner at once, an agent after a yes.
	if out, err := h.core.CreateGroup(ctx, Owner, "Trip", []string{"Sam"}); err != nil || out != "created new@g.us" {
		t.Fatalf("CreateGroup = %q, %v", out, err)
	}
	if created := h.expectKind("create"); created.text != "Trip sam@s.whatsapp.net" {
		t.Errorf("created %q", created.text)
	}
	if chat, _ := h.st.Chat(ctx, "new@g.us"); chat.Name != "Trip" {
		t.Errorf("the new group's name = %q", chat.Name)
	}
	_, _ = h.core.CreateGroup(ctx, Caller{Session: homeName}, "Party", []string{"Sam"})
	h.expectCode(`create the group "Party" with sam@s.whatsapp.net`)
	for name, err := range map[string]error{
		"no name":     first(h.core.CreateGroup(ctx, Owner, "", []string{"Sam"})),
		"a long name": first(h.core.CreateGroup(ctx, Owner, strings.Repeat("x", 26), []string{"Sam"})),
		"no members":  first(h.core.CreateGroup(ctx, Owner, "Trip", nil)),
		"no link":     first(h.core.JoinGroup(ctx, Owner, " ")),
	} {
		if err == nil {
			t.Errorf("%s gave no error", name)
		}
	}
	if out, err := h.core.JoinGroup(ctx, Owner, "https://chat.whatsapp.com/AbC"); err != nil || out != "joined joined@g.us" {
		t.Errorf("JoinGroup = %q, %v", out, err)
	}
	_, _ = h.core.JoinGroup(ctx, Caller{Session: homeName}, "https://chat.whatsapp.com/XyZ")
	h.expectCode("join the group of this invite link: https://chat.whatsapp.com/XyZ")
}

func TestPeopleAndProfile(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	home := Caller{Session: homeName}
	bot := Caller{Session: "bot"}

	if found, err := h.core.FindContacts(ctx, home, "Kim", 1); err != nil || len(found) != 1 || found[0].Name != "Kim 1" {
		t.Errorf("FindContacts = %+v, %v", found, err)
	}
	if info, err := h.core.ContactInfo(ctx, Owner, "Sam"); err != nil || info.About != "busy" || info.JID != "sam@s.whatsapp.net" {
		t.Errorf("ContactInfo = %+v, %v", info, err)
	}
	for name, err := range map[string]error{
		"FindContacts for a chat agent": first2(h.core.FindContacts(ctx, bot, "Kim", 5)),
		"ContactInfo for a chat agent":  first3(h.core.ContactInfo(ctx, bot, "Sam")),
	} {
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if _, err := h.core.ContactInfo(ctx, Owner, "g@g.us"); err == nil {
		t.Error("ContactInfo of a group gave no error")
	}

	// The owner changes the profile at once; an agent's change shows the new picture first.
	if out, err := h.core.SetProfile(ctx, Owner, model.Profile{Name: "Kan", About: "busy"}); err != nil || out != "profile changed" {
		t.Fatalf("SetProfile = %q, %v", out, err)
	}
	if changed := h.expectKind("profile"); changed.text != "Kan|busy" {
		t.Errorf("profile %q", changed.text)
	}
	picture := &model.File{Name: "me.png", Data: []byte("png")}
	_, _ = h.core.SetProfile(ctx, home, model.Profile{About: "away", Picture: picture})
	asked := h.expectFile("me.png")
	if !strings.Contains(asked.text, `change your WhatsApp profile: the about text to "away", the picture to this photo`) {
		t.Errorf("asked %q", asked.text)
	}
	for name, p := range map[string]model.Profile{
		"no change":      {},
		"an empty photo": {Picture: &model.File{Name: "e.png"}},
	} {
		if _, err := h.core.SetProfile(ctx, Owner, p); err == nil {
			t.Errorf("%s gave no error", name)
		}
	}
	h.wa.mu.Lock()
	h.wa.failUpload = errors.New("offline")
	h.wa.mu.Unlock()
	if _, err := h.core.SetProfile(ctx, home, model.Profile{Picture: picture}); err == nil {
		t.Error("a picture that did not upload gave no error")
	}
}

func first2(_ []model.Contact, err error) error { return err }

func first3(_ model.Contact, err error) error { return err }
