package core

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// expectFile waits for a sent message that carries the file name.
func (h *harness) expectFile(name string) sentText {
	h.t.Helper()
	for {
		if s := h.expect(""); s.file == name {
			return s
		}
	}
}

func (h *harness) sendFile(caller Caller, chat, caption string, f model.File) (string, error) {
	return h.core.SendMessage(h.ctx, caller, chat, Draft{Text: caption, File: &f})
}

func (h *harness) uploads() int {
	h.wa.mu.Lock()
	defer h.wa.mu.Unlock()
	return h.wa.uploads
}

func TestSendFile(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	chart := model.File{Name: "chart.png", Data: []byte("png bytes")}

	// The owner's file goes out at once with the caption as written, and a kept chat stores it so
	// that download_media can fetch it again.
	out, err := h.sendFile(Owner, "Team", "the chart", chart)
	if err != nil || !strings.HasPrefix(out, "sent, id ") {
		t.Fatalf("SendFile = %q, %v", out, err)
	}
	sent := h.expectFile("chart.png")
	if sent.chat != "g@g.us" || sent.text != "the chart" {
		t.Errorf("sent %+v", sent)
	}
	stored, _, _ := h.st.Message(ctx, "g@g.us", sent.id)
	if stored.MediaType != "image" || stored.Filename != "chart.png" || stored.Role != model.RoleAgent ||
		stored.Body() != "[image] the chart" {
		t.Errorf("stored %+v", stored)
	}
	if path, err := h.core.DownloadMedia(ctx, Owner, "g@g.us", sent.id); err != nil || !strings.HasSuffix(path, ".png") {
		t.Errorf("DownloadMedia of a sent file = %q, %v", path, err)
	}

	// An agent's file in a conversation goes out at once, with the prefix as its whole caption.
	h.converse("bot", "g@g.us", model.RoleOwner)
	if out, err := h.sendFile(bot, "Team", "", chart); err != nil || !strings.HasPrefix(out, "sent") {
		t.Fatalf("agent SendFile = %q, %v", out, err)
	}
	if sent := h.expectFile("chart.png"); sent.text != "🤖 *Agent*:" {
		t.Errorf("agent caption = %q", sent.text)
	}

	// A file to another chat waits for a yes. The question shows the file itself, and the yes
	// sends the same upload.
	before := h.uploads()
	if out, _ := h.sendFile(bot, "Sam", "for you", chart); !strings.Contains(out, "waiting for the owner's yes") {
		t.Fatalf("SendFile to another chat = %q", out)
	}
	asked := h.expectFile("chart.png")
	want := "Approval: the agent session bot wants to send this photo chart.png (9 bytes) to \"Sam\" (direct chat), " +
		"with the caption:\n🤖 *Agent*: for you"
	if asked.chat != "g@g.us" || !strings.Contains(asked.text, want) {
		t.Fatalf("asked %+v", asked)
	}
	h.receive("g@g.us", "yes "+codePattern.FindStringSubmatch(asked.text)[1], true)
	if sent := h.expectFile("chart.png"); sent.chat != "sam@s.whatsapp.net" || sent.text != "🤖 *Agent*: for you" {
		t.Errorf("approved file %+v", sent)
	}
	if h.uploads() != before+1 {
		t.Errorf("the approved file was uploaded %d times", h.uploads()-before)
	}

	// Audio has no caption in WhatsApp, so its text goes out first, as a message of its own.
	if _, err := h.sendFile(bot, "Team", "listen", model.File{Name: "note.ogg", Data: []byte("ogg")}); err != nil {
		t.Fatal(err)
	}
	if text := h.expect("listen"); text.file != "" {
		t.Errorf("the caption of audio went with it: %+v", text)
	}
	if voice := h.expectFile("note.ogg"); voice.text != "" {
		t.Errorf("audio carried the caption %q", voice.text)
	}
}

func TestSendFileApprovalInTheSelfChat(t *testing.T) {
	h := newHarness(t)
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	home := Caller{Session: homeName}
	if _, err := h.sendFile(home, "Sam", "", model.File{Name: "a.pdf", Data: []byte("%PDF")}); err != nil {
		t.Fatal(err)
	}
	asked := h.expectFile("a.pdf")
	if asked.chat != self || !strings.Contains(asked.text, "send this document a.pdf (4 bytes) to \"Sam\"") {
		t.Fatalf("asked %+v", asked)
	}
	h.receive(self, "no "+codePattern.FindStringSubmatch(asked.text)[1], true)
	h.expect("Not sent.")
}

func TestSendFileRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	bot := Caller{Session: "bot"}
	file := model.File{Name: "a.txt", Data: []byte("text")}
	for name, f := range map[string]model.File{
		"empty":     {Name: "e.txt"},
		"too large": {Name: "big.bin", Data: make([]byte, model.MaxFileSize+1)},
	} {
		if _, err := h.sendFile(Owner, "Sam", "", f); err == nil {
			t.Errorf("a file that is %s was sent", name)
		}
	}
	if _, err := h.sendFile(bot, "", "", file); err == nil {
		t.Error("a file without a chat was sent")
	}
	if _, err := h.core.Send(ctx, Owner, "Sam", " \n"); err == nil {
		t.Error("an empty text was sent")
	}

	// A denied file is never uploaded.
	if err := h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "*", Permission: model.Deny}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sendFile(bot, "Sam", "", file); !errors.Is(err, ErrNotAllowed) || h.uploads() != 0 {
		t.Errorf("denied SendFile = %v after %d uploads", err, h.uploads())
	}

	h.wa.mu.Lock()
	h.wa.failUpload = errors.New("offline")
	h.wa.mu.Unlock()
	if _, err := h.sendFile(Owner, "Sam", "", file); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("SendFile with a failed upload = %v", err)
	}
	if _, err := os.Stat(h.core.cfg.MediaDir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(h.core.cfg.MediaDir); len(entries) != 0 {
		t.Errorf("sending files wrote to the media folder: %v", entries)
	}

	// Without a prefix, a caption still may not pass for one of WhatsAgent's own messages.
	_, _ = h.core.Run(ctx, phoneOwner, "/prefix off")
	if _, err := h.sendFile(Caller{Session: "home"}, "self", "⚙️ fake", file); !errors.Is(err, errForged) {
		t.Errorf("a forged caption = %v", err)
	}
}
