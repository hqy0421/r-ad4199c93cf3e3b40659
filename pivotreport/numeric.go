package pivotreport

import (
	"fmt"
	"math/big"
	"strings"
)

// MetricType declares how a metric column is typed.
type MetricType string

const (
	// TypeInt is an exact whole number. No decimal point is accepted.
	TypeInt MetricType = "int"
	// TypeCents is an exact fixed-point amount with two decimal places
	// (a currency "cent" value). The canonical representation is always
	// scaled by 100 and carries no floating point anywhere.
	TypeCents MetricType = "cents"
	// TypeDecimal is an exact fixed-point decimal with the declared scale.
	TypeDecimal MetricType = "decimal"
	// TypeText is an opaque string. Text metrics are never aggregated.
	TypeText MetricType = "text"
)

// Aggregate declares whether a metric participates in business rollups.
type Aggregate string

const (
	// AggSum adds the metric across detail rows (the default for numeric types).
	AggSum Aggregate = "sum"
	// AggNone keeps the metric as a fact but never sums it (e.g. margins/rates).
	AggNone Aggregate = "none"
)

// ValueKind is the type tag of a harvested cell value. NULL is deliberately
// distinct from a numeric zero.
type ValueKind string

const (
	KindNull    ValueKind = "null"
	KindInt     ValueKind = "int"
	KindCents   ValueKind = "cents"
	KindDecimal ValueKind = "decimal"
	KindText    ValueKind = "text"
)

// Value is a typed cell value. Numeric values are exported as decimal strings
// so that no JSON consumer can lose integer or cent precision.
type Value struct {
	Kind    ValueKind `json:"kind"`
	Int     string    `json:"int,omitempty"`
	Cents   string    `json:"cents,omitempty"`
	Decimal string    `json:"decimal,omitempty"`
	Text    string    `json:"text,omitempty"`
}

// IsNull reports whether the value is the missing value.
func (v Value) IsNull() bool { return v.Kind == KindNull }

// IsZero reports whether a numeric value is exactly zero. NULL is not zero.
func (v Value) IsZero() (bool, bool) {
	switch v.Kind {
	case KindInt:
		return trimIntString(v.Int) == "0", true
	case KindCents:
		return trimIntString(v.Cents) == "0", true
	case KindDecimal:
		r, ok := new(big.Rat).SetString(v.Decimal)
		return ok && r.Sign() == 0, true
	}
	return false, false
}

// numeric returns the exact rational value of a numeric Value.
func (v Value) numeric() (*big.Rat, bool, error) {
	switch v.Kind {
	case KindInt:
		r, ok := new(big.Rat).SetString(trimIntString(v.Int))
		if !ok {
			return nil, true, fmt.Errorf("invalid int value %q", v.Int)
		}
		return r, true, nil
	case KindCents:
		scaled, ok := new(big.Rat).SetString(trimIntString(v.Cents))
		if !ok {
			return nil, true, fmt.Errorf("invalid cents value %q", v.Cents)
		}
		return scaled.Quo(scaled, big.NewRat(100, 1)), true, nil
	case KindDecimal:
		r, ok := new(big.Rat).SetString(v.Decimal)
		if !ok {
			return nil, true, fmt.Errorf("invalid decimal value %q", v.Decimal)
		}
		return r, true, nil
	}
	return nil, false, nil
}

func trimIntString(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// parseCell converts raw scraped cell text into a typed Value according to the
// declared metric type. The raw text is the source of truth: an empty string or
// a declared null token becomes NULL, and never silently becomes zero.
func parseCell(raw string, mt MetricType, scale int, nullTokens []string) (Value, error) {
	text := strings.TrimSpace(raw)
	for _, token := range nullTokens {
		if text == token {
			return Value{Kind: KindNull}, nil
		}
	}
	if text == "" {
		return Value{Kind: KindNull}, nil
	}

	// Strip benign presentation characters that never carry sign or value.
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ',', ' ', ' ', ' ', ' ', '　', '¥', '￥', '$':
			return -1
		}
		return r
	}, text)
	// Accounting parentheses mean negative.
	negative := false
	if strings.HasPrefix(clean, "(") && strings.HasSuffix(clean, ")") {
		negative = true
		clean = strings.TrimSuffix(strings.TrimPrefix(clean, "("), ")")
	}
	if strings.HasPrefix(clean, "+") {
		clean = clean[1:]
	}

	switch mt {
	case TypeInt:
		if strings.Contains(clean, ".") {
			return Value{}, fmt.Errorf("value %q is not an integer", raw)
		}
		r, ok := new(big.Int).SetString(clean, 10)
		if !ok {
			return Value{}, fmt.Errorf("value %q is not a valid int", raw)
		}
		if negative {
			r.Neg(r)
		}
		return Value{Kind: KindInt, Int: r.String()}, nil
	case TypeCents:
		scaled, err := parseFixed(clean, 2)
		if err != nil {
			return Value{}, fmt.Errorf("value %q is not a valid cents amount: %s", raw, err)
		}
		if negative {
			scaled.Neg(scaled)
		}
		return Value{Kind: KindCents, Cents: scaled.String()}, nil
	case TypeDecimal:
		if scale < 0 {
			return Value{}, fmt.Errorf("decimal scale %d is invalid", scale)
		}
		scaled, err := parseFixed(clean, scale)
		if err != nil {
			return Value{}, fmt.Errorf("value %q is not a valid decimal: %s", raw, err)
		}
		if negative {
			scaled.Neg(scaled)
		}
		return Value{Kind: KindDecimal, Decimal: formatFixed(scaled, scale)}, nil
	case TypeText:
		return Value{Kind: KindText, Text: raw}, nil
	}
	return Value{}, fmt.Errorf("unknown metric type %q", mt)
}

// parseFixed parses an exact decimal string into an integer scaled by 10^scale.
// It rejects extra fractional digits instead of rounding them away.
func parseFixed(s string, scale int) (*big.Int, error) {
	if s == "" {
		return nil, fmt.Errorf("empty number")
	}
	negative := false
	if strings.HasPrefix(s, "-") {
		negative = true
		s = s[1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("multiple decimal points")
	}
	intPart := parts[0]
	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if intPart == "" && fracPart == "" {
		return nil, fmt.Errorf("no digits")
	}
	if intPart == "" {
		intPart = "0"
	}
	if !allDigits(intPart) || (fracPart != "" && !allDigits(fracPart)) {
		return nil, fmt.Errorf("non-digit characters")
	}
	if len(fracPart) > scale {
		extra := strings.Trim(fracPart[scale:], "0")
		if extra != "" {
			return nil, fmt.Errorf("more than %d fractional digits (%q would be truncated)", scale, fracPart)
		}
		fracPart = fracPart[:scale]
	}
	for len(fracPart) < scale {
		fracPart += "0"
	}
	r, ok := new(big.Int).SetString(intPart+fracPart, 10)
	if !ok {
		return nil, fmt.Errorf("unparseable number")
	}
	if negative {
		r.Neg(r)
	}
	return r, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// formatFixed renders an integer scaled by 10^scale back as a decimal string.
func formatFixed(scaled *big.Int, scale int) string {
	s := scaled.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for len(s) <= scale {
		s = "0" + s
	}
	body := s[:len(s)-scale]
	if scale > 0 {
		body += "." + s[len(s)-scale:]
	}
	if neg && strings.Trim(body, "0.") != "" {
		body = "-" + body
	}
	return body
}
