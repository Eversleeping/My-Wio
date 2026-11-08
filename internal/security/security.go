package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"golang.org/x/crypto/argon2"
)

const passwordParams = "m=65536,t=3,p=2"

func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$%s$%s$%s", passwordParams, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	expected, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return len(actual) == len(expected) && subtle.ConstantTimeCompare(actual, expected) == 1
}

func HashRecovery(code string) string {
	hash := argon2.IDKey([]byte(strings.ToUpper(strings.TrimSpace(code))), []byte("wio-recovery-v1"), 2, 16*1024, 1, 32)
	return hex.EncodeToString(hash)
}

func GenerateRecoveryCodes(count int) ([]string, []string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codes := make([]string, 0, count)
	hashes := make([]string, 0, count)
	for i := 0; i < count; i++ {
		random := make([]byte, 12)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return nil, nil, err
		}
		plain := make([]byte, len(random))
		for index, value := range random {
			plain[index] = alphabet[int(value)&31]
		}
		code := string(plain[:6]) + "-" + string(plain[6:])
		codes = append(codes, code)
		hashes = append(hashes, HashRecovery(code))
	}
	return codes, hashes, nil
}

const (
	totpPeriodSeconds = 30
	totpSkewSteps     = 1
)

// MatchTOTP returns the time-step counter a six-digit TOTP code belongs to,
// allowing one step of clock skew either way. Callers must persist the counter
// and reject values that are not newer, because a code otherwise stays valid
// for its whole window. If a random collision matches several steps the newest
// wins, so a replay cannot be accepted under an older step.
func MatchTOTP(code, secret string, at time.Time) (int64, bool) {
	current := at.Unix() / totpPeriodSeconds
	options := hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}
	for counter := current + totpSkewSteps; counter >= current-totpSkewSteps; counter-- {
		if valid, err := hotp.ValidateCustom(code, uint64(counter), secret, options); err == nil && valid {
			return counter, true
		}
	}
	return 0, false
}

type Vault struct{ key []byte }

func NewVault(encoded string) (*Vault, error) {
	if encoded == "" {
		return nil, errors.New("WIO_MASTER_KEY is required")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("WIO_MASTER_KEY must be a base64-encoded 32-byte key")
	}
	return &Vault{key: key}, nil
}

func DevVault() *Vault {
	key := sha256.Sum256([]byte("wio-development-vault-key"))
	return &Vault{key: key[:]}
}

func (v *Vault) Encrypt(value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, plain, []byte("wio-vault-v1"))
	combined := append(nonce, ciphertext...)
	return "v1:" + base64.RawStdEncoding.EncodeToString(combined), nil
}

func (v *Vault) Decrypt(encoded string, out any) error {
	if !strings.HasPrefix(encoded, "v1:") {
		return errors.New("unsupported ciphertext version")
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(data) < aead.NonceSize() {
		return errors.New("invalid ciphertext")
	}
	nonce, ciphertext := data[:aead.NonceSize()], data[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("wio-vault-v1"))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, out)
}

// secretKeys are matched as substrings of normalized JSON keys, so snake_case,
// camelCase and header spellings (api_key, apiKey, x-api-key) all hit one entry.
// "secret" also covers secretKey and clientSecret; "credential" covers the plural.
var secretKeys = []string{"token", "secret", "password", "passwd", "authorization", "credential", "cookie", "apikey", "privatekey", "accesskey"}

// usageCounterSuffixes name token-usage counters. Their keys also contain
// "token", but the UI and usage accounting read them; only nonnegative
// integers are kept, so a string under such a key is still redacted.
var usageCounterSuffixes = []string{"tokens", "tokencount", "tokensused", "tokenbudget"}

func normalizeSecretKey(key string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
}

func isUsageCounter(normalized string, value any) bool {
	count, ok := value.(float64)
	if !ok || count < 0 || math.Trunc(count) != count {
		return false
	}
	for _, suffix := range usageCounterSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

// isUsageContainer matches objects that group usage counters. They are walked
// rather than replaced, so anything secret-looking inside them is still hidden.
func isUsageContainer(normalized string, value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return strings.HasSuffix(normalized, "tokenusage") || normalized == "tokencount"
	default:
		return false
	}
}

func RedactJSON(raw []byte) []byte {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	redactValue(value)
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}

func redactValue(v any) {
	switch x := v.(type) {
	case map[string]any:
		for key, val := range x {
			normalized := normalizeSecretKey(key)
			if isUsageCounter(normalized, val) {
				continue
			}
			if isUsageContainer(normalized, val) {
				redactValue(val)
				continue
			}
			hidden := false
			for _, needle := range secretKeys {
				if strings.Contains(normalized, needle) {
					hidden = true
					break
				}
			}
			if hidden {
				x[key] = "[REDACTED]"
			} else {
				redactValue(val)
			}
		}
	case []any:
		for _, item := range x {
			redactValue(item)
		}
	}
}
