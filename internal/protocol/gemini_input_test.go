package protocol

import (
	"strings"
	"testing"
)

func TestGeminiImageValidation(t *testing.T) {
	image := TurnImage{DataURL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="}
	if err := ValidateGeminiImages([]TurnImage{image}); err != nil {
		t.Fatal(err)
	}
	for _, images := range [][]TurnImage{
		{image, image, image, image, image},
		{{DataURL: strings.Replace(image.DataURL, "image/png", "image/jpeg", 1)}},
		{{DataURL: "data:image/png;base64,YmFk"}},
		{{DataURL: "data:image/png;base64,"}},
		{{DataURL: "data:image/png;base64," + strings.Repeat("A", (2<<20)*4/3+5)}},
	} {
		if ValidateGeminiImages(images) == nil {
			t.Fatal("accepted invalid images")
		}
	}
}

func TestGeminiSkillManagementValidation(t *testing.T) {
	base := GeminiSkillManageCommand{Action: "install", Source: "https://github.com/example/skills.git", Scope: "workspace"}
	if err := ValidateGeminiSkillManagement(base); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"https://user:secret@example.com/a.git", "https://example.com/a.git?token=secret", "http://example.com/a.git", "file:///outside", "-unsafe", "bad\nsource"} {
		command := base
		command.Source = source
		if ValidateGeminiSkillManagement(command) == nil {
			t.Fatalf("accepted source %q", source)
		}
	}
	for _, path := range []string{"../escape", "/absolute", "a/../../escape", `a\..\escape`, "C:/outside"} {
		command := base
		command.Path = path
		if ValidateGeminiSkillManagement(command) == nil {
			t.Fatalf("accepted subdirectory %q", path)
		}
	}
	for _, name := range []string{"-flag", "../other", "two words", "x;command"} {
		if ValidateGeminiSkillManagement(GeminiSkillManageCommand{Action: "uninstall", Name: name, Scope: "user"}) == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
}
