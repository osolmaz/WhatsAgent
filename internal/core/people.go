package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// errAddressBook is returned when a session that may not read the owner's address book asks
// about people.
var errAddressBook = fmt.Errorf("%w: only the owner and home read the owner's address book", ErrNotAllowed)

// readsPeople reports whether caller may look up people: the owner and home, which read every
// chat as well.
func readsPeople(caller Caller) bool {
	return caller.owner || caller.Session == homeName
}

// FindContacts returns at most limit people from the owner's address book whose name or number
// contains query. A query that is a phone number also checks whether that number is on WhatsApp.
func (c *Core) FindContacts(ctx context.Context, caller Caller, query string, limit int) ([]model.Contact, error) {
	if !readsPeople(caller) {
		return nil, errAddressBook
	}
	found, err := c.wa.Contacts(ctx, query)
	return found[:min(limit, len(found))], err
}

// ContactInfo returns what WhatsApp shows of a person: about text, business name and profile
// picture.
func (c *Core) ContactInfo(ctx context.Context, caller Caller, ref string) (model.Contact, error) {
	if !readsPeople(caller) {
		return model.Contact{}, errAddressBook
	}
	jids, err := c.people(ctx, []string{ref})
	if err != nil {
		return model.Contact{}, err
	}
	return c.wa.ContactInfo(ctx, jids[0])
}

// SetProfile changes the owner's name, about text or profile picture. The owner's change runs at
// once. An agent's waits for the owner's yes, and its question shows the new picture.
func (c *Core) SetProfile(ctx context.Context, caller Caller, p model.Profile) (string, error) {
	if err := checkProfile(p); err != nil {
		return "", err
	}
	var preview *model.Media
	if !caller.isOwner() {
		var err error
		if preview, err = c.upload(ctx, p.Picture); err != nil {
			return "", err
		}
	}
	return c.administer(ctx, caller, "", proposal{action: describeProfile(p), media: preview,
		act: func(ctx context.Context) (string, error) {
			return "profile changed", c.wa.SetProfile(ctx, p)
		}})
}

func checkProfile(p model.Profile) error {
	if p.Name == "" && p.About == "" && p.Picture == nil {
		return errors.New("no profile change given: set a name, an about text or a picture")
	}
	if p.Picture == nil {
		return nil
	}
	return checkDraft(Draft{File: p.Picture})
}

func describeProfile(p model.Profile) string {
	var parts []string
	if p.Name != "" {
		parts = append(parts, fmt.Sprintf("the name to %q", p.Name))
	}
	if p.About != "" {
		parts = append(parts, fmt.Sprintf("the about text to %q", p.About))
	}
	if p.Picture != nil {
		parts = append(parts, "the picture to this photo")
	}
	return "change your WhatsApp profile: " + strings.Join(parts, ", ")
}
