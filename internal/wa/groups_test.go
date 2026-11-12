package wa

import (
	"bytes"
	"context"
	"image/jpeg"
	"slices"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waVnameCert"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func TestGroupsWithoutConnection(t *testing.T) {
	ctx := context.Background()
	c := linked(t)
	if _, err := c.GroupInfo(ctx, "1@g.us"); err == nil {
		t.Error("GroupInfo without a connection gave no error")
	}
	if _, err := c.CreateGroup(ctx, "Trip", []string{"2@s.whatsapp.net"}); err == nil {
		t.Error("CreateGroup without a connection gave no error")
	}
	if _, err := c.JoinGroup(ctx, "https://chat.whatsapp.com/AbC"); err == nil {
		t.Error("JoinGroup without a connection gave no error")
	}
	if _, err := c.Groups(ctx); err == nil {
		t.Error("Groups without a connection gave no error")
	}
	// The connect resync gives up quietly without a connection.
	r := &recorder{names: map[string]string{}}
	c.loadGroupNames(ctx, r)
	if len(r.names) != 0 {
		t.Errorf("names = %v", r.names)
	}
	// Every action reaches WhatsApp and fails without a connection.
	for action, needs := range model.GroupActions {
		change := model.GroupChange{Action: action, Text: "x"}
		if needs == "members" {
			change.Members = []string{"2@s.whatsapp.net"}
		}
		if _, err := c.ChangeGroup(ctx, "1@g.us", change); err == nil {
			t.Errorf("%s without a connection gave no error", action)
		}
	}
	for name, err := range map[string]error{
		"an unknown action": first(c.ChangeGroup(ctx, "1@g.us", model.GroupChange{Action: "explode"})),
		"a bad group":       first(c.ChangeGroup(ctx, "bad", model.GroupChange{Action: "leave"})),
		"a bad member":      first(c.ChangeGroup(ctx, "1@g.us", model.GroupChange{Action: "add", Members: []string{"bad"}})),
		"a bad new member":  first(c.CreateGroup(ctx, "Trip", []string{"bad"})),
		"a bad group info":  firstGroup(c.GroupInfo(ctx, "bad")),
	} {
		if err == nil {
			t.Errorf("%s gave no error", name)
		}
	}
}

func first(_ string, err error) error { return err }

func firstGroup(_ model.Group, err error) error { return err }

func TestGroupDetails(t *testing.T) {
	c := linked(t)
	info := &types.GroupInfo{JID: *mustJID(t, "1@g.us"), GroupName: types.GroupName{Name: "Team"},
		GroupTopic: types.GroupTopic{Topic: "plans"}, GroupAnnounce: types.GroupAnnounce{IsAnnounce: true},
		Participants: []types.GroupParticipant{
			{JID: *mustJID(t, "777@lid"), PhoneNumber: *mustJID(t, "4915550000@s.whatsapp.net"), IsAdmin: true},
			{JID: *mustJID(t, "888@s.whatsapp.net")},
		}}
	g := c.group(context.Background(), info)
	if g.Name != "Team" || g.Description != "plans" || !g.Announce || len(g.Members) != 2 ||
		g.Members[0].JID != "4915550000@s.whatsapp.net" || !g.Members[0].Admin || g.Members[1].Admin {
		t.Errorf("group = %+v", g)
	}
	result := memberResult("add", []types.GroupParticipant{{JID: *mustJID(t, "1@s.whatsapp.net")},
		{JID: *mustJID(t, "2@s.whatsapp.net"), Error: 403}})
	if result != "add: 1 of 2; refused 2 (error 403)" {
		t.Errorf("memberResult = %q", result)
	}
}

func TestPeopleWithoutConnection(t *testing.T) {
	ctx := context.Background()
	c := linked(t)
	kim := *mustJID(t, "4915550000@s.whatsapp.net")
	if err := c.cli.Store.Contacts.PutContactName(ctx, kim, "Kim", "Kim Park"); err != nil {
		t.Fatal(err)
	}
	// The address book is local, so a search by name works offline.
	if found, err := c.Contacts(ctx, "park"); err != nil || len(found) != 1 || found[0].Name != "Kim Park" {
		t.Errorf("Contacts = %+v, %v", found, err)
	}
	if found, _ := c.Contacts(ctx, "nobody"); len(found) != 0 {
		t.Errorf("Contacts of nobody = %+v", found)
	}
	// A phone number is also checked on WhatsApp, which needs the connection.
	if found, err := c.Contacts(ctx, "+49 1555 0000"); err == nil || len(found) != 1 {
		t.Errorf("Contacts of a number = %+v, %v", found, err)
	}
	if _, err := c.ContactInfo(ctx, "4915550000@s.whatsapp.net"); err == nil {
		t.Error("ContactInfo without a connection gave no error")
	}
	if _, err := c.ContactInfo(ctx, "bad"); err == nil {
		t.Error("ContactInfo of a bad address gave no error")
	}
	for _, p := range []model.Profile{{Name: "Kan"}, {About: "busy"}, {Picture: &model.File{Data: pngFile(t, 10, 10)}},
		{Picture: &model.File{Data: []byte("not a photo")}}} {
		if err := c.SetProfile(ctx, p); err == nil {
			t.Errorf("SetProfile(%+v) without a connection gave no error", p)
		}
	}
	if err := c.SetProfile(ctx, model.Profile{}); err != nil {
		t.Errorf("an empty profile change = %v", err)
	}
}

func TestProfilePicture(t *testing.T) {
	wide, err := profilePicture(pngFile(t, 1000, 700))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(wide))
	if err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 640 {
		t.Fatalf("picture = %v, %v", img.Bounds(), err)
	}
	small, _ := profilePicture(pngFile(t, 30, 50))
	if img, _ := jpeg.Decode(bytes.NewReader(small)); img.Bounds().Dx() != 30 || img.Bounds().Dy() != 30 {
		t.Errorf("small picture = %v", img.Bounds())
	}
	if _, err := profilePicture([]byte("not a photo")); err == nil || !strings.Contains(err.Error(), "JPEG or PNG") {
		t.Errorf("profilePicture of text = %v", err)
	}
	contact := model.Contact{JID: "4915550000@s.whatsapp.net", Name: "Kim"}
	for q, want := range map[string]bool{"kim": true, "4915": true, "+49 15": true, "sam": false} {
		if matches(contact, q) != want {
			t.Errorf("matches(%q) = %v", q, !want)
		}
	}
	if slices.Contains(model.PollOptions("x"), "x") {
		t.Error("PollOptions read a question as an option")
	}
	jid := *mustJID(t, "4915550000@s.whatsapp.net")
	business := &types.VerifiedName{Details: &waVnameCert.VerifiedNameCertificate_Details{VerifiedName: proto.String("Kim's Shop")}}
	got := userContact(jid, "Kim", types.UserInfo{Status: "open", VerifiedName: business}, &types.ProfilePictureInfo{URL: "https://pps/x"})
	if got.About != "open" || got.Business != "Kim's Shop" || got.Picture != "https://pps/x" || got.Name != "Kim" {
		t.Errorf("userContact = %+v", got)
	}
	if hidden := userContact(jid, "Kim", types.UserInfo{}, nil); hidden.Picture != "" || hidden.Business != "" {
		t.Errorf("userContact without details = %+v", hidden)
	}
}
