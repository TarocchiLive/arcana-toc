package tui

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/makiuchi-d/gozxing"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
	qrcode "github.com/skip2/go-qrcode"
)

// Login QR images are small; reject hostile dimensions before allocating pixels.
const loginQRMaxDimension = 2048

func renderLoginImage(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("service returned an empty login QR image")
	}
	if len(data) > tuiResponseLimit {
		return "", errors.New("service returned an oversized login QR image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return "", errors.New("service returned an invalid login QR image")
	}
	if cfg.Width > loginQRMaxDimension || cfg.Height > loginQRMaxDimension {
		return "", errors.New("service returned an oversized login QR image")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("service returned an invalid login QR image")
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(decoded)
	if err != nil {
		return "", errors.New("service returned an unreadable login QR image")
	}
	result, err := zxingqr.NewQRCodeReader().Decode(bitmap, nil)
	if err != nil || result == nil || result.GetText() == "" {
		return "", errors.New("service returned an unreadable login QR image")
	}
	qr, err := qrcode.New(result.GetText(), qrcode.Medium)
	if err != nil {
		return "", errors.New("service returned an invalid login QR payload")
	}
	return qr.ToSmallString(false), nil
}
