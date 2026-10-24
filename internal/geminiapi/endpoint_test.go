package geminiapi

import "testing"

func TestBaseURLPreservesGatewaysAndRemovesOnlyVersionSuffix(t *testing.T) {
	for input, want := range map[string]string{
		"https://v1":     "https://v1",
		"https://v1beta": "https://v1beta",
		"https://example.test/gateway%2Fprefix/v1": "https://example.test/gateway%2Fprefix",
		"https://example.test/gateway%2Fv1":        "https://example.test/gateway%2Fv1",
		"https://example.test":                     "https://example.test",
		" https://example.test/ ":                  "https://example.test",
		"https://example.test/v1/":                 "https://example.test",
		"https://example.test/v1beta":              "https://example.test",
		"https://example.test/gateway/v1":          "https://example.test/gateway",
		"https://example.test/gateway/v1beta/":     "https://example.test/gateway",
		"https://example.test/gateway/v1custom":    "https://example.test/gateway/v1custom",
	} {
		if got := BaseURL(input); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}
