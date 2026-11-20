package pivotreport

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ColumnInfo describes one metric value column of the published detail file.
type ColumnInfo struct {
	Column int        `json:"column"`
	Metric string     `json:"metric"`
	Path   []string   `json:"metric_path"`
	Type   MetricType `json:"type"`
	Scale  int        `json:"scale,omitempty"`
	Unit   string     `json:"unit,omitempty"`
	// HeaderPath is the complete header path of the column as harvested,
	// including column axes and metric levels.
	HeaderPath  []string  `json:"header_path"`
	HeaderCells []CellRef `json:"header_cells"`
	// ColumnAxis maps each declared column axis level to its value.
	ColumnAxis []AxisValue `json:"column_axis,omitempty"`
	Period     *Period     `json:"period,omitempty"`
}

// Detail is the published detail document: one typed fact per detail row per
// metric column, each with its exact source cells. It is the file that is
// reopened, summed and audited.
type Detail struct {
	Version            int    `json:"version"`
	DeclarationVersion int    `json:"declaration_version"`
	Report             string `json:"report"`

	Source SourceRef `json:"source"`

	// Axes mirrors the declaration's axes so the file is self-describing.
	Axes    []Axis       `json:"axes"`
	Metrics []Metric     `json:"metrics"`
	Columns []ColumnInfo `json:"columns"`

	Facts      []Fact      `json:"facts"`
	SourceRows []SourceRow `json:"source_rows,omitempty"`

	// Counts makes an empty report explicit.
	DetailRows int  `json:"detail_rows"`
	Empty      bool `json:"empty"`

	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// OutputPaths names the five artefacts of a run.
type OutputPaths struct {
	Detail  string
	Summary string
	Raw     string
}

// WriteOutputs writes the detail and summary documents (and the raw harvest when
// Raw is set), then re-opens the detail file and verifies that every typed value
// survived the write bit-for-bit. A failed re-read is reported as a write error.
func WriteOutputs(paths OutputPaths, detail *Detail, summary *Summary) error {
	if detail == nil || summary == nil {
		return failure(KindWrite, "write outputs", "detail and summary are both required")
	}
	if strings.TrimSpace(paths.Detail) == "" || strings.TrimSpace(paths.Summary) == "" {
		return failure(KindWrite, "write outputs", "both a detail path and a summary path are required")
	}
	if err := writeJSON(paths.Detail, "write detail", detail); err != nil {
		return err
	}
	reopened, err := ReadDetail(paths.Detail)
	if err != nil {
		return failure(KindWrite, "verify detail", "the detail file %s cannot be re-opened: %s", paths.Detail, err)
	}
	if err := verifyRoundTrip(detail, reopened); err != nil {
		return err
	}
	if err := writeJSON(paths.Summary, "write summary", summary); err != nil {
		return err
	}
	return nil
}

func writeJSON(path, op string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return failure(KindWrite, op, "cannot encode %T: %s", value, err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return failure(KindWrite, op, "cannot create directory %s: %s", dir, err)
		}
	}
	tmp := path + ".tmp"
	if err := ioutil.WriteFile(tmp, data, 0644); err != nil {
		return failure(KindWrite, op, "cannot write %s: %s", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return failure(KindWrite, op, "cannot replace %s: %s", path, err)
	}
	return nil
}

// ReadDetail re-opens a detail document, rejecting other versions and any value
// that cannot be represented under its declared type.
func ReadDetail(path string) (*Detail, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, failure(KindStructure, "read detail", "cannot read %s: %s", path, err)
	}
	return DecodeDetail(data)
}

// DecodeDetail parses a detail document and validates its version and values.
func DecodeDetail(data []byte) (*Detail, error) {
	var d Detail
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, failure(KindStructure, "read detail", "invalid detail document: %s", err)
	}
	if d.Version != SchemaVersion {
		return nil, failure(KindVersion, "read detail", "detail version %d is not supported by this build (supports version %d); documents of different versions are never mixed", d.Version, SchemaVersion)
	}
	if d.DeclarationVersion != SchemaVersion {
		return nil, failure(KindVersion, "read detail", "detail was produced from declaration version %d, which this build does not support (supports version %d)", d.DeclarationVersion, SchemaVersion)
	}
	columnType := map[int]ColumnInfo{}
	for _, c := range d.Columns {
		columnType[c.Column] = c
	}
	for i, f := range d.Facts {
		ci, ok := columnType[f.Cell.Column]
		if !ok {
			return nil, failureAt(KindStructure, "read detail", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path),
				"fact %d refers to column %d which is not declared in the detail document", i, f.Cell.Column)
		}
		if pathKey(ci.Path) != pathKey(f.Path) {
			return nil, failureAt(KindSemantic, "read detail", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path),
				"fact %d claims metric %q but its column %d is %q", i, describePath(f.Path), f.Cell.Column, describePath(ci.Path))
		}
		if err := checkValue(ci.Type, ci.Scale, f.Value); err != nil {
			return nil, failureAt(KindType, "read detail", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "fact %d has an invalid value: %s", i, err)
		}
	}
	return &d, nil
}

