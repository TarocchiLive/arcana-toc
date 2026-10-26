package qqmusic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var wxUUIDPattern = regexp.MustCompile(`uuid=([^"&]+)"`)

func (a *Adapter) QRStart(ctx context.Context, channel string) (QR, error) {
	if channel != "qq" && channel != "wx" && channel != "mobile" {
		return QR{}, errors.New("qqmusic: invalid login channel")
	}
	if err := a.prepareEnrollment(); err != nil {
		return QR{}, err
	}

	var (
		qr  QR
		err error
	)
	switch channel {
	case "qq":
		qr, err = a.startQQQR(ctx)
	case "wx":
		qr, err = a.startWXQR(ctx)
	case "mobile":
		qr, err = a.startMobileQR(ctx)
	}
	if err != nil {
		a.failEnrollment()
		return QR{}, err
	}
	a.activateEnrollment(qr)
	return qr, nil
}

func (a *Adapter) QRPoll(ctx context.Context, channel, identifier string) (int, error) {
	if identifier == "" {
		return 0, errors.New("qqmusic: QR identifier is empty")
	}
	a.loginMu.Lock()
	session := a.enrollment
	active := session != nil && session.channel == channel && session.identifier == identifier && session.ctx.Err() == nil
	if active {
		session.polls.Add(1)
	}
	a.loginMu.Unlock()
	if !active {
		return 0, errors.New("qqmusic: unknown or expired QR session")
	}
	pollCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	var (
		code int
		err  error
	)
	switch channel {
	case "qq":
		code, err = a.pollQQQR(pollCtx, identifier)
	case "wx":
		code, err = a.pollWXQR(pollCtx, identifier)
	case "mobile":
		code, err = a.pollMobileQR(pollCtx, identifier)
	default:
		err = errors.New("qqmusic: invalid login channel")
	}
	session.polls.Done()
	if err == nil && (code == QRCodeDone || code == QRCodeExpired || code == QRCodeRefused) {
		a.finishEnrollment(session)
	}
	return code, err
}

func (a *Adapter) prepareEnrollment() error {
	for {
		a.loginMu.Lock()
		if a.closed {
			a.loginMu.Unlock()
			return errors.New("qqmusic: adapter is closed")
		}
		if a.enrollmentStarting {
			a.loginMu.Unlock()
			return errors.New("qqmusic: QR enrollment already starting")
		}
		previous := a.enrollment
		if previous == nil {
			if a.credentialPath != "" && a.enrollmentLock == nil {
				lock, err := LockCredential(a.credentialPath)
				if err != nil {
					a.loginMu.Unlock()
					return err
				}
				a.enrollmentLock = lock
			}
			a.enrollmentStarting = true
			a.loginMu.Unlock()
			return nil
		}
		a.enrollment = nil
		previous.cancel()
		a.loginMu.Unlock()
		<-previous.done
		a.cancelMobileSessions()
	}
}

func (a *Adapter) cancelMobileSessions() {
	a.mu.Lock()
	sessions := a.mobile
	a.mobile = nil
	for _, session := range sessions {
		session.cancel()
	}
	a.mu.Unlock()
	for _, session := range sessions {
		<-session.done
	}
}

