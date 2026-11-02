package wa

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // PNG photos get a preview as well.
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// knownTypes fixes the MIME types of the files that WhatsApp shows in the chat itself, so that
// their handling does not depend on the system's MIME table. WhatsApp's voice messages are Opus
// in Ogg.
var knownTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".mp4": "video/mp4",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac",
	".ogg": "audio/ogg; codecs=opus", ".opus": "audio/ogg; codecs=opus",
}

// shown maps the MIME types that WhatsApp shows in the chat to their media type. Every other file
// goes out as a document.
var shown = map[string]string{
	"image/jpeg": "image", "image/png": "image", "video/mp4": "video",
	"audio/mpeg": "audio", "audio/mp4": "audio", "audio/aac": "audio", "audio/ogg": "audio",
}

var uploadTypes = map[string]whatsmeow.MediaType{
	"image": whatsmeow.MediaImage, "video": whatsmeow.MediaVideo, "audio": whatsmeow.MediaAudio,
	"document": whatsmeow.MediaDocument,
}

const (
	// maxPreviewPixels bounds the photos that get a preview, because decoding a photo takes four
	// bytes per pixel.
	maxPreviewPixels = 50_000_000
	// previewSide is the longer side of a photo's preview, in pixels.
	previewSide = 96
)

// fileType returns a file's MIME type and its media type. The MIME type comes from the file
// name's extension when the extension is known, and from the file's first bytes otherwise.
func fileType(f model.File) (mimeType, mediaType string) {
	ext := strings.ToLower(filepath.Ext(f.Name))
	if mimeType = knownTypes[ext]; mimeType == "" {
		mimeType = mime.TypeByExtension(ext)
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(f.Data)
	}
	base, _, _ := strings.Cut(mimeType, ";")
	if mediaType = shown[base]; mediaType == "" {
		mediaType = "document"
	}
	return mimeType, mediaType
}

// Upload encrypts a file and uploads it to WhatsApp's media servers. The media that it returns
// can go out in any number of messages, through SendMedia.
func (c *Client) Upload(ctx context.Context, f model.File) (model.Media, error) {
	mimeType, mediaType := fileType(f)
	up, err := c.cli.Upload(ctx, f.Data, uploadTypes[mediaType])
	if err != nil {
		return model.Media{}, fmt.Errorf("upload %s: %w", f.Name, err)
	}
	raw, err := proto.Marshal(mediaMessage(f, mimeType, mediaType, up))
	return model.Media{Type: mediaType, MimeType: mimeType, Filename: f.Name, Size: len(f.Data), Raw: raw}, err
}

// SendMedia sends uploaded media to chat with a caption, under the message ID id.
func (c *Client) SendMedia(ctx context.Context, chat, id string, media model.Media, caption string) error {
	var msg waE2E.Message
	if err := proto.Unmarshal(media.Raw, &msg); err != nil {
		return fmt.Errorf("decode media: %w", err)
	}
	return c.sendAs(ctx, chat, id, withCaption(&msg, caption))
}

// mediaMessage builds the message for an uploaded file. A photo carries its size and a small
// preview, which WhatsApp shows until the phone has downloaded the photo. An Ogg file goes out as
// a voice message.
func mediaMessage(f model.File, mimeType, mediaType string, up whatsmeow.UploadResponse) *waE2E.Message {
	var msg waE2E.Message
	var part proto.Message
	switch mediaType {
	case "image":
		width, height, preview := imageDetails(f.Data, maxPreviewPixels)
		msg.ImageMessage = &waE2E.ImageMessage{Width: width, Height: height, JPEGThumbnail: preview}
		part = msg.ImageMessage
	case "video":
		msg.VideoMessage = &waE2E.VideoMessage{}
		part = msg.VideoMessage
	case "audio":
		msg.AudioMessage = &waE2E.AudioMessage{PTT: proto.Bool(strings.HasPrefix(mimeType, "audio/ogg"))}
		part = msg.AudioMessage
	default:
		msg.DocumentMessage = &waE2E.DocumentMessage{FileName: proto.String(f.Name), Title: proto.String(f.Name)}
		part = msg.DocumentMessage
	}
	setUpload(part.ProtoReflect(), mimeType, up)
	return &msg
}

// setUpload fills the fields that every kind of media message has, which carry the upload. The
// four kinds give these fields the same names.
func setUpload(part protoreflect.Message, mimeType string, up whatsmeow.UploadResponse) {
	fields := part.Descriptor().Fields()
	for name, value := range map[protoreflect.Name]protoreflect.Value{
		"mimetype":          protoreflect.ValueOfString(mimeType),
		"URL":               protoreflect.ValueOfString(up.URL),
		"directPath":        protoreflect.ValueOfString(up.DirectPath),
		"mediaKey":          protoreflect.ValueOfBytes(up.MediaKey),
		"mediaKeyTimestamp": protoreflect.ValueOfInt64(time.Now().Unix()),
		"fileSHA256":        protoreflect.ValueOfBytes(up.FileSHA256),
		"fileEncSHA256":     protoreflect.ValueOfBytes(up.FileEncSHA256),
		"fileLength":        protoreflect.ValueOfUint64(up.FileLength),
	} {
		part.Set(fields.ByName(name), value)
	}
}

// withCaption puts a caption on a media message. WhatsApp sends a document with a caption inside
// a documentWithCaptionMessage, and audio has no caption.
func withCaption(msg *waE2E.Message, caption string) *waE2E.Message {
	if caption == "" {
		return msg
	}
	text := proto.String(caption)
	switch {
	case msg.ImageMessage != nil:
		msg.ImageMessage.Caption = text
	case msg.VideoMessage != nil:
		msg.VideoMessage.Caption = text
	case msg.DocumentMessage != nil:
		msg.DocumentMessage.Caption = text
		return &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: msg}}
	}
	return msg
}

// imageDetails returns a photo's width and height, and a small JPEG preview of it when it has at
// most maxPixels pixels. A file that does not decode as JPEG or PNG gets none of them.
func imageDetails(data []byte, maxPixels int) (width, height *uint32, preview []byte) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, nil, nil
	}
	width, height = proto.Uint32(uint32(cfg.Width)), proto.Uint32(uint32(cfg.Height))
	if cfg.Width*cfg.Height > maxPixels {
		return width, height, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return width, height, nil
	}
	return width, height, scaledJPEG(img, previewSide)
}

// scaledJPEG scales img down so that its longer side is at most side pixels, picking the nearest
// pixel, and encodes it as JPEG on white, since JPEG has no transparency.
func scaledJPEG(img image.Image, side int) []byte {
	b := img.Bounds()
	longer := max(b.Dx(), b.Dy())
	scale := min(side, longer)
	w, h := max(1, b.Dx()*scale/longer), max(1, b.Dy()*scale/longer)
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			small.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	out := image.NewRGBA(small.Bounds())
	draw.Draw(out, out.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), small, image.Point{}, draw.Over)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, out, &jpeg.Options{Quality: 70})
	return buf.Bytes()
}
