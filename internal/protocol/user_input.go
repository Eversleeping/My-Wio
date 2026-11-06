package protocol

import (
	"encoding/json"
	"errors"
	"strings"
)

const UserInputRequestMethod = "item/tool/requestUserInput"

type UserInputAnswer struct {
	Answers []string `json:"answers"`
}
type UserInputQuestion struct {
	MultiSelect bool   `json:"multiSelect,omitempty"`
	ID          string `json:"id"`
	Header      string `json:"header"`
	Question    string `json:"question"`
	IsOther     bool   `json:"isOther"`
	IsSecret    bool   `json:"isSecret"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

func ValidateUserInputAnswers(detail json.RawMessage, answers map[string]UserInputAnswer) error {
	var request struct {
		Questions []UserInputQuestion `json:"questions"`
	}
	if json.Unmarshal(detail, &request) != nil || len(request.Questions) == 0 || len(answers) != len(request.Questions) {
		return errors.New("answer every requested question")
	}
	for _, question := range request.Questions {
		answer, ok := answers[question.ID]
		if !ok || len(answer.Answers) == 0 || len(answer.Answers) > 13 || (!question.MultiSelect && len(answer.Answers) != 1) {
			return errors.New("answer every question; only multi-select questions accept multiple answers")
		}
		seen := map[string]bool{}
		for _, value := range answer.Answers {
			if strings.TrimSpace(value) == "" || len(value) > 16384 || seen[value] {
				return errors.New("invalid or duplicate answer")
			}
			seen[value] = true
			if len(question.Options) > 0 && !question.IsOther {
				valid := false
				for _, option := range question.Options {
					if option.Label == value {
						valid = true
					}
				}
				if !valid {
					return errors.New("select one of the offered options")
				}
			}
		}
	}
	return nil
}
