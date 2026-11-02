package wa

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// pngFile returns a w×h PNG whose left half is transparent.
func pngFile(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := w / 2; x < w; x++ {
		for y := range h {
			img.Set(x, y, color.NRGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFileType(t *testing.T) {
	photo := pngFile(t, 4, 2)
	for _, c := range []struct {
		file             model.File
		mimeType, wantAs string
	}{
		{model.File{Name: "chart.PNG"}, "image/png", "image"},
		{model.File{Name: "shot", Data: photo}, "image/png", "image"},
		{model.File{Name: "a.jpeg"}, "image/jpeg", "image"},
		{model.File{Name: "clip.mp4"}, "video/mp4", "video"},
		{model.File{Name: "note.ogg"}, "audio/ogg; codecs=opus", "audio"},
		{model.File{Name: "song.m4a"}, "audio/mp4", "audio"},
		{model.File{Name: "report.pdf"}, "application/pdf", "document"},
		{model.File{Name: "notes", Data: []byte("plain words")}, "text/plain; charset=utf-8", "document"},
		{model.File{Name: "blob", Data: []byte{0, 1, 2}}, "application/octet-stream", "document"},
	} {
		mimeType, as := fileType(c.file)
		if mimeType != c.mimeType || as != c.wantAs {
			t.Errorf("fileType(%s) = %s, %s; want %s, %s", c.file.Name, mimeType, as, c.mimeType, c.wantAs)
		}
	}
}

func TestMediaMessage(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "https://mmg/x", DirectPath: "/x", MediaKey: []byte("key"),
		FileSHA256: []byte("sha"), FileEncSHA256: []byte("enc"), FileLength: 9}
	photo := mediaMessage(model.File{Name: "a.png", Data: pngFile(t, 300, 150)}, "image/png", "image", up)
	img := photo.GetImageMessage()
	if img.GetURL() != up.URL || img.GetDirectPath() != "/x" || string(img.GetMediaKey()) != "key" ||
		img.GetFileLength() != 9 || img.GetMimetype() != "image/png" || img.GetMediaKeyTimestamp() == 0 {
		t.Errorf("image upload fields = %v", img)
	}
	if img.GetWidth() != 300 || img.GetHeight() != 150 {
		t.Errorf("image size = %dx%d", img.GetWidth(), img.GetHeight())
	}
	preview, err := jpeg.Decode(bytes.NewReader(img.GetJPEGThumbnail()))
	if err != nil || preview.Bounds().Dx() != previewSide || preview.Bounds().Dy() != previewSide/2 {
		t.Fatalf("preview = %v, %v", preview, err)
	}
	// The transparent half of the photo shows as white in the preview.
	if r, g, b, _ := preview.At(5, 5).RGBA(); r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Errorf("transparent pixels became %d,%d,%d", r>>8, g>>8, b>>8)
	}

	if v := mediaMessage(model.File{Name: "v.mp4"}, "video/mp4", "video", up).GetVideoMessage(); v.GetURL() != up.URL {
		t.Errorf("video = %v", v)
	}
	if a := mediaMessage(model.File{Name: "n.ogg"}, "audio/ogg; codecs=opus", "audio", up).GetAudioMessage(); !a.GetPTT() {
		t.Errorf("an Ogg file is not a voice message: %v", a)
	}
	if a := mediaMessage(model.File{Name: "s.mp3"}, "audio/mpeg", "audio", up).GetAudioMessage(); a.GetPTT() || a.GetURL() == "" {
		t.Errorf("an MP3 file is a voice message: %v", a)
	}
	doc := mediaMessage(model.File{Name: "r.pdf"}, "application/pdf", "document", up).GetDocumentMessage()
	if doc.GetFileName() != "r.pdf" || doc.GetTitle() != "r.pdf" || doc.GetFileEncSHA256() == nil {
		t.Errorf("document = %v", doc)
	}
}

func TestImageDetails(t *testing.T) {
	if w, h, preview := imageDetails([]byte("not an image"), maxPreviewPixels); w != nil || h != nil || preview != nil {
		t.Error("a file that is not an image got details")
	}
	data := pngFile(t, 40, 20)
	if w, h, preview := imageDetails(data, 100); *w != 40 || *h != 20 || preview != nil {
		t.Error("a photo above the pixel limit got a preview")
	}
	// A small photo keeps its size in the preview.
	if _, _, preview := imageDetails(data, maxPreviewPixels); preview == nil {
		t.Fatal("no preview")
	} else if img, _ := jpeg.Decode(bytes.NewReader(preview)); img.Bounds().Dx() != 40 {
		t.Errorf("preview width = %d", img.Bounds().Dx())
	}
	// A PNG whose header is fine but whose pixels are not gets its size and no preview.
	if w, _, preview := imageDetails(data[:60], maxPreviewPixels); w == nil || preview != nil {
		t.Errorf("broken PNG: width %v, preview %d bytes", w, len(preview))
	}
}

func TestWithCaption(t *testing.T) {
	msg := func() *waE2E.Message {
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}
	}
	if got := withCaption(msg(), ""); got.GetImageMessage().Caption != nil {
		t.Error("an empty caption was set")
	}
	if got := withCaption(msg(), "hi"); got.GetImageMessage().GetCaption() != "hi" {
		t.Error("image caption missing")
	}
	if got := withCaption(&waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, "hi"); got.GetVideoMessage().GetCaption() != "hi" {
		t.Error("video caption missing")
	}
	if doc := withCaption(&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{}}, "hi"); doc.GetDocumentMessage().GetCaption() != "hi" {
		t.Errorf("document with a caption = %v", doc)
	}
	if got := withCaption(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "hi"); got.GetAudioMessage() == nil {
		t.Error("audio lost its media")
	}
}

func TestMediaWithoutConnection(t *testing.T) {
	ctx := context.Background()
	c := linked(t)
	if _, err := c.Upload(ctx, model.File{Name: "a.txt", Data: []byte("x")}); err == nil {
		t.Error("Upload without a connection gave no error")
	}
	raw, _ := proto.Marshal(&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.txt")}})
	if err := c.Send(ctx, "1@g.us", c.NewID(), model.Outgoing{Text: "caption", Media: &model.Media{Raw: raw}}); err == nil {
		t.Error("Send of media without a connection gave no error")
	}
	if err := c.Send(ctx, "1@g.us", "id", model.Outgoing{Media: &model.Media{Raw: []byte{0xff, 0xff}}}); err == nil {
		t.Error("Send of bad media bytes gave no error")
	}
}
