package qqmusic

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/LingLambda/arcana-toc/internal/fileutil"
)

// Credential mirrors qqmusic-api-python's Credential model. Expired maps to
// Python's expired_at field; its historical Go name is retained for callers.
// Both Python field names and their camel-case aliases are accepted on input.
type Credential struct {
	OpenID             string
	RefreshToken       string
	AccessToken        string
	Expired            int64
	MusicID            string
	MusicKey           string
	UnionID            string
	StrMusicID         string
	RefreshKey         string
	MusicKeyCreateTime int64
	KeyExpiresIn       int64
	FirstLogin         int
	BindAccountType    int
	NeedRefreshKeyIn   int
	EncryptUIN         string
	LoginType          int
}

type credentialInput struct {
	OpenID                  string          `json:"openid"`
	RefreshToken            string          `json:"refresh_token"`
	AccessToken             string          `json:"access_token"`
	ExpiredAt               *int64          `json:"expired_at"`
	MusicID                 json.RawMessage `json:"musicid"`
	MusicKey                string          `json:"musickey"`
	UnionID                 string          `json:"unionid"`
	StrMusicID              string          `json:"str_musicid"`
	RefreshKey              string          `json:"refresh_key"`
	MusicKeyCreateTime      *int64          `json:"musickeyCreateTime"`
	MusicKeyCreateTimeSnake *int64          `json:"musickey_create_time"`
	KeyExpiresIn            *int64          `json:"keyExpiresIn"`
	KeyExpiresInSnake       *int64          `json:"key_expires_in"`
	FirstLogin              *int            `json:"first_login"`
	BindAccountType         *int            `json:"bindAccountType"`
	BindAccountTypeSnake    *int            `json:"bind_account_type"`
	NeedRefreshKeyIn        *int            `json:"needRefreshKeyIn"`
	NeedRefreshKeyInSnake   *int            `json:"need_refresh_key_in"`
	EncryptUIN              *string         `json:"encryptUin"`
	EncryptUINSnake         *string         `json:"encrypt_uin"`
	LoginType               *int            `json:"loginType"`
	LoginTypeSnake          *int            `json:"login_type"`
}

func (c *Credential) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("qqmusic: malformed credential")
	}
	for _, name := range []string{
		"openid", "refresh_token", "access_token", "expired_at", "musicid", "musickey", "unionid", "str_musicid", "refresh_key",
		"musickeyCreateTime", "musickey_create_time", "keyExpiresIn", "key_expires_in", "first_login", "bindAccountType", "bind_account_type",
		"needRefreshKeyIn", "need_refresh_key_in", "encryptUin", "encrypt_uin", "loginType", "login_type",
	} {
		if value, ok := fields[name]; ok && string(value) == "null" {
			return errors.New("qqmusic: malformed credential")
		}
	}
	var value credentialInput
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("qqmusic: malformed credential")
	}
	musicID, err := credentialMusicID(value.MusicID)
	if err != nil {
		return errors.New("qqmusic: malformed credential")
	}
	*c = Credential{
		OpenID: value.OpenID, RefreshToken: value.RefreshToken, AccessToken: value.AccessToken,
		Expired: valueOrZero(value.ExpiredAt), MusicID: musicID, MusicKey: value.MusicKey,
		UnionID: value.UnionID, StrMusicID: value.StrMusicID, RefreshKey: value.RefreshKey,
		MusicKeyCreateTime: int64Alias(value.MusicKeyCreateTime, value.MusicKeyCreateTimeSnake),
		KeyExpiresIn:       int64Alias(value.KeyExpiresIn, value.KeyExpiresInSnake),
		FirstLogin:         valueOrZero(value.FirstLogin),
		BindAccountType:    intAlias(value.BindAccountType, value.BindAccountTypeSnake),
		NeedRefreshKeyIn:   intAlias(value.NeedRefreshKeyIn, value.NeedRefreshKeyInSnake),
		EncryptUIN:         stringAlias(value.EncryptUIN, value.EncryptUINSnake),
		LoginType:          intAlias(value.LoginType, value.LoginTypeSnake),
	}
	if value.LoginType == nil && value.LoginTypeSnake == nil && c.MusicKey != "" {
		if len(c.MusicKey) >= 3 && c.MusicKey[:3] == "W_X" {
			c.LoginType = 1
		} else {
			c.LoginType = 2
		}
	}
	return nil
}

