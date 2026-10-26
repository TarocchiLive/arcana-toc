package netease

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/LingLambda/arcana-toc/internal/platform"
	"github.com/go-musicfox/netease-music/util"
)

// QR is a pending NetEase QR enrollment.
type QR struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

var qrStartOmittedCookies = map[string]struct{}{
	"__csrf":        {},
	"__remember_me": {},
	"NMTID":         {},
	"os":            {},
}

func (a *Adapter) qrStartJar() http.CookieJar {
	return &requestCookieJar{
		base: a.jar,
		host: "music.163.com",
		omit: qrStartOmittedCookies,
	}
}

func (a *Adapter) qrPollJar() http.CookieJar {
	return &requestCookieJar{
		base: a.jar,
		host: "music.163.com",
		omit: qrStartOmittedCookies,
		include: []*http.Cookie{
			{Name: "os", Value: "pc"},
			{Name: "NMTID", Value: "some_random_id_from_strategy"},
		},
	}
}

// QRStart creates an enrollment key and the corresponding NetEase login URL.
func (a *Adapter) QRStart(ctx context.Context) (QR, error) {
	code, body, err := a.callWeapiWithJar(ctx, "start qr login", "login/qrcode/unikey", map[string]interface{}{
		"type":         1,
		"noCheckToken": true,
	}, a.qrStartJar())
	if err != nil {
		return QR{}, err
	}
	if code != 200 {
		return QR{}, fmt.Errorf("netease start qr login: response code %d", code)
	}
	var response struct {
		Unikey string `json:"unikey"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Unikey == "" {
		return QR{}, fmt.Errorf("netease start qr login: malformed response")
	}
	chainID := util.GenerateChainID(a.jar)
	return QR{
		Key: response.Unikey,
		URL: "https://music.163.com/login?codekey=" + response.Unikey + "&chainId=" + chainID,
	}, nil
}

// QRPoll reports the NetEase QR scan state. A successful confirmation persists
// the newly issued session before it returns.
func (a *Adapter) QRPoll(ctx context.Context, key string) (int, error) {
	if key == "" {
		return 0, fmt.Errorf("netease qr poll: key is empty")
	}
	// Match LoginQRService.CheckQR without permanently adding its synthetic
	// strategy cookies to the shared session used by unrelated requests.
	_, body, err := a.callWeapiWithJar(ctx, "poll qr login", "login/qrcode/client/login", map[string]interface{}{
		"type":         1,
		"noCheckToken": true,
		"key":          key,
	}, a.qrPollJar())
	if err != nil {
		return 0, err
	}
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return 0, fmt.Errorf("netease qr poll: malformed response")
	}
	switch response.Code {
	case 800, 801, 802, 803:
		return response.Code, nil
	case 301:
		return 0, fmt.Errorf("netease qr poll: %w", platform.ErrNotLoggedIn)
	default:
		return 0, fmt.Errorf("netease qr poll: response code %d", response.Code)
	}
}
