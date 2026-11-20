package pivotreport

import "math/big"

// Test-only exports. These keep the internal seams private in the public API
// while letting the external test package exercise exact parsing directly.

func ParseValueForTest(raw string, mt MetricType, scale int, nullTokens []string) (Value, error) {
	v, err := parseCell(raw, mt, scale, nullTokens)
	if err != nil {
		return Value{}, &Error{Kind: KindType, Op: "parse value", Message: err.Error()}
	}
	return v, nil
}

func FormatFixedForTest(scaled string, scale int) string {
	n, ok := new(big.Int).SetString(scaled, 10)
	if !ok {
		return "<invalid>"
	}
	return formatFixed(n, scale)
}

func GridDimsForTest(table *RawTable) (rows, cols int, err error) {
	g, err := buildGrid(table)
	if err != nil {
		return 0, 0, err
	}
	return g.rows, g.cols, nil
}
