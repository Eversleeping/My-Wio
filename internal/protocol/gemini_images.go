package protocol

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// ValidateGeminiImages applies the same limits to queued and live image input.
func ValidateGeminiImages(images []TurnImage) error {
	if len(images) > 4 {
		return errors.New("at most four Gemini images are allowed")
	}
	total := 0
	for _, image := range images {
		prefix, data, ok := strings.Cut(image.DataURL, ",")
		mime := strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
		if !ok || len(data) > (2<<20)*4/3+4 || prefix != "data:"+mime+";base64" || (mime != "image/png" && mime != "image/jpeg" && mime != "image/webp") {
			return errors.New("invalid Gemini image")
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		total += len(raw)
		if err != nil || len(raw) == 0 || len(raw) > 2<<20 || total > 4<<20 || http.DetectContentType(raw) != mime {
			return errors.New("invalid Gemini image")
		}
	}
	return nil
}
