package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
)

// ErrAlreadyLinked is returned when Link runs on a store that already holds a linked device.
var ErrAlreadyLinked = errors.New("this session store is already linked")

// LinkOptions chooses between QR pairing and phone-number pairing.
type LinkOptions struct {
	// Phone, when set, pairs with a phone-number code instead of a QR code. It is the
	// account's number in international format.
	Phone string
	// OnQR receives each new QR code payload during QR pairing.
	OnQR func(code string)
	// OnPairCode receives the code to type on the phone during phone-number pairing.
	OnPairCode func(code string)
}

// pairFunc asks WhatsApp for a phone-number pairing code.
type pairFunc func(ctx context.Context, phone string) (string, error)

// Link pairs the session store with a WhatsApp account and returns when pairing succeeds.
func (c *Client) Link(ctx context.Context, opts LinkOptions) error {
	if c.Linked() {
		return ErrAlreadyLinked
	}
	items, err := c.cli.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("start pairing: %w", err)
	}
	if err := c.cli.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	pair := func(ctx context.Context, phone string) (string, error) {
		return c.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "WhatsAgent")
	}
	return waitForPairing(ctx, items, opts, pair)
}

// waitForPairing reads pairing events until pairing succeeds, fails, or ctx ends.
func waitForPairing(ctx context.Context, items <-chan whatsmeow.QRChannelItem, opts LinkOptions, pair pairFunc) error {
	p := pairing{opts: opts, pair: pair}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-items:
			if !ok {
				return errors.New("pairing ended without success")
			}
			if done, err := p.handle(ctx, item); done || err != nil {
				return err
			}
		}
	}
}

// pairing holds the state of one pairing attempt.
type pairing struct {
	opts          LinkOptions
	pair          pairFunc
	codeRequested bool
}

// handle processes one pairing event and reports whether pairing finished.
func (p *pairing) handle(ctx context.Context, item whatsmeow.QRChannelItem) (bool, error) {
	switch item.Event {
	case whatsmeow.QRChannelSuccess.Event:
		return true, nil
	case whatsmeow.QRChannelEventCode:
		return false, p.onCode(ctx, item.Code)
	default:
		return true, pairingError(item)
	}
}

// onCode shows a new QR code, or requests the phone pairing code once when Phone is set.
// WhatsApp needs the QR flow running before it hands out a phone pairing code.
func (p *pairing) onCode(ctx context.Context, code string) error {
	if p.opts.Phone == "" {
		if p.opts.OnQR != nil {
			p.opts.OnQR(code)
		}
		return nil
	}
	if p.codeRequested {
		return nil
	}
	pairCode, err := p.pair(ctx, p.opts.Phone)
	if err != nil {
		return fmt.Errorf("request pairing code: %w", err)
	}
	p.codeRequested = true
	if p.opts.OnPairCode != nil {
		p.opts.OnPairCode(pairCode)
	}
	return nil
}

// pairingErrors maps whatsmeow's pairing failure events to messages for the owner.
var pairingErrors = map[string]string{
	whatsmeow.QRChannelTimeout.Event:                   "no phone scanned a QR code in time; run `whatsagent link` to start again",
	whatsmeow.QRChannelClientOutdated.Event:            "WhatsApp rejected this client version; update WhatsAgent and link again",
	whatsmeow.QRChannelScannedWithoutMultidevice.Event: "the phone that scanned the code has multi-device turned off",
	whatsmeow.QRChannelErrUnexpectedEvent.Event:        "WhatsApp sent a pairing event out of order; run `whatsagent link` again",
	whatsmeow.QRChannelEventPasskeyRequest:             "WhatsApp wants a passkey check during pairing, which WhatsAgent does not support yet",
	whatsmeow.QRChannelEventPasskeyResponse:            "WhatsApp wants a passkey confirmation during pairing, which WhatsAgent does not support yet",
}

// pairingError turns a failed pairing event into an error.
func pairingError(item whatsmeow.QRChannelItem) error {
	if item.Event == whatsmeow.QRChannelEventError && item.Error != nil {
		return fmt.Errorf("pairing failed: %w", item.Error)
	}
	if msg, ok := pairingErrors[item.Event]; ok {
		return errors.New(msg)
	}
	return fmt.Errorf("pairing failed with event %q", item.Event)
}
