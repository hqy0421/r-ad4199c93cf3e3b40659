package pivotreport

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"
)

// SchemaVersion is the version of the declaration and of the written detail
// and summary documents. Everything written records it; everything read
// rejects anything else. There is exactly one supported version.
const SchemaVersion = 1

// AxisRole declares what an axis header contributes to each fact.
type AxisRole string

const (
	// AxisDimension carries business context (region, product, channel...).
	AxisDimension AxisRole = "dimension"
	// AxisPeriod carries the accounting period a fact belongs to.
	AxisPeriod AxisRole = "period"
	// AxisLabel identifies the row or column within its context. It is carried
	// through to facts but is never rolled up on its own.
	AxisLabel AxisRole = "label"
)

// Orientation declares where an axis lives.
type Orientation string

const (
	// OrientationRow means the axis occupies leading table columns; its values
	// are read from each detail row and are inherited across rowspans.
	OrientationRow Orientation = "row"
	// OrientationColumn means the axis occupies a level of the column header
	// tree, above the metric leaves. Its values are read from the header path.
	OrientationColumn Orientation = "column"
)

// Axis describes one declared axis.
type Axis struct {
	Name string   `json:"name"`
	Role AxisRole `json:"role"`
	// Orientation is "row" or "column". All row axes must be declared before
	// any column axis.
	Orientation Orientation `json:"orientation"`
	// Levels is the number of table columns (for row axes) or header tree
	// levels (for column axes) the axis occupies.
	Levels int `json:"levels"`
	// LevelNames names each level, in order. It is required when Levels > 1 so
	// that facts can name inherited levels unambiguously.
	LevelNames []string `json:"level_names,omitempty"`
	// PeriodFormat is only meaningful for AxisPeriod and must be one of the
	// accepted formats (see periodFormats).
	PeriodFormat string `json:"period_format,omitempty"`
}

// Metric declares one measurable leaf column.
type Metric struct {
	// Name is the final leaf name shown in the metric header (e.g. "销量").
	Name string `json:"name"`
	// Path is the full header path of the leaf from the first metric level to
	// the leaf itself. Column axes are prepended automatically, so Path must
	// not repeat them.
	Path      []string   `json:"path"`
	Type      MetricType `json:"type"`
	Scale     int        `json:"scale,omitempty"`
	Aggregate Aggregate  `json:"aggregate,omitempty"`
	Unit      string     `json:"unit,omitempty"`
	Nulls     []string   `json:"null_tokens,omitempty"`
}

// Layout declares how the HTML table is shaped.
type Layout struct {
	// HeaderRows is the number of leading rows that contain the header tree.
	HeaderRows int `json:"header_rows"`
	// MarkerColumn is the zero-based column index of the cell that marks a row
	// as detail / subtotal / grand total. It is never a value column.
	MarkerColumn int `json:"marker_column"`
	// Markers declares the text that classifies rows. Only detail rows produce
	// facts; subtotal and grand-total rows are kept as evidence but never
	// contribute to rollups.
	Markers Markers `json:"markers"`
}

// Markers holds the declared row classification vocabulary. Matching is exact
// on the trimmed cell text after whitespace normalisation.
type Markers struct {
	Detail     []string `json:"detail"`
	Subtotal   []string `json:"subtotal"`
	GrandTotal []string `json:"grand_total"`
	Ignored    []string `json:"ignored,omitempty"`
}

// Declaration is the full, versioned description of a report.
type Declaration struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	// TableSelector locates the report table in the live page.
	TableSelector string `json:"table_selector"`
	// Axes are declared in table order: row axes first, then column axes
	// from the top of the header tree to the leaves.
	Axes    []Axis   `json:"axes"`
	Metrics []Metric `json:"metrics"`
	Layout  Layout   `json:"layout"`
}

