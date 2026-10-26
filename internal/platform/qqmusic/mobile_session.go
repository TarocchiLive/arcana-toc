package qqmusic

import (
	"context"
	"errors"
	"fmt"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gorilla/websocket"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
)

type mobileSession struct {
	mu     sync.Mutex
	code   int
	err    error
	cancel context.CancelFunc
	done   chan struct{}
}

func (a *Adapter) runMobileSession(ctx context.Context, id string, s *mobileSession) {
	defer s.cancel()
	defer close(s.done)
	finish := func(code int, err error) { s.mu.Lock(); s.code = code; s.err = err; s.mu.Unlock() }
	endpoint := "wss://mu.y.qq.com/ws/handshake"
	messages := make(chan *paho.Publish, 8192)
	var client *paho.Client
	for redirect := 0; redirect <= 3; redirect++ {
		dialer := websocket.Dialer{Subprotocols: []string{"mqtt"}, HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment}
		ws, _, err := dialer.DialContext(ctx, endpoint, http.Header{"Origin": {"https://y.qq.com"}, "Referer": {"https://y.qq.com/"}, "User-Agent": {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36"}})
		if err != nil {
			finish(0, errors.New("qqmusic: mobile connection failed"))
			return
		}
		ws.SetReadLimit(maxQQBodyBytes)
		stopClose := context.AfterFunc(ctx, func() { _ = ws.Close() })
		defer stopClose()
		candidate := paho.NewClient(paho.ClientConfig{Conn: &mqttWebSocket{socket: ws}, PacketTimeout: 20 * time.Second, OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(received paho.PublishReceived) (bool, error) {
			select {
			case messages <- received.Packet:
			default:
			}
			return true, nil
		}}})
		ack, err := candidate.Connect(ctx, &paho.Connect{ClientID: fmt.Sprintf("%d%d", time.Now().UnixMilli(), rand.IntN(9000)+1000), KeepAlive: 45, CleanStart: true, Properties: &paho.ConnectProperties{AuthMethod: "pass", User: paho.UserProperties{{Key: "tmeAppID", Value: "qqmusic"}, {Key: "business", Value: "management"}, {Key: "hashTag", Value: id}, {Key: "clientTag", Value: "management.user"}, {Key: "userID", Value: id}}}})
		if err == nil {
			client = candidate
			defer func() { _ = ws.Close() }()
			break
		}
		_ = ws.Close()
		if ack == nil || ack.Properties == nil || (ack.ReasonCode != 0x9c && ack.ReasonCode != 0x9d) || ack.Properties.ServerReference == "" || redirect == 3 {
			finish(0, errors.New("qqmusic: mobile MQTT connect failed"))
			return
		}
		reference := ack.Properties.ServerReference
		last := strings.LastIndexByte(endpoint, '/')
		if strings.Contains(endpoint[last+1:], ":") {
			endpoint = endpoint[:last+1] + reference
		} else {
			endpoint += "/" + reference
		}
	}
	ack, err := client.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "management.qrcode_login/" + id}}, Properties: &paho.SubscribeProperties{User: paho.UserProperties{{Key: "authorization", Value: "tmelogin"}, {Key: "pubsub", Value: "unicast"}}}})
	if err != nil || ack == nil {
		finish(0, errors.New("qqmusic: mobile subscription failed"))
		return
	}
	for _, reason := range ack.Reasons {
		if reason >= 128 {
			finish(0, errors.New("qqmusic: mobile subscription denied"))
			return
		}
	}
	finish(QRCodeWaiting, nil)
	for {
		select {
		case <-ctx.Done():
			finish(QRCodeExpired, nil)
			return
		case <-client.Done():
			finish(0, errors.New("qqmusic: mobile connection closed"))
			return
		case message := <-messages:
			event := ""
			if message.Properties != nil {
				for _, property := range message.Properties.User {
					if property.Key == "type" {
						event = property.Value
					}
				}
			}
			code, handled, err := a.mobileMessage(ctx, id, event, message.Payload)
			if !handled {
				continue
			}
			finish(code, err)
			if err != nil || code == QRCodeDone || code == QRCodeRefused || code == QRCodeExpired {
				return
			}
		}
	}
}
