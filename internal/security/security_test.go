package security

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
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

func TestRedactJSONClaudeCacheCounters(t *testing.T) {
	for _, key := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		for _, value := range []string{"0", "135", `"secret"`, `{"api_key":"secret"}`, "-1", "1.5", "null", "true"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				raw := []byte(`{"usage":{"` + key + `":` + value + `},"cache_read_input_tokens_secret":123}`)
				var got struct {
					Usage  map[string]json.RawMessage `json:"usage"`
					Secret string                     `json:"cache_read_input_tokens_secret"`
				}
				if err := json.Unmarshal(RedactJSON(raw), &got); err != nil {
					t.Fatal(err)
				}
				want := `"[REDACTED]"`
				if value == "0" || value == "135" {
					want = value
				}
				if string(got.Usage[key]) != want || got.Secret != "[REDACTED]" {
					t.Fatalf("unexpected redaction: %s", RedactJSON(raw))
				}
			})
		}
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

func TestRedactJSONPreservesOnlyNumericUsageCounters(t *testing.T) {
	raw := []byte(`{"stats":{"input_tokens":120,"output_tokens":0,"total_tokens":120,"cached_tokens":10,"models":{"gemini":{"input_tokens":120,"output_tokens":0}},"api_key":"secret"},"access_token":123,"token":"secret","nested":[{"input_tokens":"secret","output_tokens":{"api_key":"secret"},"total_tokens":-1}]}`)
	var value map[string]any
	redacted := RedactJSON(raw)
	if err := json.Unmarshal(redacted, &value); err != nil {
		t.Fatal(err)
	}
	stats := value["stats"].(map[string]any)
	if stats["input_tokens"] != float64(120) || stats["output_tokens"] != float64(0) || stats["total_tokens"] != float64(120) || stats["cached_tokens"] != float64(10) || stats["api_key"] != "[REDACTED]" {
		t.Fatalf("incorrect usage redaction: %s", redacted)
	}
	model := stats["models"].(map[string]any)["gemini"].(map[string]any)
	if model["input_tokens"] != float64(120) || model["output_tokens"] != float64(0) {
		t.Fatalf("model usage lost: %s", redacted)
	}
	for _, key := range []string{"access_token", "token"} {
		if value[key] != "[REDACTED]" {
			t.Fatalf("credential %s exposed", key)
		}
	}
	for _, field := range value["nested"].([]any)[0].(map[string]any) {
		if field != "[REDACTED]" {
			t.Fatalf("unsafe counter value exposed: %s", redacted)
		}
	}
}

func TestRedactJSONCatchesSecretKeySpellings(t *testing.T) {
	for _, key := range []string{
		"apiKey", "api_key", "API-KEY", "x-api-key", "X_Api_Key", "privateKey", "private_key", "private-key",
		"accessKey", "access_key", "secretKey", "clientSecret", "client_secret", "credential", "credentials", "gitCredentials",
		"cookie", "Set-Cookie", "authorization", "Proxy-Authorization", "password", "userPassword", "passwd", "token", "accessToken", "access-token", "refresh_token", "idToken",
	} {
		for _, value := range []string{`"secret-value"`, `123`, `{"nested":"secret-value"}`, `["secret-value"]`, `true`} {
			raw := []byte(`{"outer":{"` + key + `":` + value + `,"name":"safe"}}`)
			redacted := string(RedactJSON(raw))
			if strings.Contains(redacted, "secret-value") || !strings.Contains(redacted, `"`+key+`":"[REDACTED]"`) || !strings.Contains(redacted, `"name":"safe"`) {
				t.Errorf("%s with %s was not redacted: %s", key, value, redacted)
			}
		}
	}
}

func TestRedactJSONKeepsUsageCounters(t *testing.T) {
	for _, key := range []string{
		"input_tokens", "output_tokens", "total_tokens", "cached_tokens", "cached_input_tokens", "reasoning_output_tokens",
		"cache_creation_input_tokens", "cache_read_input_tokens", "pre_tokens", "post_tokens", "tokens_used", "token_budget",
		"inputTokens", "outputTokens", "totalTokens", "cachedInputTokens", "reasoningOutputTokens", "tokensUsed", "tokenBudget",
		"promptTokenCount", "candidatesTokenCount", "totalTokenCount", "max_output_tokens", "ephemeral_5m_input_tokens",
	} {
		raw := []byte(`{"usage":{"` + key + `":42}}`)
		var got struct {
			Usage map[string]float64 `json:"usage"`
		}
		if err := json.Unmarshal(RedactJSON(raw), &got); err != nil || got.Usage[key] != 42 {
			t.Errorf("%s was over-redacted: %s (%v)", key, RedactJSON(raw), err)
		}
	}
}

