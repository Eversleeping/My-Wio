package httpapi

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/wio-platform/wio/internal/protocol"
)

func validReasoningEffort(value string) bool {
	switch value {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func validTurnImages(images []protocol.TurnImage) bool {
	if len(images) > 4 {
		return false
	}
	total := 0
	for _, image := range images {
		prefix, encoded, ok := strings.Cut(image.DataURL, ",")
		if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || len(decoded) > 2<<20 {
			return false
		}
		if http.DetectContentType(decoded) != strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64") {
			return false
		}
		total += len(decoded)
	}
	return total <= 4<<20
}
