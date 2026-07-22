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
	"strings"

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

var secretKeys = []string{"token", "secret", "password", "authorization", "private_key", "api_key"}

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
			lower := strings.ToLower(key)
			hidden := false
			for _, needle := range secretKeys {
				if strings.Contains(lower, needle) {
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
