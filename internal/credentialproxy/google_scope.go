package credentialproxy

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
)

func (b *Lease) bindGoogleProject(source io.Reader, route string) ([]byte, error) {
	decoder := json.NewDecoder(source)
	decoder.UseNumber()
	var body map[string]any
	if decoder.Decode(&body) != nil || body == nil {
		return nil, errors.New("invalid project payload")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("trailing project payload")
	}
	normalized := func(key string) string { return strings.ToLower(strings.ReplaceAll(key, "_", "")) }
	bind := func(payload map[string]any, key string, required bool) error {
		for field := range payload {
			if normalized(field) == normalized(key) && field != key {
				return errors.New("ambiguous project field")
			}
			if key == "project" && (normalized(field) == "projectid" || normalized(field) == "projectname") {
				return errors.New("unsupported project alias")
			}
		}
		if value, present := payload[key]; present && value != nil && value != "" {
			if project, ok := value.(string); !ok || project != b.config.Project {
				return errors.New("foreign project")
			}
		}
		if required {
			payload[key] = b.config.Project
		}
		return nil
	}
	load := route == "/v1internal:loadCodeAssist"
	model := func(payload map[string]any) error {
		for key, value := range payload {
			if normalized(key) == "modelid" || normalized(key) == "modelname" {
				return errors.New("unsupported model alias")
			}
			if normalized(key) == "model" {
				name, ok := value.(string)
				if key != "model" || !ok || !regexp.MustCompile(`^(models/)?[A-Za-z0-9_.-]+$`).MatchString(name) {
					return errors.New("unsupported model resource")
				}
			}
		}
		return nil
	}
	if err := model(body); err != nil {
		return nil, err
	}
	if err := bind(body, "cloudaicompanionProject", load); err != nil {
		return nil, err
	}
	if err := bind(body, "project", !load && route != "/v1internal:countTokens"); err != nil {
		return nil, err
	}
	for key := range body {
		if (normalized(key) == "metadata" && key != "metadata") || (normalized(key) == "request" && key != "request") {
			return nil, errors.New("ambiguous native structure")
		}
	}
	if value, present := body["metadata"]; present && value != nil {
		metadata, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid project metadata")
		}
		if err := bind(metadata, "duetProject", load); err != nil {
			return nil, err
		}
	}
	if value, present := body["request"]; present && value != nil {
		request, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("invalid native inference payload")
		}
		if err := model(request); err != nil {
			return nil, err
		}
		if err := bind(request, "project", false); err != nil {
			return nil, err
		}
		for key, value := range request {
			if normalized(key) == "cachedcontent" {
				if key != "cachedContent" {
					return nil, errors.New("ambiguous cached content")
				}
				if value != nil && value != "" {
					name, ok := value.(string)
					decoded, decodeErr := url.PathUnescape(name)
					if !ok || decodeErr != nil || decoded != name || path.Clean(name) != name || !strings.HasPrefix(name, "projects/"+b.config.Project+"/locations/") {
						return nil, errors.New("foreign cached content")
					}
				}
			}
		}
	}
	return json.Marshal(body)
}
