package pivotreport

import (
	"fmt"
	"regexp"
	"strings"
)

// periodKind is the granularity of a declared period.
type periodKind string

const (
	periodMonth   periodKind = "month"
	periodQuarter periodKind = "quarter"
	periodYear    periodKind = "year"
	periodDay     periodKind = "day"
)

// periodFormat describes one accepted period label format. Every format has a
// single canonical output so that labels which mean the same period align even
// when written differently ("2026年1月", "2026-01", "2026/1").
type periodFormat struct {
	name    string
	kind    periodKind
	pattern *regexp.Regexp
}

func digits(n int, min int, max int) string {
	return fmt.Sprintf(`(\d{%d,%d})`, min, max)
}

var periodFormats = []periodFormat{
	{"yyyy-MM", periodMonth, regexp.MustCompile(`^(\d{4})-(\d{1,2})$`)},
	{"yyyy/MM", periodMonth, regexp.MustCompile(`^(\d{4})/(\d{1,2})$`)},
	{"yyyy.MM", periodMonth, regexp.MustCompile(`^(\d{4})\.(\d{1,2})$`)},
	{"yyyy年MM月", periodMonth, regexp.MustCompile(`^(\d{4})\s*年\s*(\d{1,2})\s*月?$`)},
	{"yyyy-MM-dd", periodDay, regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)},
	{"yyyy/MM/dd", periodDay, regexp.MustCompile(`^(\d{4})/(\d{1,2})/(\d{1,2})$`)},
	{"yyyy-Qn", periodQuarter, regexp.MustCompile(`^(\d{4})[-_ ]?[Qq](\d)$`)},
	{"yyyy年第n季度", periodQuarter, regexp.MustCompile(`^(\d{4})\s*年\s*第?\s*([1-4一二三四])\s*季度$`)},
	{"yyyy", periodYear, regexp.MustCompile(`^(\d{4})\s*年?$`)},
}

func validPeriodFormat(name string) bool {
	_, ok := lookupPeriodFormat(name)
	return ok
}

func lookupPeriodFormat(name string) (periodFormat, bool) {
	for _, f := range periodFormats {
		if f.name == name {
			return f, true
		}
	}
	return periodFormat{}, false
}

// Period is the canonical accounting period of a fact.
type Period struct {
	// Canonical is a stable, sortable key. It is produced only from the
	// declared format, never guessed from the raw label.
	Canonical string     `json:"canonical"`
	Kind      periodKind `json:"kind"`
	Label     string     `json:"label"`
}

// ParsePeriod converts a raw period label using the declared format.
func ParsePeriod(raw string, format string) (Period, error) {
	f, ok := lookupPeriodFormat(format)
	if !ok {
		return Period{}, fmt.Errorf("unsupported period_format %q", format)
	}
	text := normaliseText(strings.Replace(strings.Replace(raw, "　", " ", -1), " ", " ", -1))
	matches := f.pattern.FindStringSubmatch(text)
	if matches == nil {
		return Period{}, fmt.Errorf("period label %q does not match declared format %q", raw, format)
	}
	year, err := atoi(matches[1])
	if err != nil {
		return Period{}, fmt.Errorf("period label %q has an invalid year", raw)
	}
	canonical := ""
	switch f.kind {
	case periodYear:
		canonical = fmt.Sprintf("%04d", year)
	case periodMonth:
		month, err := atoi(matches[2])
		if err != nil || month < 1 || month > 12 {
			return Period{}, fmt.Errorf("period label %q has an invalid month", raw)
		}
		canonical = fmt.Sprintf("%04d-%02d", year, month)
	case periodQuarter:
		quarter, ok := parseQuarter(matches[2])
		if !ok {
			return Period{}, fmt.Errorf("period label %q has an invalid quarter", raw)
		}
		canonical = fmt.Sprintf("%04d-Q%d", year, quarter)
	case periodDay:
		month, err := atoi(matches[2])
		if err != nil || month < 1 || month > 12 {
			return Period{}, fmt.Errorf("period label %q has an invalid month", raw)
		}
		day, err := atoi(matches[3])
		if err != nil || day < 1 || day > 31 {
			return Period{}, fmt.Errorf("period label %q has an invalid day", raw)
		}
		canonical = fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	}
	return Period{Canonical: canonical, Kind: f.kind, Label: raw}, nil
}

func parseQuarter(s string) (int, bool) {
	switch s {
	case "1", "一":
		return 1, true
	case "2", "二":
		return 2, true
	case "3", "三":
		return 3, true
	case "4", "四":
		return 4, true
	}
	return 0, false
}

func atoi(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
