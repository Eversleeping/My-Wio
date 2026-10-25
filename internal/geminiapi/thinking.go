package geminiapi

import "strings"

// PresetThinkingLevel describes an upstream model ID's explicit level suffix.
// It is a model preset, not a value reported by Gemini's inference response.
func PresetThinkingLevel(model string) string {
	model = strings.ToLower(model)
	for _, level := range []string{"minimal", "low", "medium", "high"} {
		if strings.HasSuffix(model, "-"+level) {
			return level
		}
	}
	return ""
}

func ValidThinkingLevel(model, level string) bool {
	if level == "" {
		return true
	}
	if level != "minimal" && level != "low" && level != "medium" && level != "high" {
		return false
	}
	if preset := PresetThinkingLevel(model); preset != "" {
		return level == preset
	}
	model = strings.ToLower(model)
	// Gemini 2.5 uses thinkingBudget, not thinkingLevel. Auto aliases cannot
	// guarantee a model family, and image models are outside this CLI control.
	if model == "auto" || strings.HasPrefix(model, "gemini-2.") || strings.Contains(model, "-image") {
		return false
	}
	if strings.HasPrefix(model, "gemini-3-pro") && level != "low" && level != "high" {
		return false
	}
	return true
}
