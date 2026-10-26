package qqmusic

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *Adapter) authorizeQQ(ctx context.Context, uin, sig string) error {
	query := url.Values{"uin": {uin}, "pttype": {"1"}, "service": {"ptqrlogin"}, "nodirect": {"0"}, "ptsigx": {sig}, "s_url": {"https://graph.qq.com/oauth2.0/login_jump"}, "ptlang": {"2052"}, "ptredirect": {"100"}, "aid": {"716027609"}, "daid": {"383"}, "j_later": {"0"}, "low_login_hour": {"0"}, "regmaster": {"0"}, "pt_login_type": {"3"}, "pt_aid": {"0"}, "pt_aaid": {"16"}, "pt_light": {"0"}, "pt_3rd_aid": {"100497308"}}
	response, err := a.authorizationRequest(ctx, http.MethodGet, "https://ssl.ptlogin2.graph.qq.com/check_sig?"+query.Encode(), nil, nil)
	if err != nil {
		return err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	checkURL, _ := url.Parse("https://ssl.ptlogin2.graph.qq.com/check_sig")
	authorizeURL, _ := url.Parse("https://graph.qq.com/oauth2.0/authorize")
	jar.SetCookies(checkURL, response.Cookies())
	cookies := jar.Cookies(authorizeURL)
	skey := ""
	for _, cookie := range cookies {
		if cookie.Name == "p_skey" {
			skey = cookie.Value
		}
	}
	if skey == "" {
		return errors.New("qqmusic: missing QQ authorization cookie")
	}
	form := url.Values{"response_type": {"code"}, "client_id": {"100497308"}, "redirect_uri": {"https://y.qq.com/portal/wx_redirect.html?login_type=1&surl=https://y.qq.com/"}, "scope": {"get_user_info,get_app_friends"}, "state": {"state"}, "switch": {""}, "from_ptlogin": {"1"}, "src": {"1"}, "update_auth": {"1"}, "openapi": {"1010_1030"}, "g_tk": {strconv.FormatInt(Hash33(skey, 5381), 10)}, "auth_time": {strconv.FormatInt(time.Now().Unix()*1000, 10)}, "ui": {uuid.NewString()}}
	response, err = a.authorizationRequest(ctx, http.MethodPost, "https://graph.qq.com/oauth2.0/authorize", strings.NewReader(form.Encode()), cookies)
	if err != nil {
		return err
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil || location.Query().Get("code") == "" {
		return errors.New("qqmusic: missing QQ authorization code")
	}
	result, err := a.callCGI(ctx, false, map[string]any{"tmeLoginType": 2}, RequestItem{Module: "QQConnectLogin.LoginServer", Method: "QQLogin", Param: Object(Field("code", location.Query().Get("code")))})
	if err != nil {
		return err
	}
	return a.saveLoginResponse(result)
}

func (a *Adapter) authorizationRequest(ctx context.Context, method, endpoint string, body io.Reader, cookies []*http.Cookie) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, errors.New("qqmusic: invalid authorization request")
	}
	request.Header.Set("User-Agent", defaultUserAgent)
	if method == http.MethodGet {
		request.Header.Set("Referer", "https://xui.ptlogin2.qq.com/")
	} else {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	client := *a.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("qqmusic: authorization request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return nil, errors.New("qqmusic: authorization HTTP failure")
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, maxQQBodyBytes))
	if err != nil {
		return nil, errors.New("qqmusic: authorization response failed")
	}
	return response, nil
}
