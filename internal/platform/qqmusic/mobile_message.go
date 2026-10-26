package qqmusic

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

func (a *Adapter) mobileMessage(ctx context.Context, id, event string, payload []byte) (int, bool, error) {
	switch event {
	case "scanned":
		return QRCodeScanned, true, nil
	case "canceled":
		return QRCodeRefused, true, nil
	case "timeout":
		return QRCodeExpired, true, nil
	case "loginFailed":
		return 0, true, &MobileLoginError{Stage: "provider_rejected"}
	case "cookies":
		var data struct {
			Cookies map[string]struct {
				Value json.RawMessage `json:"value"`
			} `json:"cookies"`
		}
		if err := json.Unmarshal(payload, &data); err != nil {
			return 0, true, &MobileLoginError{Stage: "cookie_payload", Err: errors.New("qqmusic: malformed mobile credential")}
		}
		var idText, key string
		if err := json.Unmarshal(data.Cookies["qqmusic_uin"].Value, &idText); err != nil {
			idText = string(data.Cookies["qqmusic_uin"].Value)
		}
		musicID, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || musicID <= 0 {
			return 0, true, &MobileLoginError{Stage: "cookie_identity", Err: ErrCredentialInvalid}
		}
		if err := json.Unmarshal(data.Cookies["qqmusic_key"].Value, &key); err != nil || key == "" {
			return 0, true, &MobileLoginError{Stage: "cookie_key", Err: ErrCredentialInvalid}
		}
		response, err := a.callCGI(ctx, false, map[string]any{"tmeLoginType": 6}, RequestItem{Module: "music.login.LoginServer", Method: "Login", Param: Object(Field("musicid", musicID), Field("qrCodeID", id), Field("token", key))})
		if err != nil {
			return 0, true, &MobileLoginError{Stage: "credential_exchange", Err: err}
		}
		if err := a.saveLoginResponse(response); err != nil {
			return 0, true, &MobileLoginError{Stage: "credential_commit", Err: err}
		}
		return QRCodeDone, true, nil
	default:
		return 0, false, nil
	}
}
