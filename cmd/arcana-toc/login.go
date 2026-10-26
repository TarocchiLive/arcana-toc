package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/LingLambda/arcana-toc/internal/config"
	"github.com/LingLambda/arcana-toc/internal/platform/netease"
	"github.com/skip2/go-qrcode"
)

// loginTerminal uses the running service: it alone owns and persists credentials.
func loginTerminal(ctx context.Context, cfg config.Config, out io.Writer) error {
	client := &http.Client{Timeout: 20 * time.Second}
	call := func(method, path string, dst any) error {
		req, err := http.NewRequestWithContext(ctx, method, "http://"+cfg.Addr()+path, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("无法连接服务，请确认同一配置的服务正在运行")
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			return fmt.Errorf("登录接口返回 HTTP %d；请检查服务日志，不要连续重试", resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(dst)
	}
	var qr netease.QR
	if err := call("POST", "/api/netease/login", &qr); err != nil {
		return err
	}
	if qr.Key == "" || qr.URL == "" {
		return errors.New("登录接口未返回有效二维码")
	}
	image, err := qrcode.New(qr.URL, qrcode.Low)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "请用网易云音乐 APP 扫码，并在手机上确认登录：\n"+image.ToSmallString(false)); err != nil {
		return err
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		var response struct {
			Code int `json:"code"`
		}
		if err := call("GET", "/api/netease/login?key="+url.QueryEscape(qr.Key), &response); err != nil {
			return err
		}
		switch response.Code {
		case 800:
			return errors.New("二维码已过期，请重新运行登录命令")
		case 801:
			if last != 801 {
				if _, err := fmt.Fprintln(out, "等待扫码……"); err != nil {
					return err
				}
			}
		case 802:
			if last != 802 {
				if _, err := fmt.Fprintln(out, "已扫码，请在手机上确认……"); err != nil {
					return err
				}
			}
		case 803:
			_, err := fmt.Fprintln(out, "登录成功，会话已由服务保存。可以关闭此终端，服务继续运行。")
			return err
		default:
			return fmt.Errorf("未知扫码状态 %d", response.Code)
		}
		last = response.Code
	}
}