func credentialMusicID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return "", err
		}
		text = number.String()
	}
	parsed, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return "", errors.New("invalid musicid")
	}
	return parsed.String(), nil
}

func int64Alias(preferred, fallback *int64) int64 {
	if preferred != nil {
		return *preferred
	}
	if fallback != nil {
		return *fallback
	}
	return 0
}

func valueOrZero[T int | int64](value *T) T {
	if value != nil {
		return *value
	}
	return 0
}

func intAlias(preferred, fallback *int) int {
	if preferred != nil {
		return *preferred
	}
	if fallback != nil {
		return *fallback
	}
	return 0
}

func stringAlias(preferred, fallback *string) string {
	if preferred != nil {
		return *preferred
	}
	if fallback != nil {
		return *fallback
	}
	return ""
}

func (c Credential) MarshalJSON() ([]byte, error) {
	type credentialWire struct {
		OpenID             string          `json:"openid"`
		RefreshToken       string          `json:"refresh_token"`
		AccessToken        string          `json:"access_token"`
		Expired            int64           `json:"expired_at"`
		MusicID            json.RawMessage `json:"musicid"`
		MusicKey           string          `json:"musickey"`
		UnionID            string          `json:"unionid"`
		StrMusicID         string          `json:"str_musicid"`
		RefreshKey         string          `json:"refresh_key"`
		MusicKeyCreateTime int64           `json:"musickeyCreateTime"`
		KeyExpiresIn       int64           `json:"keyExpiresIn"`
		FirstLogin         int             `json:"first_login"`
		BindAccountType    int             `json:"bindAccountType"`
		NeedRefreshKeyIn   int             `json:"needRefreshKeyIn"`
		EncryptUIN         string          `json:"encryptUin"`
		LoginType          int             `json:"loginType"`
	}
	musicID := big.NewInt(0)
	if c.MusicID != "" {
		var ok bool
		musicID, ok = new(big.Int).SetString(c.MusicID, 10)
		if !ok {
			return nil, errors.New("qqmusic: invalid musicid")
		}
	}
	return json.Marshal(credentialWire{
		OpenID: c.OpenID, RefreshToken: c.RefreshToken, AccessToken: c.AccessToken,
		Expired: c.Expired, MusicID: json.RawMessage(musicID.String()), MusicKey: c.MusicKey, UnionID: c.UnionID,
		StrMusicID: c.StrMusicID, RefreshKey: c.RefreshKey, MusicKeyCreateTime: c.MusicKeyCreateTime,
		KeyExpiresIn: c.KeyExpiresIn, FirstLogin: c.FirstLogin, BindAccountType: c.BindAccountType,
		NeedRefreshKeyIn: c.NeedRefreshKeyIn, EncryptUIN: c.EncryptUIN, LoginType: c.LoginType,
	})
}

func (c Credential) ExpiredAt(now time.Time) bool {
	return now.Unix() >= c.MusicKeyCreateTime+c.KeyExpiresIn
}

func ValidateCredential(c Credential) error {
	musicID, ok := new(big.Int).SetString(c.MusicID, 10)
	if !ok || musicID.Sign() <= 0 || c.MusicKey == "" {
		return ErrCredentialInvalid
	}
	if c.StrMusicID != "" && c.StrMusicID != musicID.String() {
		return ErrCredentialInvalid
	}
	return nil
}

func safeCredentialError(_ error) error {
	return fmt.Errorf("%w: credential file invalid", ErrCredentialInvalid)
}

func credentialDirectory(file string, create bool) (*os.File, string, error) {
	directory, name := filepath.Dir(file), filepath.Base(file)
	if create {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, "", safeCredentialError(err)
		}
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("qqmusic: credential directory not found: %w", os.ErrNotExist)
	}
	if err != nil {
		return nil, "", safeCredentialError(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("%w: credential directory invalid", ErrCredentialInvalid)
	}
	stream, err := openCredentialDirectory(directory)
	if err != nil {
		return nil, "", safeCredentialError(err)
	}
	return stream, name, nil
}

