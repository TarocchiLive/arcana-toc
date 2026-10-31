package qqmusic

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LingLambda/arcana-toc/internal/platform"
)

const (
	// AndroidAppVersion and AndroidSDKVersion are the default Android profile
	// used by qqmusic-api-python's version policy.
	AndroidAppVersion = "14.9.0.8"
	AndroidSDKVersion = "1.2.13.6"

	qimeiEndpoint    = "https://api.tencentmusic.com/tme/trpc/proxy"
	qimeiAppKey      = "0AND0HD6FE4HY80F"
	qimeiChannelID   = "10003505"
	qimeiPackageID   = "com.tencent.qqmusic"
	qimeiSecret      = "ZdJqM15EeO2zWc08"
	qimeiCacheAge    = 24 * time.Hour
	androidVersion   = 14090008
	androidUserAgent = "QQMusic 14090008(android %s)"
)

const qimeiPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDEIxgwoutfwoJxcGQeedgP7FG9
qaIuS0qzfR8gWkrkTZKM2iWHn2ajQpBRZjMSoSf6+KJGvar2ORhBfpDXyVtZCKp
qLQ+FLkpncClKVIrBwv6PHyUvuCb0rIarmgDnzkfQAqVufEtR64iazGDKatvJ9y
6B9NMbHddGSAUmRTCrHQIDAQAB
-----END PUBLIC KEY-----`

var qimeiPublicKey = mustQIMEIPublicKey()

// AndroidOSVersion is the Android build version included in QIMEI and Android
// CGI requests.
type AndroidOSVersion struct {
	Incremental string `json:"incremental"`
	Release     string `json:"release"`
	Codename    string `json:"codename"`
	SDK         int    `json:"sdk"`
}

// AndroidDevice is a stable Android fingerprint together with the QIMEI and
// GetSession state associated with it. Its JSON names retain the Python device
// state format, so a caller can persist and restore it without translation.
type AndroidDevice struct {
	Display         string           `json:"display"`
	Product         string           `json:"product"`
	Device          string           `json:"device"`
	Board           string           `json:"board"`
	Model           string           `json:"model"`
	Fingerprint     string           `json:"fingerprint"`
	BootID          string           `json:"boot_id"`
	ProcVersion     string           `json:"proc_version"`
	IMEI            string           `json:"imei"`
	Brand           string           `json:"brand"`
	Bootloader      string           `json:"bootloader"`
	BaseBand        string           `json:"base_band"`
	Version         AndroidOSVersion `json:"version"`
	SIMInfo         string           `json:"sim_info"`
	OSType          string           `json:"os_type"`
	MACAddress      string           `json:"mac_address"`
	IPAddress       []int            `json:"ip_address"`
	WiFiBSSID       string           `json:"wifi_bssid"`
	WiFiSSID        string           `json:"wifi_ssid"`
	IMSIMD5         []byte           `json:"imsi_md5"`
	AndroidID       string           `json:"android_id"`
	APN             string           `json:"apn"`
	VendorName      string           `json:"vendor_name"`
	VendorOSName    string           `json:"vendor_os_name"`
	QIMEI           string           `json:"qimei,omitempty"`
	QIMEI36         string           `json:"qimei36,omitempty"`
	QIMEISaveTime   int64            `json:"qimei_save_time,omitempty"`
	SessionUID      string           `json:"session_uid,omitempty"`
	SessionSID      string           `json:"session_sid,omitempty"`
	SessionVKey     string           `json:"session_vkey,omitempty"`
	SessionSaveTime int64            `json:"session_save_time,omitempty"`
	OpenUDID        string           `json:"open_udid"`
}

// QIMEI is the pair of device identifiers returned by Tencent's QIMEI proxy.
type QIMEI struct {
	Q16 string
	Q36 string
}

// DeviceContextOptions configures a DeviceContext. Device may be restored from
// persistent state. HTTPClient, endpoint and randomness dependencies are
// injectable to make provider interactions deterministic in callers' tests.
type DeviceContextOptions struct {
	HTTPClient      *http.Client
	Credential      Credential
	Device          *AndroidDevice
	QIMEIEndpoint   string
	SessionEndpoint string
	Now             func() time.Time
	Entropy         io.Reader
}

// DeviceContext owns one Android fingerprint and its short-lived QIMEI and
// GetSession state. It deliberately does not depend on Adapter, so an adapter
// can choose when to attach Android common parameters.
type DeviceContext struct {
	mu              sync.Mutex
	client          *http.Client
	credential      Credential
	device          AndroidDevice
	qimeiEndpoint   string
	sessionEndpoint string
	now             func() time.Time
	entropy         io.Reader
}

// NewDeviceContextWithOptions creates a context from a restored or newly
// generated device fingerprint.
func NewDeviceContextWithOptions(options DeviceContextOptions) (*DeviceContext, error) {
	entropy := options.Entropy
	if entropy == nil {
		entropy = cryptorand.Reader
	}
	var device AndroidDevice
	if options.Device == nil {
		var err error
		device, err = GenerateAndroidDevice(entropy)
		if err != nil {
			return nil, fmt.Errorf("qqmusic: generate device: %w", err)
		}
	} else {
		device = cloneAndroidDevice(*options.Device)
	}
	qimeiURL := options.QIMEIEndpoint
	if qimeiURL == "" {
		qimeiURL = qimeiEndpoint
	}
	sessionURL := options.SessionEndpoint
	if sessionURL == "" {
		sessionURL = cgiEndpoint
	}
	if !validDeviceEndpoint(qimeiURL) || !validDeviceEndpoint(sessionURL) {
		return nil, errors.New("qqmusic: invalid device endpoint")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &DeviceContext{
		client:          client,
		credential:      options.Credential,
		device:          device,
		qimeiEndpoint:   qimeiURL,
		sessionEndpoint: sessionURL,
		now:             now,
		entropy:         entropy,
	}, nil
}

// Device returns a copy of the current Android state. Persist this value after
// a successful QIMEI or session request to preserve its cache across restarts.
func (c *DeviceContext) Device() AndroidDevice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneAndroidDevice(c.device)
}

// SetCredential changes the credential used for future Android comm and
// GetSession requests. A new credential does not mutate the device fingerprint.
func (c *DeviceContext) SetCredential(credential Credential) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credential = credential
}

// QIMEI returns the cached QIMEI pair when it is younger than 24 hours;
// otherwise it performs the real QIMEI RSA/AES request and updates Device.
func (c *DeviceContext) QIMEI(ctx context.Context) (QIMEI, error) {
	if err := ctx.Err(); err != nil {
		return QIMEI{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qimeiLocked(ctx, c.now().UTC())
}

// EnsureSession obtains an Android GetSession response when the current uid and
// sid are missing or are at least 24 hours old. It also ensures fresh QIMEI.
func (c *DeviceContext) EnsureSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	qimei, err := c.qimeiLocked(ctx, now)
	if err != nil {
		return err
	}
	if c.sessionValidLocked(now) {
		return nil
	}
	comm := buildAndroidComm(c.credential, c.device, qimei)
	body, err := BuildCGIPayload(comm, RequestItem{
		Module: "music.getSession.session",
		Method: "GetSession",
		Param: Object(
			Field("uid", c.device.SessionUID),
			Field("vkey", 0),
			Field("caller", 0),
		),
	})
	if err != nil {
		return errors.New("qqmusic: encode GetSession request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sessionEndpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("qqmusic: build GetSession request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", fmt.Sprintf(androidUserAgent, c.device.Version.Release))
	response, err := c.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("qqmusic: GetSession request failed: %w", platform.ErrTemporary)
	}
	defer func() { _ = response.Body.Close() }()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if response.StatusCode != http.StatusOK {
		if platform.IsTemporaryHTTPStatus(response.StatusCode) {
			return fmt.Errorf("qqmusic: GetSession HTTP %d: %w", response.StatusCode, platform.ErrTemporary)
		}
		return fmt.Errorf("qqmusic: GetSession HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("qqmusic: GetSession response invalid: %w", platform.ErrTemporary)
	}
	if len(raw) > maxResponseBytes {
		return errors.New("qqmusic: GetSession response invalid")
	}
	uid, sid, vkey, err := parseSessionResponse(raw)
	if err != nil {
		return err
	}
	c.device.SessionUID = uid
	c.device.SessionSID = sid
	c.device.SessionVKey = vkey
	c.device.SessionSaveTime = now.Unix()
	return nil
}

// AndroidComm returns ordered Android common parameters from the current
// device state. Call EnsureSession first when the request requires uid/sid.
func (c *DeviceContext) AndroidComm() OrderedObject {
	c.mu.Lock()
	defer c.mu.Unlock()
	return buildAndroidComm(c.credential, c.device, QIMEI{Q16: c.device.QIMEI, Q36: c.device.QIMEI36})
}

// BuildWebComm constructs ordered common parameters for qqmusic-api-python's
// web profile. MusicID must be a non-negative decimal value when present.
func BuildWebComm(credential Credential) (OrderedObject, error) {
	fields := []JSONField{
		Field("ct", 24),
		Field("cv", 4747474),
		Field("platform", "yqq.json"),
		Field("chid", "0"),
	}
	if credential.MusicID != "" {
		uin, err := strconv.ParseInt(credential.MusicID, 10, 64)
		if err != nil || uin < 0 {
			return nil, errors.New("qqmusic: invalid credential musicid")
		}
		fields = append(fields, Field("uin", uin))
	}
	gTK := Hash33(credential.MusicKey, 5381)
	fields = append(fields,
		Field("g_tk", gTK),
		Field("g_tk_new_20200303", gTK),
		Field("format", "json"),
		Field("inCharset", "utf-8"),
		Field("outCharset", "utf-8"),
		Field("notice", 0),
		Field("need_new_code", 1),
	)
	return Object(fields...), nil
}

// GenerateAndroidDevice creates the Python default Android device profile with
// fresh identifier values. Entropy must provide unpredictable bytes in
// production; use a fixed reader only for deterministic tests.
func GenerateAndroidDevice(entropy io.Reader) (AndroidDevice, error) {
	if entropy == nil {
		entropy = cryptorand.Reader
	}
	displayNumber, err := randomRange(entropy, 100000, 999999)
	if err != nil {
		return AndroidDevice{}, err
	}
	fingerprintNumber, err := randomRange(entropy, 1000000, 9999999)
	if err != nil {
		return AndroidDevice{}, err
	}
	procSuffix, err := randomAlphaNumeric(entropy, 8)
	if err != nil {
		return AndroidDevice{}, err
	}
	bootID, err := randomUUID(entropy)
	if err != nil {
		return AndroidDevice{}, err
	}
	imei, err := randomIMEI(entropy)
	if err != nil {
		return AndroidDevice{}, err
	}
	imsiSource := make([]byte, 16)
	if _, err := io.ReadFull(entropy, imsiSource); err != nil {
		return AndroidDevice{}, err
	}
	imsiHash := md5.Sum(imsiSource)
	androidBytes := make([]byte, 8)
	if _, err := io.ReadFull(entropy, androidBytes); err != nil {
		return AndroidDevice{}, err
	}
	udid, err := randomUUID(entropy)
	if err != nil {
		return AndroidDevice{}, err
	}
	return AndroidDevice{
		Display:      fmt.Sprintf("QMAPI.%d.001", displayNumber),
		Product:      "iarim",
		Device:       "sagit",
		Board:        "eomam",
		Model:        "MI 6",
		Fingerprint:  fmt.Sprintf("xiaomi/iarim/sagit:10/eomam.200122.001/%d:user/release-keys", fingerprintNumber),
		BootID:       bootID,
		ProcVersion:  fmt.Sprintf("Linux 5.4.0-54-generic-%s (android-build@google.com)", procSuffix),
		IMEI:         imei,
		Brand:        "Xiaomi",
		Bootloader:   "U-boot",
		BaseBand:     "",
		Version:      AndroidOSVersion{Incremental: "5891938", Release: "10", Codename: "REL", SDK: 29},
		SIMInfo:      "T-Mobile",
		OSType:       "android",
		MACAddress:   "00:50:56:C0:00:08",
		IPAddress:    []int{10, 0, 1, 3},
		WiFiBSSID:    "00:50:56:C0:00:08",
		WiFiSSID:     "<unknown ssid>",
		IMSIMD5:      imsiHash[:],
		AndroidID:    hex.EncodeToString(androidBytes),
		APN:          "wifi",
		VendorName:   "MIUI",
		VendorOSName: "qmapi",
		OpenUDID:     strings.ReplaceAll(udid, "-", ""),
	}, nil
}

func (c *DeviceContext) qimeiLocked(ctx context.Context, now time.Time) (QIMEI, error) {
	if c.qimeiValidLocked(now) {
		return QIMEI{Q16: c.device.QIMEI, Q36: c.device.QIMEI36}, nil
	}
	request, err := buildQIMEIRequest(c.device, now, c.entropy)
	if err != nil {
		return QIMEI{}, errors.New("qqmusic: encode QIMEI request")
	}
	body, err := marshalJSON(request.Body)
	if err != nil {
		return QIMEI{}, errors.New("qqmusic: encode QIMEI request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.qimeiEndpoint, bytes.NewReader(body))
	if err != nil {
		return QIMEI{}, errors.New("qqmusic: build QIMEI request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	for name, value := range request.Headers {
		httpRequest.Header.Set(name, value)
	}
	response, err := c.client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return QIMEI{}, ctx.Err()
		}
		return QIMEI{}, fmt.Errorf("qqmusic: QIMEI request failed: %w", platform.ErrTemporary)
	}
	defer func() { _ = response.Body.Close() }()
	if ctx.Err() != nil {
		return QIMEI{}, ctx.Err()
	}
	if response.StatusCode != http.StatusOK {
		if platform.IsTemporaryHTTPStatus(response.StatusCode) {
			return QIMEI{}, fmt.Errorf("qqmusic: QIMEI HTTP %d: %w", response.StatusCode, platform.ErrTemporary)
		}
		return QIMEI{}, fmt.Errorf("qqmusic: QIMEI HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if ctx.Err() != nil {
		return QIMEI{}, ctx.Err()
	}
	if err != nil {
		return QIMEI{}, fmt.Errorf("qqmusic: QIMEI response invalid: %w", platform.ErrTemporary)
	}
	if len(raw) > maxResponseBytes {
		return QIMEI{}, errors.New("qqmusic: QIMEI response invalid")
	}
	qimei, err := parseQIMEIResponse(raw)
	if err != nil {
		return QIMEI{}, err
	}
	c.device.QIMEI = qimei.Q16
	c.device.QIMEI36 = qimei.Q36
	c.device.QIMEISaveTime = now.Unix()
	return qimei, nil
}

func (c *DeviceContext) qimeiValidLocked(now time.Time) bool {
	return c.device.QIMEI != "" && c.device.QIMEI36 != "" && now.Unix()-c.device.QIMEISaveTime < int64(qimeiCacheAge/time.Second)
}

func (c *DeviceContext) sessionValidLocked(now time.Time) bool {
	return c.device.SessionUID != "" && c.device.SessionSID != "" && now.Unix()-c.device.SessionSaveTime < int64(qimeiCacheAge/time.Second)
}

func buildAndroidComm(credential Credential, device AndroidDevice, qimei QIMEI) OrderedObject {
	fields := []JSONField{
		Field("ct", 11),
		Field("cv", androidVersion),
		Field("v", androidVersion),
		Field("chid", qimeiChannelID),
	}
	if credential.MusicID != "" {
		fields = append(fields, Field("qq", credential.MusicID))
	}
	if credential.MusicKey != "" {
		fields = append(fields, Field("authst", credential.MusicKey))
	}
	fields = append(fields, Field("tmeAppID", "qqmusic"))
	if credential.LoginType != 0 {
		fields = append(fields, Field("tmeLoginType", credential.LoginType))
	}
	fields = append(fields,
		Field("QIMEI", qimei.Q16),
		Field("QIMEI36", qimei.Q36),
		Field("OpenUDID", device.OpenUDID),
		Field("udid", device.OpenUDID),
	)
	if device.SessionUID != "" {
		fields = append(fields, Field("uid", device.SessionUID))
	}
	fields = append(fields, Field("OpenUDID2", device.OpenUDID))
	if device.SessionSID != "" {
		fields = append(fields, Field("sid", device.SessionSID))
	}
	fields = append(fields,
		Field("aid", device.AndroidID),
		Field("os_ver", device.Version.Release),
		Field("phonetype", device.Model),
		Field("devicelevel", strconv.Itoa(device.Version.SDK)),
		Field("newdevicelevel", strconv.Itoa(device.Version.SDK)),
		Field("rom", device.Fingerprint),
	)
	return Object(fields...)
}

type qimeiRequest struct {
	Headers map[string]string
	Body    OrderedObject
}

func buildQIMEIRequest(device AndroidDevice, now time.Time, entropy io.Reader) (qimeiRequest, error) {
	payload, err := buildQIMEIPayload(device, AndroidAppVersion, AndroidSDKVersion, now, entropy)
	if err != nil {
		return qimeiRequest{}, err
	}
	cryptKey, err := randomHex(entropy, 16)
	if err != nil {
		return qimeiRequest{}, err
	}
	nonce, err := randomHex(entropy, 16)
	if err != nil {
		return qimeiRequest{}, err
	}
	// QQMusicApi 0.7.2 QIMEI requires PKCS#1 v1.5; OAEP is not wire-compatible.
	//nolint:staticcheck // SA1019: approved legacy-protocol exception; keep standard-library encryption.
	keyCiphertext, err := rsa.EncryptPKCS1v15(cryptorand.Reader, qimeiPublicKey, []byte(cryptKey))
	if err != nil {
		return qimeiRequest{}, err
	}
	payloadJSON, err := marshalJSON(payload)
	if err != nil {
		return qimeiRequest{}, err
	}
	paramsCiphertext, err := aesCBCEncrypt([]byte(cryptKey), payloadJSON)
	if err != nil {
		return qimeiRequest{}, err
	}
	ts := now.Unix()
	key := base64.StdEncoding.EncodeToString(keyCiphertext)
	params := base64.StdEncoding.EncodeToString(paramsCiphertext)
	extra := `{"appKey":"` + qimeiAppKey + `"}`
	sign := md5Hex(key, params, strconv.FormatInt(ts*1000, 10), nonce, qimeiSecret, extra)
	return qimeiRequest{
		Headers: map[string]string{
			"Host":       "api.tencentmusic.com",
			"method":     "GetQimei",
			"service":    "trpc.tme_datasvr.qimeiproxy.QimeiProxy",
			"appid":      "qimei_qq_android",
			"sign":       md5Hex("qimei_qq_androidpzAuCmaFAaFaHrdakPjLIEqKrGnSOOvH", strconv.FormatInt(ts, 10)),
			"user-agent": "QQMusic",
			"timestamp":  strconv.FormatInt(ts, 10),
		},
		Body: Object(
			Field("app", 0),
			Field("os", 1),
			Field("qimeiParams", Object(
				Field("key", key),
				Field("params", params),
				Field("time", strconv.FormatInt(ts, 10)),
				Field("nonce", nonce),
				Field("sign", sign),
				Field("extra", extra),
			)),
		),
	}, nil
}

func buildQIMEIPayload(device AndroidDevice, appVersion, sdkVersion string, now time.Time, entropy io.Reader) (OrderedObject, error) {
	fixedRand, err := randomRange(entropy, 0, 14400)
	if err != nil {
		return nil, err
	}
	beaconID, err := randomBeaconID(now, entropy)
	if err != nil {
		return nil, err
	}
	reservedJSON, err := marshalJSON(Object(
		Field("harmony", "0"),
		Field("clone", "0"),
		Field("containe", ""),
		Field("oz", "UhYmelwouA+V2nPWbOvLTgN2/m8jwGB+yUB5v9tysQg="),
		Field("oo", "Xecjt+9S1+f8Pz2VLSxgpw=="),
		Field("kelong", "0"),
		Field("uptimes", now.UTC().Add(-time.Duration(fixedRand)*time.Second).Format("2006-01-02 15:04:05")),
		Field("multiUser", "0"),
		Field("bod", device.Brand),
		Field("dv", device.Device),
		Field("firstLevel", ""),
		Field("manufact", device.Brand),
		Field("name", device.Model),
		Field("host", "se.infra"),
		Field("kernel", device.ProcVersion),
	))
	if err != nil {
		return nil, err
	}
	return Object(
		Field("androidId", device.AndroidID),
		Field("platformId", 1),
		Field("appKey", qimeiAppKey),
		Field("appVersion", appVersion),
		Field("beaconIdSrc", beaconID),
		Field("brand", device.Brand),
		Field("channelId", qimeiChannelID),
		Field("cid", ""),
		Field("imei", device.IMEI),
		Field("imsi", ""),
		Field("mac", ""),
		Field("model", device.Model),
		Field("networkType", "unknown"),
		Field("oaid", ""),
		Field("osVersion", fmt.Sprintf("Android %s,level %d", device.Version.Release, device.Version.SDK)),
		Field("qimei", ""),
		Field("qimei36", ""),
		Field("sdkVersion", sdkVersion),
		Field("targetSdkVersion", "33"),
		Field("audit", ""),
		Field("userId", "{}"),
		Field("packageId", qimeiPackageID),
		Field("deviceType", "Phone"),
		Field("sdkName", ""),
		Field("reserved", string(reservedJSON)),
	), nil
}

func randomBeaconID(now time.Time, entropy io.Reader) (string, error) {
	first, err := randomRange(entropy, 100000, 999999)
	if err != nil {
		return "", err
	}
	second, err := randomRange(entropy, 100000000, 999999999)
	if err != nil {
		return "", err
	}
	month := now.UTC().Format("2006-01-") + "01"
	var builder strings.Builder
	for index := 1; index <= 40; index++ {
		builder.WriteString("k")
		builder.WriteString(strconv.Itoa(index))
		builder.WriteByte(':')
		switch index {
		case 1, 2, 13, 14, 17, 18, 21, 22, 25, 26, 29, 30, 33, 34, 37, 38:
			builder.WriteString(month)
			builder.WriteString(strconv.Itoa(first))
			builder.WriteByte('.')
			builder.WriteString(strconv.Itoa(second))
		case 3:
			builder.WriteString("0000000000000000")
		case 4:
			value, err := randomNonZeroHex(entropy, 16)
			if err != nil {
				return "", err
			}
			builder.WriteString(value)
		default:
			value, err := randomRange(entropy, 0, 9999)
			if err != nil {
				return "", err
			}
			builder.WriteString(strconv.Itoa(value))
		}
		builder.WriteByte(';')
	}
	return builder.String(), nil
}

func aesCBCEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padding)
	copy(padded, plaintext)
	for index := range padded[len(plaintext):] {
		padded[len(plaintext)+index] = byte(padding)
	}
	cipher.NewCBCEncrypter(block, key).CryptBlocks(padded, padded)
	return padded, nil
}

func parseQIMEIResponse(raw []byte) (QIMEI, error) {
	var outer struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return QIMEI{}, errors.New("qqmusic: malformed QIMEI response")
	}
	var encoded string
	if len(outer.Data) == 0 || json.Unmarshal(outer.Data, &encoded) != nil {
		return QIMEI{}, errors.New("qqmusic: malformed QIMEI response")
	}
	var inner struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(encoded), &inner); err != nil {
		return QIMEI{}, errors.New("qqmusic: malformed QIMEI response")
	}
	q16Raw, hasQ16 := inner.Data["q16"]
	q36Raw, hasQ36 := inner.Data["q36"]
	if !hasQ16 || !hasQ36 {
		return QIMEI{}, errors.New("qqmusic: QIMEI response missing identifiers")
	}
	var qimei QIMEI
	if json.Unmarshal(q16Raw, &qimei.Q16) != nil || json.Unmarshal(q36Raw, &qimei.Q36) != nil || qimei.Q16 == "" || qimei.Q36 == "" {
		return QIMEI{}, errors.New("qqmusic: malformed QIMEI response")
	}
	return qimei, nil
}

func parseSessionResponse(raw []byte) (string, string, string, error) {
	var response struct {
		Code    int `json:"code"`
		Request struct {
			Code int `json:"code"`
			Data struct {
				Session struct {
					UID  json.RawMessage `json:"uid"`
					SID  string          `json:"sid"`
					VKey string          `json:"vkey"`
				} `json:"session"`
			} `json:"data"`
		} `json:"req_0"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Code != 0 || response.Request.Code != 0 || len(response.Request.Data.Session.UID) == 0 || response.Request.Data.Session.SID == "" {
		return "", "", "", errors.New("qqmusic: malformed GetSession response")
	}
	uid, err := jsonValueString(response.Request.Data.Session.UID)
	if err != nil || uid == "" {
		return "", "", "", errors.New("qqmusic: malformed GetSession response")
	}
	return uid, response.Request.Data.Session.SID, response.Request.Data.Session.VKey, nil
}