// DecodeDeclaration parses and fully validates a declaration.
func DecodeDeclaration(data []byte) (*Declaration, error) {
	var d Declaration
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, failure(KindStructure, "decode declaration", "invalid declaration JSON: %s", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// LoadDeclaration reads and validates a declaration from disk.
func LoadDeclaration(path string) (*Declaration, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, failure(KindStructure, "load declaration", "cannot read %s: %s", path, err)
	}
	return DecodeDeclaration(data)
}

func (d *Declaration) checkVersion(op string) error {
	if d.Version != SchemaVersion {
		return failure(KindVersion, op,
			"declaration version %d is not supported by this build (supports version %d); documents of different versions are never mixed",
			d.Version, SchemaVersion)
	}
	return nil
}

// Validate enforces the structural and semantic rules of a declaration.
func (d *Declaration) Validate() error {
	const op = "validate declaration"
	if err := d.checkVersion(op); err != nil {
		return err
	}
	if strings.TrimSpace(d.Name) == "" {
		return failure(KindSemantic, op, "declaration name must not be empty")
	}
	if strings.TrimSpace(d.TableSelector) == "" {
		return failure(KindSemantic, op, "table_selector must not be empty")
	}
	if len(d.Axes) == 0 {
		return failure(KindSemantic, op, "at least one axis must be declared")
	}
	if len(d.Metrics) == 0 {
		return failure(KindSemantic, op, "at least one metric must be declared")
	}

	periods := 0
	seenColumnAxis := false
	axisNames := map[string]bool{}
	for i, axis := range d.Axes {
		if strings.TrimSpace(axis.Name) == "" {
			return failure(KindSemantic, op, "axis %d has an empty name", i)
		}
		if axisNames[axis.Name] {
			return failure(KindSemantic, op, "axis name %q is declared more than once", axis.Name)
		}
		axisNames[axis.Name] = true
		if axis.Levels < 1 {
			return failure(KindSemantic, op, "axis %q must declare at least one level", axis.Name)
		}
		if axis.Levels > 1 {
			if len(axis.LevelNames) != axis.Levels {
				return failure(KindSemantic, op, "axis %q declares %d levels but %d level names; every level must be named", axis.Name, axis.Levels, len(axis.LevelNames))
			}
			for _, name := range axis.LevelNames {
				if strings.TrimSpace(name) == "" {
					return failure(KindSemantic, op, "axis %q has an unnamed level", axis.Name)
				}
			}
		}
		switch axis.Role {
		case AxisDimension, AxisLabel:
			if axis.PeriodFormat != "" {
				return failure(KindSemantic, op, "axis %q is not a period axis but declares period_format", axis.Name)
			}
		case AxisPeriod:
			periods++
			if axis.Levels != 1 {
				return failure(KindSemantic, op, "period axis %q must declare exactly one level", axis.Name)
			}
			if !validPeriodFormat(axis.PeriodFormat) {
				return failure(KindSemantic, op, "period axis %q declares unsupported period_format %q", axis.Name, axis.PeriodFormat)
			}
		default:
			return failure(KindSemantic, op, "axis %q declares unknown role %q", axis.Name, axis.Role)
		}
		switch axis.Orientation {
		case OrientationRow:
			if seenColumnAxis {
				return failure(KindSemantic, op, "row axis %q is declared after a column axis; row axes must come first", axis.Name)
			}
		case OrientationColumn:
			seenColumnAxis = true
			if axis.Role == AxisPeriod && axis.Orientation == OrientationRow {
				return failure(KindSemantic, op, "unreachable")
			}
		default:
			return failure(KindSemantic, op, "axis %q declares unknown orientation %q", axis.Name, axis.Orientation)
		}
	}
	if periods > 1 {
		return failure(KindSemantic, op, "at most one period axis may be declared, found %d", periods)
	}

	seen := map[string]bool{}
	for i, metric := range d.Metrics {
		const mop = op
		if strings.TrimSpace(metric.Name) == "" {
			return failure(KindSemantic, mop, "metric %d has an empty name", i)
		}
		if len(metric.Path) == 0 {
			return failure(KindSemantic, mop, "metric %q must declare its full header path", metric.Name)
		}
		if normaliseText(metric.Path[len(metric.Path)-1]) != normaliseText(metric.Name) {
			return failure(KindSemantic, mop, "metric %q does not match the final element of its header path %q", metric.Name, strings.Join(metric.Path, " / "))
		}
		for _, segment := range metric.Path {
			if strings.TrimSpace(segment) == "" {
				return failure(KindSemantic, mop, "metric %q has an empty header path segment", metric.Name)
			}
		}
		key := pathKey(metric.Path)
		if seen[key] {
			return failure(KindSemantic, mop, "duplicate metric header path %q; same-named leaves must be disambiguated by distinct paths and are never merged into one column", strings.Join(metric.Path, " / "))
		}
		seen[key] = true
		switch metric.Type {
		case TypeInt, TypeCents, TypeDecimal, TypeText:
		default:
			return failure(KindSemantic, mop, "metric %q declares unknown type %q", metric.Name, metric.Type)
		}
		switch metric.Aggregate {
		case "", AggSum, AggNone:
		default:
			return failure(KindSemantic, mop, "metric %q declares unknown aggregate %q", metric.Name, metric.Aggregate)
		}
		if metric.Type == TypeText && metric.Aggregate == AggSum {
			return failure(KindSemantic, mop, "text metric %q cannot be summed", metric.Name)
		}
		if metric.Type == TypeDecimal {
			if metric.Scale < 0 || metric.Scale > 18 {
				return failure(KindSemantic, mop, "decimal metric %q must declare scale between 0 and 18", metric.Name)
			}
		} else if metric.Scale != 0 {
			return failure(KindSemantic, mop, "metric %q declares scale but its type is %q", metric.Name, metric.Type)
		}
	}

	if d.Layout.HeaderRows < 1 {
		return failure(KindSemantic, op, "layout.header_rows must be at least 1")
	}
	if d.Layout.MarkerColumn < 0 {
		return failure(KindSemantic, op, "layout.marker_column must not be negative")
	}
	if len(d.Layout.Markers.Detail) == 0 {
		return failure(KindSemantic, op, "layout.markers.detail must declare at least one marker; without it no row can be a fact")
	}
	allMarkers := map[string]string{}
	register := func(kind string, markers []string) error {
		for _, marker := range markers {
			norm := normaliseText(marker)
			if norm == "" {
				return failure(KindSemantic, op, "layout.markers.%s contains an empty marker", kind)
			}
			if prior, ok := allMarkers[norm]; ok && prior != kind {
				return failure(KindSemantic, op, "marker %q is declared as both %s and %s", marker, prior, kind)
			}
			allMarkers[norm] = kind
		}
		return nil
	}
	if err := register("detail", d.Layout.Markers.Detail); err != nil {
		return err
	}
	if err := register("subtotal", d.Layout.Markers.Subtotal); err != nil {
		return err
	}
	if err := register("grand_total", d.Layout.Markers.GrandTotal); err != nil {
		return err
	}
	if err := register("ignored", d.Layout.Markers.Ignored); err != nil {
		return err
	}
	return nil
}

// RowAxes returns the declared row-oriented axes, in order.
func (d *Declaration) RowAxes() []Axis {
	var out []Axis
	for _, axis := range d.Axes {
		if axis.Orientation == OrientationRow {
			out = append(out, axis)
		}
	}
	return out
}

// ColumnAxes returns the declared column-oriented axes, top-down.
func (d *Declaration) ColumnAxes() []Axis {
	var out []Axis
	for _, axis := range d.Axes {
		if axis.Orientation == OrientationColumn {
			out = append(out, axis)
		}
	}
	return out
}

func (d *Declaration) rowAxisWidth() int {
	n := 0
	for _, axis := range d.RowAxes() {
		n += axis.Levels
	}
	return n
}

func (d *Declaration) columnAxisDepth() int {
	n := 0
	for _, axis := range d.ColumnAxes() {
		n += axis.Levels
	}
	return n
}

func pathKey(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = normaliseText(p)
	}
	return strings.Join(parts, "\x00")
}