func ReadCredential(file string) (Credential, error) {
	directory, name, err := credentialDirectory(file, false)
	if err != nil {
		return Credential{}, err
	}
	defer func() { _ = directory.Close() }()
	stream, err := openCredentialAt(directory, name, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, fmt.Errorf("qqmusic: credential file not found: %w", os.ErrNotExist)
	}
	if err != nil {
		return Credential{}, safeCredentialError(err)
	}
	defer func() { _ = stream.Close() }()
	info, err := stream.Stat()
	if err != nil || checkCredentialFile(info) != nil {
		return Credential{}, fmt.Errorf("%w: credential file permissions", ErrCredentialInvalid)
	}
	raw, err := io.ReadAll(io.LimitReader(stream, 65537))
	if err != nil || len(raw) > 65536 {
		return Credential{}, fmt.Errorf("%w: credential file size", ErrCredentialInvalid)
	}
	var credential Credential
	if err := json.Unmarshal(raw, &credential); err != nil || ValidateCredential(credential) != nil {
		return Credential{}, fmt.Errorf("%w: credential content", ErrCredentialInvalid)
	}
	credential.StrMusicID = credential.MusicID
	return credential, nil
}

func WriteCredential(file string, credential Credential) error {
	if err := ValidateCredential(credential); err != nil {
		return err
	}
	musicID, _ := new(big.Int).SetString(credential.MusicID, 10)
	credential.MusicID = musicID.String()
	credential.StrMusicID = credential.MusicID
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return safeCredentialError(err)
	}
	data = append(data, '\n')
	directory, name, err := credentialDirectory(file, true)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	if err := checkCredentialTarget(directory, name); err != nil {
		if errors.Is(err, ErrCredentialInvalid) {
			return fmt.Errorf("%w: credential target permissions", ErrCredentialInvalid)
		}
		return safeCredentialError(err)
	}
	temporaryName, err := credentialTemporaryName(name)
	if err != nil {
		return safeCredentialError(err)
	}
	stream, err := openCredentialAt(directory, temporaryName, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return safeCredentialError(err)
	}
	defer func() {
		_ = stream.Close()
		if temporaryName != "" {
			_ = removeCredentialAt(directory, temporaryName)
		}
	}()
	if err := stream.Chmod(0o600); err != nil {
		return safeCredentialError(err)
	}
	if _, err := stream.Write(data); err != nil {
		return safeCredentialError(err)
	}
	if err := stream.Sync(); err != nil {
		return safeCredentialError(err)
	}
	if err := stream.Close(); err != nil {
		return safeCredentialError(err)
	}
	if err := checkCredentialTarget(directory, name); err != nil {
		if errors.Is(err, ErrCredentialInvalid) {
			return fmt.Errorf("%w: credential target permissions", ErrCredentialInvalid)
		}
		return safeCredentialError(err)
	}
	if err := renameCredentialAt(directory, temporaryName, name); err != nil {
		return safeCredentialError(err)
	}
	temporaryName = ""
	syncCredentialDirectory(directory)
	return nil
}

func credentialTemporaryName(name string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "." + name + "." + hex.EncodeToString(random[:]), nil
}

// CredentialLock is an advisory non-blocking process lock.
type CredentialLock struct {
	file *os.File
}

func LockCredential(file string) (*CredentialLock, error) {
	directory, name, err := credentialDirectory(file, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	stream, err := openCredentialAt(directory, name+".lock", os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, safeCredentialError(err)
	}
	info, err := stream.Stat()
	if err != nil || checkCredentialFile(info) != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("%w: credential lock permissions", ErrCredentialInvalid)
	}
	if err := fileutil.Lock(stream); err != nil {
		_ = stream.Close()
		return nil, errors.New("qqmusic: credential update already in progress")
	}
	return &CredentialLock{file: stream}, nil
}

func (lock *CredentialLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}
