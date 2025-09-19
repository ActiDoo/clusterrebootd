package windows

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Evaluator interface {
	Evaluate(time.Time) Decision
}

type Decision struct {
	Allowed         bool
	AllowConfigured bool
	MatchedAllow    *Match
	MatchedDeny     *Match
}

type Match struct {
	Expression string
}

type evaluator struct {
	allow []interval
	deny  []interval
}

type interval struct {
	start int
	end   int
	expr  string
}

const (
	secondsPerMinute = 60
	minutesPerHour   = 60
	hoursPerDay      = 24
	secondsPerDay    = hoursPerDay * minutesPerHour * secondsPerMinute
	secondsPerWeek   = secondsPerDay * 7
)

// NewEvaluator parses allow/deny expressions into an evaluator. A nil evaluator is returned
// when both slices are empty.
func NewEvaluator(allowExprs, denyExprs []string) (Evaluator, error) {
	eval := &evaluator{}

	for idx, expr := range denyExprs {
		trimmed := strings.TrimSpace(expr)
		if trimmed == "" {
			return nil, fmt.Errorf("windows.deny[%d]: expression must not be empty", idx)
		}
		intervals, err := parseExpression(trimmed)
		if err != nil {
			return nil, fmt.Errorf("windows.deny[%d]: %w", idx, err)
		}
		for _, in := range intervals {
			in.expr = trimmed
			eval.deny = append(eval.deny, in)
		}
	}

	for idx, expr := range allowExprs {
		trimmed := strings.TrimSpace(expr)
		if trimmed == "" {
			return nil, fmt.Errorf("windows.allow[%d]: expression must not be empty", idx)
		}
		intervals, err := parseExpression(trimmed)
		if err != nil {
			return nil, fmt.Errorf("windows.allow[%d]: %w", idx, err)
		}
		for _, in := range intervals {
			in.expr = trimmed
			eval.allow = append(eval.allow, in)
		}
	}

	if len(eval.allow) == 0 && len(eval.deny) == 0 {
		return nil, nil
	}

	return eval, nil
}

func (e *evaluator) Evaluate(t time.Time) Decision {
	seconds := secondsSinceWeekStart(t)
	decision := Decision{Allowed: true, AllowConfigured: len(e.allow) > 0}

	for _, in := range e.deny {
		if in.contains(seconds) {
			decision.Allowed = false
			decision.MatchedDeny = &Match{Expression: in.expr}
			return decision
		}
	}

	if decision.AllowConfigured {
		decision.Allowed = false
		for _, in := range e.allow {
			if in.contains(seconds) {
				decision.Allowed = true
				decision.MatchedAllow = &Match{Expression: in.expr}
				return decision
			}
		}
	}

	return decision
}

func (in interval) contains(seconds int) bool {
	return seconds >= in.start && seconds < in.end
}

func secondsSinceWeekStart(t time.Time) int {
	daySeconds := int(t.Weekday()) * secondsPerDay
	hourSeconds := t.Hour() * minutesPerHour * secondsPerMinute
	minuteSeconds := t.Minute() * secondsPerMinute
	return daySeconds + hourSeconds + minuteSeconds + t.Second()
}

func parseExpression(expr string) ([]interval, error) {
	colonIdx := strings.Index(expr, ":")
	if colonIdx == -1 {
		return nil, fmt.Errorf("missing time component in %q", expr)
	}
	dashIdx := strings.Index(expr[colonIdx:], "-")
	if dashIdx == -1 {
		return nil, fmt.Errorf("missing '-' in time range %q", expr)
	}
	dashIdx += colonIdx
	startPart := strings.TrimSpace(expr[:dashIdx])
	endPart := strings.TrimSpace(expr[dashIdx+1:])
	if startPart == "" || endPart == "" {
		return nil, fmt.Errorf("invalid window expression %q", expr)
	}

	startDays, startSeconds, err := parseDayAndTime(startPart)
	if err != nil {
		return nil, err
	}
	endDays, hasEndDay, endSeconds, err := parseEndPart(endPart, startDays)
	if err != nil {
		return nil, err
	}

	if hasEndDay {
		if len(startDays) != 1 {
			return nil, fmt.Errorf("window %q specifies an end day but start matches multiple days", expr)
		}
		if len(endDays) != 1 {
			return nil, fmt.Errorf("window %q end day must resolve to a single day", expr)
		}
		start := dayOffset(startDays[0]) + startSeconds
		end := dayOffset(endDays[0]) + endSeconds
		for end <= start {
			end += secondsPerWeek
		}
		if end > secondsPerWeek {
			return []interval{
				{start: start, end: secondsPerWeek},
				{start: 0, end: end - secondsPerWeek},
			}, nil
		}
		return []interval{{start: start, end: end}}, nil
	}

	intervals := make([]interval, 0, len(startDays))
	for _, day := range startDays {
		start := dayOffset(day) + startSeconds
		end := dayOffset(day) + endSeconds
		if end <= start {
			end += secondsPerDay
		}
		if end > secondsPerWeek {
			intervals = append(intervals,
				interval{start: start, end: secondsPerWeek},
				interval{start: 0, end: end - secondsPerWeek},
			)
		} else {
			intervals = append(intervals, interval{start: start, end: end})
		}
	}
	return intervals, nil
}

