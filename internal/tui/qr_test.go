package tui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"

	qrcode "github.com/skip2/go-qrcode"
)

func TestRenderLoginImageRejectsInvalidImages(t *testing.T) {
	var blank bytes.Buffer
	if err := png.Encode(&blank, image.NewGray(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	// A GIF header can advertise enormous pixels in very few bytes. DecodeConfig
	// reads this header, so the dimension guard must reject it before Decode.
	oversized := append([]byte("GIF89a"), make([]byte, 7)...)
	binary.LittleEndian.PutUint16(oversized[6:8], 65535)
	binary.LittleEndian.PutUint16(oversized[8:10], 65535)
	for name, data := range map[string][]byte{
		"empty":        nil,
		"not-image":    []byte("not an image"),
		"no-qr":        blank.Bytes(),
		"dimensions":   oversized,
		"encoded-size": bytes.Repeat([]byte{'x'}, tuiResponseLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			text, err := renderLoginImage(data)
			if err == nil || text != "" {
				t.Fatalf("invalid QR image reported success: %q, %v", text, err)
			}
		})
	}
}

func TestRenderLoginImagePreservesPayloadWhitespace(t *testing.T) {
	payload := " https://example.com/login?token=abc \n"
	encoded, err := qrcode.Encode(payload, qrcode.Medium, 256)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderLoginImage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	original, err := qrcode.New(payload, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != original.ToSmallString(false) {
		t.Fatal("rendered QR no longer represents the original payload")
	}
}
