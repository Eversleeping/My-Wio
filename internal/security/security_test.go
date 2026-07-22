package security

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password was rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("invalid password was accepted")
	}
}

func TestVaultRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	vault, err := NewVault(key)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]string{"TOKEN": "secret", "REGION": "local"}
	ciphertext, err := vault.Encrypt(original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, "secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	var decoded map[string]string
	if err := vault.Decrypt(ciphertext, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["TOKEN"] != original["TOKEN"] || decoded["REGION"] != original["REGION"] {
		t.Fatalf("unexpected round trip: %#v", decoded)
	}
}

func TestRedactJSON(t *testing.T) {
	raw := []byte(`{"token":"abc","nested":{"password":"def","name":"safe"}}`)
	redacted := RedactJSON(raw)
	var value map[string]any
	if err := json.Unmarshal(redacted, &value); err != nil {
		t.Fatal(err)
	}
	if value["token"] != "[REDACTED]" {
		t.Fatalf("token was not redacted: %s", redacted)
	}
	nested := value["nested"].(map[string]any)
	if nested["password"] != "[REDACTED]" || nested["name"] != "safe" {
		t.Fatalf("unexpected nested redaction: %s", redacted)
	}
}

func TestRecoveryCodesAreHashed(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 3 || len(hashes) != 3 {
		t.Fatalf("unexpected code count: %d/%d", len(codes), len(hashes))
	}
	if hashes[0] != HashRecovery(codes[0]) || strings.Contains(hashes[0], codes[0]) {
		t.Fatal("recovery code hash mismatch")
	}
	for _, code := range codes {
		if len(code) != 13 || code[6] != '-' || strings.Count(code, "-") != 1 {
			t.Fatalf("unexpected recovery code format: %q", code)
		}
	}
}
