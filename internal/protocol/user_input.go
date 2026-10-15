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
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	IsOther  bool   `json:"isOther"`
	IsSecret bool   `json:"isSecret"`
	Options  []struct {
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
		if !ok || len(answer.Answers) != 1 || strings.TrimSpace(answer.Answers[0]) == "" || len(answer.Answers[0]) > 16384 {
			return errors.New("each question requires one non-empty answer of at most 16384 bytes")
		}
		if len(question.Options) > 0 && !question.IsOther {
			valid := false
			for _, option := range question.Options {
				if option.Label == answer.Answers[0] {
					valid = true
				}
			}
			if !valid {
				return errors.New("select one of the offered options")
			}
		}
	}
	return nil
}