func normaliseText(s string) string {
	return strings.Join(strings.Fields(strings.Replace(s, " ", " ", -1)), " ")
}

// rowClass is the classification of a body row.
type rowClass string

const (
	rowDetail     rowClass = "detail"
	rowSubtotal   rowClass = "subtotal"
	rowGrandTotal rowClass = "grand_total"
	rowIgnored    rowClass = "ignored"
)

func (rc rowClass) contributes() bool { return rc == rowDetail }

func (d *Declaration) classify(marker string) (rowClass, error) {
	norm := normaliseText(marker)
	for _, m := range d.Layout.Markers.Detail {
		if normaliseText(m) == norm {
			return rowDetail, nil
		}
	}
	for _, m := range d.Layout.Markers.Subtotal {
		if normaliseText(m) == norm {
			return rowSubtotal, nil
		}
	}
	for _, m := range d.Layout.Markers.GrandTotal {
		if normaliseText(m) == norm {
			return rowGrandTotal, nil
		}
	}
	for _, m := range d.Layout.Markers.Ignored {
		if normaliseText(m) == norm {
			return rowIgnored, nil
		}
	}
	return "", failure(KindSemantic, "classify rows",
		"row marker %q is not declared as detail, subtotal, grand_total or ignored; an unclassified row cannot be counted safely", marker)
}

func describePath(path []string) string {
	return strings.Join(path, " / ")
}

var _ = fmt.Sprintf