func (a *Adapter) activateEnrollment(qr QR) {
	timeout := a.enrollmentTimeout
	if timeout <= 0 {
		timeout = defaultEnrollmentTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	session := &enrollmentSession{channel: qr.Channel, identifier: qr.Identifier, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.loginMu.Lock()
	a.enrollmentStarting = false
	if a.closed {
		a.loginMu.Unlock()
		cancel()
		close(session.done)
		a.failEnrollment()
		return
	}
	a.enrollment = session
	a.loginMu.Unlock()
	go func() {
		<-ctx.Done()
		a.loginMu.Lock()
		active := a.enrollment == session
		var lock *CredentialLock
		if active {
			a.enrollment = nil
			lock = a.enrollmentLock
			a.enrollmentLock = nil
		}
		a.loginMu.Unlock()
		session.polls.Wait()
		if active && session.channel == "mobile" {
			a.cancelMobileSessions()
		}
		if lock != nil {
			_ = lock.Close()
		}
		close(session.done)
	}()
}

func (a *Adapter) failEnrollment() {
	a.loginMu.Lock()
	a.enrollmentStarting = false
	lock := a.enrollmentLock
	a.enrollmentLock = nil
	a.loginMu.Unlock()
	if lock != nil {
		_ = lock.Close()
	}
}

func (a *Adapter) finishEnrollment(session *enrollmentSession) {
	a.loginMu.Lock()
	if a.enrollment != session {
		a.loginMu.Unlock()
		return
	}
	a.enrollment = nil
	lock := a.enrollmentLock
	a.enrollmentLock = nil
	session.cancel()
	a.loginMu.Unlock()
	<-session.done
	if lock != nil {
		_ = lock.Close()
	}
}

func (a *Adapter) startQQQR(ctx context.Context) (QR, error) {
	query := url.Values{"appid": {"716027609"}, "e": {"2"}, "l": {"M"}, "s": {"3"}, "d": {"72"}, "v": {"4"}, "t": {strconv.FormatFloat(float64(time.Now().UnixNano()%1000000000)/1e9, 'f', -1, 64)}, "daid": {"383"}, "pt_3rd_aid": {"100497308"}}
	response, err := a.doPrivate(ctx, http.MethodGet, "https://ssl.ptlogin2.qq.com/ptqrshow?"+query.Encode(), nil, map[string]string{"Referer": "https://xui.ptlogin2.qq.com/"})
	if err != nil {
		return QR{}, err
	}
	identifier := ""
	for _, cookie := range (&http.Response{Header: response.Header}).Cookies() {
		if cookie.Name == "qrsig" {
			identifier = cookie.Value
		}
	}
	if identifier == "" {
		return QR{}, errors.New("qqmusic: QQ QR response missing identifier")
	}
	return QR{Channel: "qq", Identifier: identifier, MIME: "image/png", Image: response.Body}, nil
}

func (a *Adapter) startWXQR(ctx context.Context) (QR, error) {
	query := url.Values{
		"appid":         {"wx48db31d50e334801"},
		"redirect_uri":  {"https://y.qq.com/portal/wx_redirect.html?login_type=2&surl=https://y.qq.com/"},
		"response_type": {"code"}, "scope": {"snsapi_login"}, "state": {"STATE"},
		"href": {"https://y.qq.com/mediastyle/music_v17/src/css/popup_wechat.css#wechat_redirect"},
	}
	response, err := a.doPrivate(ctx, http.MethodGet, "https://open.weixin.qq.com/connect/qrconnect?"+query.Encode(), nil, nil)
	if err != nil {
		return QR{}, err
	}
	match := wxUUIDPattern.FindStringSubmatch(string(response.Body))
	if len(match) != 2 {
		return QR{}, errors.New("qqmusic: WeChat QR response missing identifier")
	}
	image, err := a.doPrivate(ctx, http.MethodGet, "https://open.weixin.qq.com/connect/qrcode/"+url.PathEscape(match[1]), nil, map[string]string{"Referer": "https://open.weixin.qq.com/connect/qrconnect"})
	if err != nil {
		return QR{}, err
	}
	return QR{Channel: "wx", Identifier: match[1], MIME: "image/jpeg", Image: image.Body}, nil
}

func (a *Adapter) startMobileQR(ctx context.Context) (QR, error) {
	response, err := a.callCGI(ctx, false, map[string]any{"ct": 23, "cv": 0}, RequestItem{Module: "music.login.LoginServer", Method: "CreateQRCode", Param: Object(Field("tmeAppID", "qqmusic"), Field("ct", 11), Field("cv", 14090008))})
	if err != nil {
		return QR{}, err
	}
	var data struct {
		QRCode   string `json:"qrcode"`
		QRCodeID string `json:"qrcodeID"`
	}
	raw, _ := jsonMarshal(response.Data)
	if err := jsonUnmarshal(raw, &data); err != nil || data.QRCode == "" || data.QRCodeID == "" {
		return QR{}, errors.New("qqmusic: mobile QR response invalid")
	}
	image, err := decodeBase64Image(data.QRCode)
	if err != nil {
		return QR{}, errors.New("qqmusic: mobile QR image invalid")
	}
	sessionContext, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	session := &mobileSession{code: QRCodeWaiting, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	for _, previous := range a.mobile {
		previous.cancel()
	}
	a.mobile = map[string]*mobileSession{data.QRCodeID: session}
	a.mu.Unlock()
	go a.runMobileSession(sessionContext, data.QRCodeID, session)
	return QR{Channel: "mobile", Identifier: data.QRCodeID, MIME: "image/png", Image: image}, nil
}

func (a *Adapter) pollQQQR(ctx context.Context, identifier string) (int, error) {
	query := url.Values{
		"u1": {"https://graph.qq.com/oauth2.0/login_jump"}, "ptqrtoken": {strconv.FormatInt(Hash33(identifier, 0), 10)},
		"ptredirect": {"0"}, "h": {"1"}, "t": {"1"}, "g": {"1"}, "from_ui": {"1"}, "ptlang": {"2052"},
		"action": {qqPollAction(time.Now())}, "js_ver": {"20102616"}, "js_type": {"1"},
		"pt_uistyle": {"40"}, "aid": {"716027609"}, "daid": {"383"}, "pt_3rd_aid": {"100497308"}, "has_onekey": {"1"},
	}
	response, err := a.doPrivate(ctx, http.MethodGet, "https://ssl.ptlogin2.qq.com/ptqrlogin?"+query.Encode(), nil, map[string]string{"Referer": "https://xui.ptlogin2.qq.com/", "Cookie": (&http.Cookie{Name: "qrsig", Value: identifier}).String()})
	if err != nil {
		return 0, err
	}
	match := qqStatusPattern.FindStringSubmatch(string(response.Body))
	if len(match) != 2 {
		return 0, errors.New("qqmusic: QQ QR status invalid")
	}
	args := qqArgumentPattern.FindAllStringSubmatch(match[1], -1)
	if len(args) == 0 {
		return 0, errors.New("qqmusic: QQ QR status missing code")
	}
	code, err := strconv.Atoi(args[0][1])
	if err != nil {
		return 0, errors.New("qqmusic: QQ QR status code invalid")
	}
	switch code {
	case QRCodeWaiting, QRCodeScanned, QRCodeExpired, QRCodeRefused:
		return code, nil
	case QRCodeDone:
		if len(args) < 3 {
			return 0, errors.New("qqmusic: missing QQ authorization parameters")
		}
		sig := qqSigPattern.FindStringSubmatch(args[2][1])
		uin := qqUINPattern.FindStringSubmatch(args[2][1])
		if len(sig) != 2 || len(uin) != 2 {
			return 0, errors.New("qqmusic: invalid QQ authorization parameters")
		}
		if err := a.authorizeQQ(ctx, uin[1], sig[1]); err != nil {
			return 0, err
		}
		return QRCodeDone, nil
	default:
		return 0, errors.New("qqmusic: unknown QQ QR status")
	}
}

func (a *Adapter) pollWXQR(ctx context.Context, identifier string) (int, error) {
	pollContext, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	query := url.Values{"uuid": {identifier}, "_": {strconv.FormatInt(time.Now().Unix()*1000, 10)}}
	response, err := a.doPrivate(pollContext, http.MethodGet, "https://lp.open.weixin.qq.com/connect/l/qrconnect?"+query.Encode(), nil, map[string]string{"Referer": "https://open.weixin.qq.com/"})
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return QRCodeWaiting, nil
		}
		return 0, err
	}
	match := wxStatusPattern.FindStringSubmatch(string(response.Body))
	if len(match) != 3 {
		return 0, errors.New("qqmusic: WeChat QR status invalid")
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, errors.New("qqmusic: WeChat QR status code invalid")
	}
	switch code {
	case 408:
		return QRCodeWaiting, nil
	case 404:
		return QRCodeScanned, nil
	case 402:
		return QRCodeExpired, nil
	case 403:
		return QRCodeRefused, nil
	case 405:
		if match[2] == "" {
			return 0, errors.New("qqmusic: WeChat authorization code missing")
		}
		response, err := a.callCGI(ctx, false, map[string]any{"tmeLoginType": 1}, RequestItem{Module: "music.login.LoginServer", Method: "Login", Param: Object(Field("code", match[2]), Field("strAppid", "wx48db31d50e334801"))})
		if err != nil {
			return 0, err
		}
		if err := a.saveLoginResponse(response); err != nil {
			return 0, err
		}
		return QRCodeDone, nil
	default:
		return 0, errors.New("qqmusic: unknown WeChat QR status")
	}
}

func qqPollAction(now time.Time) string {
	milliseconds := strconv.FormatFloat(float64(now.UnixNano())/float64(time.Second)*1000, 'f', -1, 64)
	if !strings.Contains(milliseconds, ".") {
		milliseconds += ".0"
	}
	return "0-0-" + milliseconds
}

func (a *Adapter) pollMobileQR(ctx context.Context, identifier string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	a.mu.RLock()
	session := a.mobile[identifier]
	a.mu.RUnlock()
	if session == nil {
		return 0, errors.New("qqmusic: unknown mobile QR session")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.code, session.err
}

type privateResponse struct {
	Header http.Header
	Body   []byte
}

func (a *Adapter) doPrivate(ctx context.Context, method, endpoint string, body io.Reader, headers map[string]string) (privateResponse, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return privateResponse{}, errors.New("qqmusic: build login request")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	client := a.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return privateResponse{}, ctx.Err()
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return privateResponse{}, context.DeadlineExceeded
		}
		return privateResponse{}, errors.New("qqmusic: login request failed")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxQQBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return privateResponse{}, ctx.Err()
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return privateResponse{}, context.DeadlineExceeded
		}
	}
	if err != nil || len(data) > maxQQBodyBytes {
		return privateResponse{}, errors.New("qqmusic: login response invalid")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return privateResponse{}, fmt.Errorf("qqmusic: login HTTP %d", response.StatusCode)
	}
	return privateResponse{Header: response.Header.Clone(), Body: data}, nil
}

func jsonMarshal(value any) ([]byte, error)      { return json.Marshal(value) }
func jsonUnmarshal(data []byte, value any) error { return json.Unmarshal(data, value) }
