package wa

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// phoneQuery matches a query that is a phone number in international form.
var phoneQuery = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{5,}$`)

// Contacts returns the people in the owner's address book whose name or number contains query.
// A query that is a phone number also gets a check of whether that number is on WhatsApp.
func (c *Client) Contacts(ctx context.Context, query string) ([]model.Contact, error) {
	all, err := c.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var found []model.Contact
	for jid, info := range all {
		contact := model.Contact{JID: jid.String(), Name: cmp.Or(info.FullName, info.FirstName), PushName: info.PushName,
			Business: info.BusinessName}
		if matches(contact, q) {
			found = append(found, contact)
		}
	}
	slices.SortFunc(found, func(a, b model.Contact) int { return cmp.Compare(a.Name+a.PushName, b.Name+b.PushName) })
	if phoneQuery.MatchString(q) {
		checked, err := c.checkNumber(ctx, q)
		if err != nil {
			return found, err
		}
		found = append([]model.Contact{checked}, found...)
	}
	return found, nil
}

// matches reports whether a contact's name or address contains q, or its number contains the
// digits of q, so that a number matches however it is spaced.
func matches(contact model.Contact, q string) bool {
	text := strings.ToLower(strings.Join([]string{contact.JID, contact.Name, contact.PushName, contact.Business}, " "))
	digits := model.Digits(q)
	return strings.Contains(text, q) || (digits != "" && strings.Contains(contact.JID, digits))
}

// checkNumber asks WhatsApp whether a phone number has an account.
func (c *Client) checkNumber(ctx context.Context, number string) (model.Contact, error) {
	result, err := c.cli.IsOnWhatsApp(ctx, []string{"+" + model.Digits(number)})
	if err != nil || len(result) == 0 {
		return model.Contact{}, err
	}
	r := result[0]
	contact := model.Contact{JID: cmp.Or(r.JID, r.PhoneNumber).String(), OnWhatsApp: &r.IsIn}
	if r.VerifiedName != nil {
		contact.Business = r.VerifiedName.Details.GetVerifiedName()
	}
	return contact, nil
}

// ContactInfo returns what WhatsApp shows of a person: name, about text, business name and the
// address of the profile picture, as far as the person's privacy settings allow.
func (c *Client) ContactInfo(ctx context.Context, person string) (model.Contact, error) {
	jid, err := parseChat(person)
	if err != nil {
		return model.Contact{}, err
	}
	users, err := c.cli.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return model.Contact{}, err
	}
	picture, _ := c.cli.GetProfilePictureInfo(ctx, jid, nil)
	return userContact(jid, c.name(ctx, person), users[jid], picture), nil
}

// userContact puts what WhatsApp tells about a user into a contact. picture is nil when the
// person's privacy settings hide the picture.
func userContact(jid types.JID, name string, u types.UserInfo, picture *types.ProfilePictureInfo) model.Contact {
	contact := model.Contact{JID: jid.String(), Name: name, About: u.Status}
	if u.VerifiedName != nil {
		contact.Business = u.VerifiedName.Details.GetVerifiedName()
	}
	if picture != nil {
		contact.Picture = picture.URL
	}
	return contact
}

// SetProfile changes the owner's name, about text and profile picture, each when it is set. The
// picture becomes a square JPEG of at most 640 pixels, as WhatsApp keeps them.
func (c *Client) SetProfile(ctx context.Context, p model.Profile) error {
	steps := []struct {
		set bool
		run func() error
	}{
		{p.Name != "", func() error { return c.cli.SendAppState(ctx, appstate.BuildSettingPushName(p.Name)) }},
		{p.About != "", func() error { return c.cli.SetStatusMessage(ctx, types.SetStatusInput{Text: &p.About}) }},
		{p.Picture != nil, func() error {
			picture, err := profilePicture(p.Picture.Data)
			if err != nil {
				return err
			}
			_, err = c.cli.SetGroupPhoto(ctx, types.EmptyJID, picture)
			return err
		}},
	}
	for _, step := range steps {
		if !step.set {
			continue
		}
		if err := step.run(); err != nil {
			return err
		}
	}
	return nil
}
