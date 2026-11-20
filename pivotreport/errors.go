package pivotreport

import "fmt"

// ErrorKind classifies a failure so callers can react without string matching.
type ErrorKind string

const (
	// KindVersion indicates a schema/format version mismatch or an unsupported
	// version. Mixed versions are never silently accepted.
	KindVersion ErrorKind = "version"
	// KindStructure indicates an ambiguous or malformed table structure
	// (overlapping spans, gaps, ragged rows, hidden declared cells, ...).
	KindStructure ErrorKind = "structure"
	// KindSemantic indicates an ambiguous declaration or an alignment that
	// cannot be resolved deterministically (unknown metric path, duplicate
	// header path, conflicting row markers, subtotal reconciliation failure).
	KindSemantic ErrorKind = "semantic"
	// KindType indicates raw cell text that cannot be represented under the
	// declared metric type without loss.
	KindType ErrorKind = "type"
	// KindWrite indicates a failure while persisting or re-reading output.
	KindWrite ErrorKind = "write"
	// KindHarvest indicates a failure to collect the table from the live page.
	KindHarvest ErrorKind = "harvest"
	// KindLogin indicates a failure during the authenticated navigation step.
	KindLogin ErrorKind = "login"
)

// Error is the only error type returned by this package. It always carries a
// classification and enough location information to act on.
type Error struct {
	Kind    ErrorKind
	Op      string
	Message string
	Row     int
	Column  int
	Metric  string
	Cell    string
}

func (e *Error) Error() string {
	loc := ""
	if e.Row > 0 || e.Column > 0 {
		loc += fmt.Sprintf(" (row %d, column %d)", e.Row, e.Column)
	}
	if e.Metric != "" {
		loc += fmt.Sprintf(" [metric %s]", e.Metric)
	}
	if e.Cell != "" {
		loc += fmt.Sprintf(" [cell %s]", e.Cell)
	}
	if e.Op != "" {
		return fmt.Sprintf("pivotreport %s: %s: %s%s", e.Kind, e.Op, e.Message, loc)
	}
	return fmt.Sprintf("pivotreport %s: %s%s", e.Kind, e.Message, loc)
}

// KindOf returns the ErrorKind of an error produced by this package.
func KindOf(err error) ErrorKind {
	if e, ok := err.(*Error); ok {
		return e.Kind
	}
	return ""
}

func failure(kind ErrorKind, op, format string, args ...interface{}) *Error {
	return &Error{Kind: kind, Op: op, Message: fmt.Sprintf(format, args...)}
}

func failureAt(kind ErrorKind, op string, row, column int, cell, metric, format string, args ...interface{}) *Error {
	return &Error{
		Kind:    kind,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
		Row:     row,
		Column:  column,
		Metric:  metric,
		Cell:    cell,
	}
}

// Diagnostic is a non-fatal observation recorded with the summary. It never
// replaces a value: anything that could change meaning is an Error instead.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Row     int    `json:"row,omitempty"`
	Column  int    `json:"column,omitempty"`
	Metric  string `json:"metric,omitempty"`
	Cell    string `json:"cell,omitempty"`
}

func diag(code, format string, args ...interface{}) Diagnostic {
	return Diagnostic{Code: code, Message: fmt.Sprintf(format, args...)}
}