func jsonValueString(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", err
	}
	return number.String(), nil
}

func randomIMEI(entropy io.Reader) (string, error) {
	digits := make([]byte, 15)
	for index := range 14 {
		value, err := randomRange(entropy, 0, 9)
		if err != nil {
			return "", err
		}
		digits[index] = byte(value)
	}
	sum := 0
	for index, digit := range digits[:14] {
		value := int(digit)
		if index%2 == 1 {
			value *= 2
			if value > 9 {
				value -= 9
			}
		}
		sum += value
	}
	digits[14] = byte((10 - sum%10) % 10)
	for index := range digits {
		digits[index] += '0'
	}
	return string(digits), nil
}

func randomUUID(entropy io.Reader) (string, error) {
	data := make([]byte, 16)
	if _, err := io.ReadFull(entropy, data); err != nil {
		return "", err
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[:4], data[4:6], data[6:8], data[8:10], data[10:]), nil
}

func randomHex(entropy io.Reader, length int) (string, error) {
	data := make([]byte, length)
	if _, err := io.ReadFull(entropy, data); err != nil {
		return "", err
	}
	const hexChars = "0123456789abcdef"
	for index := range data {
		data[index] = hexChars[data[index]&0x0f]
	}
	return string(data), nil
}

func randomNonZeroHex(entropy io.Reader, length int) (string, error) {
	data := make([]byte, length)
	if _, err := io.ReadFull(entropy, data); err != nil {
		return "", err
	}
	const hexChars = "123456789abcdef"
	for index := range data {
		data[index] = hexChars[int(data[index])%len(hexChars)]
	}
	return string(data), nil
}