func dayOffset(day time.Weekday) int {
	return int(day) * secondsPerDay
}

func parseDayAndTime(part string) ([]time.Weekday, int, error) {
	tokens := strings.Fields(part)
	if len(tokens) == 0 {
		return nil, 0, fmt.Errorf("missing day/time in %q", part)
	}
	timeToken := tokens[len(tokens)-1]
	seconds, err := parseTimeOfDay(timeToken)
	if err != nil {
		return nil, 0, err
	}
	daySpec := "*"
	if len(tokens) > 1 {
		daySpec = strings.Join(tokens[:len(tokens)-1], " ")
	}
	days, err := parseDaySpec(daySpec)
	if err != nil {
		return nil, 0, err
	}
	return days, seconds, nil
}

func parseEndPart(part string, defaultDays []time.Weekday) ([]time.Weekday, bool, int, error) {
	tokens := strings.Fields(part)
	if len(tokens) == 0 {
		return nil, false, 0, fmt.Errorf("missing end time in %q", part)
	}
	timeToken := tokens[len(tokens)-1]
	seconds, err := parseTimeOfDay(timeToken)
	if err != nil {
		return nil, false, 0, err
	}
	if len(tokens) == 1 {
		return defaultDays, false, seconds, nil
	}
	daySpec := strings.Join(tokens[:len(tokens)-1], " ")
	days, err := parseDaySpec(daySpec)
	if err != nil {
		return nil, false, 0, err
	}
	return days, true, seconds, nil
}

func parseDaySpec(spec string) ([]time.Weekday, error) {
	trimmed := strings.ToLower(strings.TrimSpace(spec))
	if trimmed == "" {
		return nil, fmt.Errorf("day specification must not be empty")
	}
	if trimmed == "*" {
		return []time.Weekday{
			time.Sunday,
			time.Monday,
			time.Tuesday,
			time.Wednesday,
			time.Thursday,
			time.Friday,
			time.Saturday,
		}, nil
	}
	parts := strings.Split(trimmed, ",")
	days := make([]time.Weekday, 0, len(parts))
	seen := make(map[time.Weekday]struct{})
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid day specification %q", spec)
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid day range %q", part)
			}
			startDay, ok := dayName(bounds[0])
			if !ok {
				return nil, fmt.Errorf("unknown day %q in %q", bounds[0], spec)
			}
			endDay, ok := dayName(bounds[1])
			if !ok {
				return nil, fmt.Errorf("unknown day %q in %q", bounds[1], spec)
			}
			for _, day := range expandDayRange(startDay, endDay) {
				if _, exists := seen[day]; !exists {
					days = append(days, day)
					seen[day] = struct{}{}
				}
			}
			continue
		}
		day, ok := dayName(part)
		if !ok {
			return nil, fmt.Errorf("unknown day %q in %q", part, spec)
		}
		if _, exists := seen[day]; !exists {
			days = append(days, day)
			seen[day] = struct{}{}
		}
	}
	if len(days) == 0 {
		return nil, fmt.Errorf("day specification %q resolved to no days", spec)
	}
	return days, nil
}

func dayName(value string) (time.Weekday, bool) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "weds", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	}
	return time.Sunday, false
}

func expandDayRange(start, end time.Weekday) []time.Weekday {
	if start == end {
		return []time.Weekday{start}
	}
	days := make([]time.Weekday, 0, 7)
	current := start
	for {
		days = append(days, current)
		if current == end {
			break
		}
		current = (current + 1) % 7
	}
	return days
}

func parseTimeOfDay(value string) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid time %q (expected HH:MM)", value)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid hour in %q", value)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid minute in %q", value)
	}
	if hour < 0 || hour > 23 {
		return 0, fmt.Errorf("hour out of range in %q", value)
	}
	if minute < 0 || minute > 59 {
		return 0, fmt.Errorf("minute out of range in %q", value)
	}
	return (hour*minutesPerHour + minute) * secondsPerMinute, nil
}
