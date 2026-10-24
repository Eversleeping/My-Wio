// Package geminiapi shares Gemini native API endpoint handling.
package geminiapi

import (
	"net/url"
	"strings"
)

// BaseURL removes a trailing API version from an already validated endpoint.
// Gemini CLI's SDK appends /v1beta itself; keeping a copied /v1 or /v1beta
// suffix would produce an invalid /v1/v1beta or /v1beta/v1beta request path.
// Gateway prefixes before the version are preserved.
func BaseURL(endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	path := u.EscapedPath()
	for _, suffix := range []string{"/v1beta", "/v1"} {
		if strings.HasSuffix(path, suffix) {
			u.RawPath = strings.TrimSuffix(path, suffix)
			u.Path, _ = url.PathUnescape(u.RawPath)
			return u.String()
		}
	}
	return base
}