func TestRedactJSONWalksTokenUsageContainers(t *testing.T) {
	raw := []byte(`{"tokenUsage":{"total":{"inputTokens":10,"outputTokens":2,"cachedInputTokens":1,"reasoningOutputTokens":0,"totalTokens":12},"modelContextWindow":200000,"apiKey":"hidden"},"token_usage":{"input_tokens":5},"total_token_usage":{"output_tokens":7},"token_count":{"input_tokens":3},"tokenUsageNote":"hidden"}`)
	var got struct {
		TokenUsage struct {
			Total struct {
				Input     float64 `json:"inputTokens"`
				Output    float64 `json:"outputTokens"`
				Cached    float64 `json:"cachedInputTokens"`
				Reasoning float64 `json:"reasoningOutputTokens"`
				Total     float64 `json:"totalTokens"`
			} `json:"total"`
			Window float64 `json:"modelContextWindow"`
			APIKey string  `json:"apiKey"`
		} `json:"tokenUsage"`
		Snake struct {
			Input float64 `json:"input_tokens"`
		} `json:"token_usage"`
		Total struct {
			Output float64 `json:"output_tokens"`
		} `json:"total_token_usage"`
		Count struct {
			Input float64 `json:"input_tokens"`
		} `json:"token_count"`
		Note string `json:"tokenUsageNote"`
	}
	if err := json.Unmarshal(RedactJSON(raw), &got); err != nil {
		t.Fatal(err)
	}
	usage := got.TokenUsage
	if usage.Total.Input != 10 || usage.Total.Output != 2 || usage.Total.Cached != 1 || usage.Total.Total != 12 || usage.Window != 200000 || got.Snake.Input != 5 || got.Total.Output != 7 || got.Count.Input != 3 {
		t.Fatalf("usage containers were over-redacted: %s", RedactJSON(raw))
	}
	if usage.APIKey != "[REDACTED]" || got.Note != "[REDACTED]" {
		t.Fatalf("secret inside usage container exposed: %s", RedactJSON(raw))
	}
	for _, value := range []string{`"abc"`, `-1`, `1.5`, `true`, `null`} {
		for _, key := range []string{"tokenUsage", "token_count", "inputTokens", "tokenBudget"} {
			redacted := string(RedactJSON([]byte(`{"` + key + `":` + value + `}`)))
			if !strings.Contains(redacted, `"`+key+`":"[REDACTED]"`) {
				t.Errorf("%s=%s should be redacted: %s", key, value, redacted)
			}
		}
	}
}

func TestMatchTOTPAllowsOneStepOfSkewAndReturnsCounter(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	at := time.Unix(1_700_000_010, 0).UTC()
	current := at.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		code, err := totp.GenerateCode(secret, time.Unix((current+offset)*30, 0))
		if err != nil {
			t.Fatal(err)
		}
		counter, ok := MatchTOTP(code, secret, at)
		if !ok || counter != current+offset {
			t.Fatalf("offset %d: counter=%d ok=%v", offset, counter, ok)
		}
	}
	for _, offset := range []int64{-2, 2} {
		code, err := totp.GenerateCode(secret, time.Unix((current+offset)*30, 0))
		if err != nil {
			t.Fatal(err)
		}
		if counter, ok := MatchTOTP(code, secret, at); ok {
			t.Fatalf("offset %d was accepted as counter %d", offset, counter)
		}
	}
	for _, code := range []string{"", "12345", "not-a-code"} {
		if _, ok := MatchTOTP(code, secret, at); ok {
			t.Fatalf("invalid code %q was accepted", code)
		}
	}
	if _, ok := MatchTOTP("123456", "not base32!", at); ok {
		t.Fatal("invalid secret was accepted")
	}
}
