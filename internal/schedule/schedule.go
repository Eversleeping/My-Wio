package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid schedule")

// Spec is a small, dependency-free cron implementation for persistent tasks.
// It accepts the common five-field cron form and @every durations.
type Spec struct {
	Every   time.Duration
	Minute  fieldSet
	Hour    fieldSet
	Day     fieldSet
	Month   fieldSet
	Weekday fieldSet
}

type fieldSet struct {
	values map[int]bool
	any    bool
}

func Parse(expression string) (Spec, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Spec{}, fmt.Errorf("%w: schedule is empty", ErrInvalid)
	}
	if strings.HasPrefix(expression, "@every ") {
		duration, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(expression, "@every ")))
		if err != nil || duration < time.Minute {
			return Spec{}, fmt.Errorf("%w: @every must be at least one minute", ErrInvalid)
		}
		return Spec{Every: duration}, nil
	}
	switch strings.ToLower(expression) {
	case "@hourly":
		expression = "0 * * * *"
	case "@daily":
		expression = "0 0 * * *"
	case "@weekly":
		expression = "0 0 * * 0"
	case "@monthly":
		expression = "0 0 1 * *"
	}
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return Spec{}, fmt.Errorf("%w: cron expressions must have five fields", ErrInvalid)
	}
	minute, err := parseField(parts[0], 0, 59, false)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: minute: %v", ErrInvalid, err)
	}
	hour, err := parseField(parts[1], 0, 23, false)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: hour: %v", ErrInvalid, err)
	}
	day, err := parseField(parts[2], 1, 31, false)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: day: %v", ErrInvalid, err)
	}
	month, err := parseField(parts[3], 1, 12, false)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: month: %v", ErrInvalid, err)
	}
	weekday, err := parseField(parts[4], 0, 7, true)
	if err != nil {
		return Spec{}, fmt.Errorf("%w: weekday: %v", ErrInvalid, err)
	}
	return Spec{Minute: minute, Hour: hour, Day: day, Month: month, Weekday: weekday}, nil
}

func Next(expression, timezone string, after time.Time) (time.Time, error) {
	spec, err := Parse(expression)
	if err != nil {
		return time.Time{}, err
	}
	if spec.Every > 0 {
		return after.UTC().Add(spec.Every), nil
	}
	location := time.UTC
	if strings.TrimSpace(timezone) != "" {
		location, err = time.LoadLocation(strings.TrimSpace(timezone))
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: timezone: %v", ErrInvalid, err)
		}
	}
	candidate := after.In(location).Truncate(time.Minute).Add(time.Minute)
	// Five years is enough to find every valid Gregorian cron expression while
	// preventing malformed schedules from making the scheduler loop forever.
	for i := 0; i < 5*366*24*60; i++ {
		dayMatches := spec.Day.values[candidate.Day()]
		weekdayMatches := spec.Weekday.values[int(candidate.Weekday())]
		dayOfMonthAndWeekdayMatch := dayMatches && weekdayMatches
		if !spec.Day.any && !spec.Weekday.any {
			// Vixie cron semantics: when both fields are restricted, either
			// field may match. If one is unrestricted, the restricted field
			// remains an AND constraint with the rest of the expression.
			dayOfMonthAndWeekdayMatch = dayMatches || weekdayMatches
		}
		if spec.Minute.values[candidate.Minute()] && spec.Hour.values[candidate.Hour()] && spec.Month.values[int(candidate.Month())] && dayOfMonthAndWeekdayMatch {
			return candidate.UTC(), nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("%w: no matching time in the next five years", ErrInvalid)
}

func parseField(raw string, minimum, maximum int, sundaySeven bool) (fieldSet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fieldSet{}, errors.New("field is empty")
	}
	values := make(map[int]bool)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return fieldSet{}, errors.New("empty list item")
		}
		base, step := item, 1
		if strings.Count(item, "/") > 1 {
			return fieldSet{}, errors.New("multiple step separators")
		}
		if strings.Contains(item, "/") {
			parts := strings.SplitN(item, "/", 2)
			base = parts[0]
			parsedStep, err := strconv.Atoi(parts[1])
			if err != nil || parsedStep <= 0 {
				return fieldSet{}, errors.New("step must be positive")
			}
			step = parsedStep
		}
		start, end := minimum, maximum
		if base != "*" && base != "?" {
			if strings.Count(base, "-") > 1 {
				return fieldSet{}, errors.New("multiple range separators")
			}
			if strings.Contains(base, "-") {
				parts := strings.SplitN(base, "-", 2)
				var err error
				start, err = strconv.Atoi(parts[0])
				if err != nil {
					return fieldSet{}, errors.New("range start is invalid")
				}
				end, err = strconv.Atoi(parts[1])
				if err != nil {
					return fieldSet{}, errors.New("range end is invalid")
				}
			} else {
				var err error
				start, err = strconv.Atoi(base)
				if err != nil {
					return fieldSet{}, errors.New("value is invalid")
				}
				end = start
			}
		}
		if start < minimum || end > maximum || start > end {
			return fieldSet{}, fmt.Errorf("range %d-%d is outside %d-%d", start, end, minimum, maximum)
		}
		for value := start; value <= end; value += step {
			if sundaySeven && value == 7 {
				values[0] = true
			} else {
				values[value] = true
			}
		}
	}
	if len(values) == 0 {
		return fieldSet{}, errors.New("field has no values")
	}
	return fieldSet{values: values, any: raw == "*" || raw == "?"}, nil
}