func randomAlphaNumeric(entropy io.Reader, length int) (string, error) {
	data := make([]byte, length)
	if _, err := io.ReadFull(entropy, data); err != nil {
		return "", err
	}
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for index := range data {
		data[index] = chars[int(data[index])%len(chars)]
	}
	return string(data), nil
}

func randomRange(entropy io.Reader, minimum, maximum int) (int, error) {
	if minimum > maximum {
		return 0, errors.New("invalid random range")
	}
	var data [8]byte
	if _, err := io.ReadFull(entropy, data[:]); err != nil {
		return 0, err
	}
	value := binary.BigEndian.Uint64(data[:])
	return minimum + int(value%uint64(maximum-minimum+1)), nil
}

func md5Hex(parts ...string) string {
	hash := md5.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func mustQIMEIPublicKey() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(qimeiPublicKeyPEM))
	if block == nil {
		panic("qqmusic: invalid QIMEI public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qqmusic: invalid QIMEI public key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		panic("qqmusic: invalid QIMEI public key")
	}
	return key
}

func validDeviceEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func cloneAndroidDevice(device AndroidDevice) AndroidDevice {
	device.IPAddress = append([]int(nil), device.IPAddress...)
	device.IMSIMD5 = append([]byte(nil), device.IMSIMD5...)
	return device
}