func checkValue(mt MetricType, scale int, v Value) error {
	switch v.Kind {
	case KindNull:
		return nil
	case KindInt:
		if mt != TypeInt {
			return fmt.Errorf("value kind int in a %s column", mt)
		}
		if _, ok := new(big.Int).SetString(trimIntString(v.Int), 10); !ok {
			return fmt.Errorf("value %q is not an integer", v.Int)
		}
		return nil
	case KindCents:
		if mt != TypeCents {
			return fmt.Errorf("value kind cents in a %s column", mt)
		}
		if _, ok := new(big.Int).SetString(trimIntString(v.Cents), 10); !ok {
			return fmt.Errorf("value %q is not an integer number of cents", v.Cents)
		}
		return nil
	case KindDecimal:
		if mt != TypeDecimal {
			return fmt.Errorf("value kind decimal in a %s column", mt)
		}
		if _, err := parseFixed(v.Decimal, scale); err != nil {
			return fmt.Errorf("value %q does not fit scale %d: %s", v.Decimal, scale, err)
		}
		return nil
	case KindText:
		if mt != TypeText {
			return fmt.Errorf("value kind text in a %s column", mt)
		}
		return nil
	}
	return fmt.Errorf("unknown value kind %q", v.Kind)
}

// verifyRoundTrip checks that a re-read detail document is value-identical to
// the one written.
func verifyRoundTrip(written, reopened *Detail) error {
	if len(written.Facts) != len(reopened.Facts) {
		return failure(KindWrite, "verify detail", "re-opened detail has %d facts but %d were written", len(reopened.Facts), len(written.Facts))
	}
	for i := range written.Facts {
		a, b := written.Facts[i], reopened.Facts[i]
		if a.ID != b.ID || a.Value != b.Value || a.Cell != b.Cell || a.SourceRow != b.SourceRow {
			return failureAt(KindWrite, "verify detail", a.SourceRow, a.Cell.Column, a.Cell.Text, describePath(a.Path),
				"fact %q changed across the write (wrote %+v, read %+v); an unstable value must not be published", a.ID, a.Value, b.Value)
		}
	}
	if len(written.SourceRows) != len(reopened.SourceRows) {
		return failure(KindWrite, "verify detail", "re-opened detail has %d retained rows but %d were written", len(reopened.SourceRows), len(written.SourceRows))
	}
	return nil
}

// ReadSummary re-opens a summary document and rejects other versions.
func ReadSummary(path string) (*Summary, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, failure(KindStructure, "read summary", "cannot read %s: %s", path, err)
	}
	var s Summary
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, failure(KindStructure, "read summary", "invalid summary document: %s", err)
	}
	if s.Version != SchemaVersion {
		return nil, failure(KindVersion, "read summary", "summary version %d is not supported by this build (supports version %d)", s.Version, SchemaVersion)
	}
	return &s, nil
}

// SumFacts recomputes exact totals from a re-opened detail document, so a
// consumer can verify the summary without trusting it.
func SumFacts(decl *Declaration, detail *Detail) (*Summary, error) {
	if decl == nil || detail == nil {
		return nil, failure(KindStructure, "sum facts", "declaration and detail are both required")
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	perColumn := map[int]ColumnInfo{}
	for _, c := range detail.Columns {
		perColumn[c.Column] = c
	}
	facts := make([]Fact, 0, len(detail.Facts))
	for _, f := range detail.Facts {
		ci, ok := perColumn[f.Cell.Column]
		if !ok {
			return nil, failureAt(KindStructure, "sum facts", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "fact refers to undeclared column %d", f.Cell.Column)
		}
		metric, ok := metricForColumn(decl, ci)
		if !ok {
			return nil, failureAt(KindSemantic, "sum facts", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "column %d does not match any declared metric", f.Cell.Column)
		}
		if f.Value.IsNull() {
			// keep typed NULL
		}
		nf := f
		nf.Path = metric.Path
		facts = append(facts, nf)
	}
	x := &Extraction{Facts: facts}
	// Rebuild the column plan so totals are produced per column, exactly as
	// during extraction. The detail document records everything needed.
	ciByColumn := map[int]ColumnInfo{}
	for _, c := range detail.Columns {
		ciByColumn[c.Column] = c
		if c.Period != nil {
			found := false
			for _, p := range x.Periods {
				if p.Canonical == c.Period.Canonical {
					found = true
					break
				}
			}
			if !found {
				x.Periods = append(x.Periods, *c.Period)
			}
		}
	}
	columnNumbers := make([]int, 0, len(ciByColumn))
	for col := range ciByColumn {
		columnNumbers = append(columnNumbers, col)
	}
	sort.Ints(columnNumbers)
	for _, col := range columnNumbers {
		ci := ciByColumn[col]
		metric, ok := metricForColumn(decl, ci)
		if !ok {
			return nil, failureAt(KindSemantic, "sum facts", 0, col, "", describePath(ci.Path), "column %d does not match any declared metric", col)
		}
		x.Columns = append(x.Columns, columnPlan{
			Column: col,
			Metric: metric,
			Levels: ci.ColumnAxis,
			Period: ci.Period,
		})
	}
	return buildSummary(decl, x, &RawTable{}, ReconcileOff)
}

func metricForColumn(decl *Declaration, ci ColumnInfo) (Metric, bool) {
	for _, m := range decl.Metrics {
		if pathKey(m.Path) == pathKey(ci.Path) {
			return m, true
		}
	}
	return Metric{}, false
}
