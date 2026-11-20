package pivotreport

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// ReconcileMode controls what happens when a published subtotal or grand-total
// row disagrees with the rollup of the detail rows it covers.
type ReconcileMode string

const (
	// ReconcileWarn records a diagnostic and keeps the computed rollup.
	ReconcileWarn ReconcileMode = "warn"
	// ReconcileError refuses to publish a report whose published totals and
	// detail rows disagree.
	ReconcileError ReconcileMode = "error"
	// ReconcileOff skips the comparison entirely.
	ReconcileOff ReconcileMode = "off"
)

func (m ReconcileMode) valid() bool {
	switch m {
	case "", ReconcileWarn, ReconcileError, ReconcileOff:
		return true
	}
	return false
}

// MetricTotal is the business total of one metric in one column context. A
// column context is the declared column axis combination plus the metric's
// full header path, so two periods that share a leaf name ("件数") are never
// added together by accident.
type MetricTotal struct {
	// Column is the source column of this metric instance.
	Column int        `json:"column"`
	Metric string     `json:"metric"`
	Path   []string   `json:"metric_path"`
	Type   MetricType `json:"type"`
	Unit   string     `json:"unit,omitempty"`
	// ColumnAxis holds the declared column-level values (period included).
	ColumnAxis []AxisValue `json:"column_axis,omitempty"`
	Period     *Period     `json:"period,omitempty"`
	// Total is NULL when every contributing fact was NULL, or when the metric
	// is declared aggregate:none (see Aggregated).
	Total Value `json:"total"`
	// Aggregated is false for metrics declared as not summable.
	Aggregated bool `json:"aggregated"`
	// NonNull and Null count the contributing facts, so NULL is never
	// mistaken for zero.
	NonNull int `json:"non_null"`
	Null    int `json:"null"`
}

// GroupTotal is a rollup over one group of a declared row dimension or period.
type GroupTotal struct {
	Kind   string        `json:"kind"` // "dimension" or "period"
	Axis   string        `json:"axis"`
	Levels []string      `json:"levels,omitempty"`
	Value  string        `json:"value"`
	Period *Period       `json:"period,omitempty"`
	Facts  int           `json:"facts"`
	Totals []MetricTotal `json:"totals"`
}

// MissingCount reports absent values per column. Missing is tracked separately
// from zero at every level.
type MissingCount struct {
	Column int        `json:"column"`
	Metric string     `json:"metric"`
	Path   []string   `json:"metric_path"`
	Type   MetricType `json:"type"`
	Period *Period    `json:"period,omitempty"`
	Null   int        `json:"null"`
	Total  int        `json:"total"`
}

// SourceRef identifies where the table came from.
type SourceRef struct {
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Caption string `json:"caption,omitempty"`
	Table   string `json:"table_selector,omitempty"`
	Bounds  *Rect  `json:"bounds,omitempty"`
}

// PublishedTotal is one retained subtotal / grand-total row and the typed values
// it showed, without any contribution to the rollup.
type PublishedTotal struct {
	Row    int              `json:"row"`
	Class  string           `json:"class"`
	Marker string           `json:"marker"`
	Values []PublishedValue `json:"values"`
}

// PublishedValue is one typed value of a retained row.
type PublishedValue struct {
	Column int        `json:"column"`
	Metric string     `json:"metric,omitempty"`
	Path   []string   `json:"metric_path,omitempty"`
	Type   MetricType `json:"type,omitempty"`
	Value  Value      `json:"value"`
	Cell   CellRef    `json:"cell"`
}

// Reconciliation is the comparison of one published total against the rollup of
// the detail rows it covers, for one metric column.
type Reconciliation struct {
	Row       int      `json:"row"`
	Class     string   `json:"class"`
	Covered   []int    `json:"covered_rows"`
	Column    int      `json:"column"`
	Metric    string   `json:"metric"`
	Path      []string `json:"metric_path"`
	Published Value    `json:"published"`
	Computed  Value    `json:"computed"`
	Agrees    bool     `json:"agrees"`
	Reason    string   `json:"reason,omitempty"`
}

// Summary is the published business rollup plus the audit information needed to
// trust it.
type Summary struct {
	Version            int    `json:"version"`
	DeclarationVersion int    `json:"declaration_version"`
	Report             string `json:"report"`

	Source SourceRef `json:"source"`

	// DetailRows is the number of classified detail rows; Facts is the number
	// of published facts (one per detail row per metric column). SourceRows is
	// the number of retained non-detail rows.
	DetailRows int `json:"detail_rows"`
	Facts      int `json:"facts"`
	SourceRows int `json:"source_rows"`
	Columns    int `json:"columns"`

	// Total holds one rollup per metric column, computed from detail facts
	// only. Published subtotal and total rows are compared against it, never
	// added into it.
	Total []MetricTotal `json:"total"`
	// ByDimension / ByPeriod group the same detail facts.
	ByDimension []GroupTotal `json:"by_dimension,omitempty"`
	ByPeriod    []GroupTotal `json:"by_period,omitempty"`

	// Missing counts absent values per column.
	Missing []MissingCount `json:"missing"`
	// Empty is true when the report has no detail rows at all. Typed totals
	// and missing counts still explain exactly what was (not) there.
	Empty bool `json:"empty"`

	// SourceTotals holds the published subtotal / grand-total rows as read,
	// never as contributions.
	SourceTotals []PublishedTotal `json:"source_totals,omitempty"`
	// Reconciliation compares computed rollups with the published totals that
	// claim to summarise them.
	Reconciliation []Reconciliation `json:"reconciliation,omitempty"`

	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func periodAxisName(decl *Declaration) string {
	for _, axis := range decl.Axes {
		if axis.Role == AxisPeriod {
			return axis.Name
		}
	}
	return "period"
}

func aggregateOf(m Metric) Aggregate {
	if m.Aggregate == "" {
		return AggSum
	}
	return m.Aggregate
}

// sums holds exact accumulators keyed by source column.
type sums struct {
	values map[int]*big.Rat
	any    map[int]bool
	nulls  map[int]int
	counts map[int]int
}

func newSums() *sums {
	return &sums{
		values: map[int]*big.Rat{},
		any:    map[int]bool{},
		nulls:  map[int]int{},
		counts: map[int]int{},
	}
}

func (s *sums) add(column int, v Value) error {
	s.counts[column]++
	if v.IsNull() {
		s.nulls[column]++
		return nil
	}
	r, numeric, err := v.numeric()
	if err != nil {
		return err
	}
	if !numeric {
		return nil
	}
	if s.values[column] == nil {
		s.values[column] = new(big.Rat)
	}
	s.values[column].Add(s.values[column], r)
	s.any[column] = true
	return nil
}

func (s *sums) result(m Metric, column int) Value {
	if !s.any[column] {
		return Value{Kind: KindNull}
	}
	return formatSum(m, s.values[column])
}

// formatSum renders an exact sum back into the declared metric representation.
func formatSum(metric Metric, r *big.Rat) Value {
	switch metric.Type {
	case TypeInt:
		if !r.IsInt() {
			return Value{Kind: KindNull}
		}
		return Value{Kind: KindInt, Int: r.Num().String()}
	case TypeCents:
		scaled := new(big.Rat).Mul(r, big.NewRat(100, 1))
		if !scaled.IsInt() {
			return Value{Kind: KindNull}
		}
		return Value{Kind: KindCents, Cents: scaled.Num().String()}
	case TypeDecimal:
		scaled := new(big.Rat).Mul(r, pow10Rat(metric.Scale))
		if !scaled.IsInt() {
			return Value{Kind: KindNull}
		}
		return Value{Kind: KindDecimal, Decimal: formatFixed(scaled.Num(), metric.Scale)}
	}
	return Value{Kind: KindNull}
}

func pow10Rat(n int) *big.Rat {
	r := big.NewRat(1, 1)
	ten := big.NewRat(10, 1)
	for i := 0; i < n; i++ {
		r.Mul(r, ten)
	}
	return r
}

// totalsFor renders one MetricTotal per declared metric column.
func totalsFor(decl *Declaration, plans []columnPlan, s *sums) []MetricTotal {
	out := make([]MetricTotal, 0, len(plans))
	for _, plan := range plans {
		m := plan.Metric
		mt := MetricTotal{
			Column:     plan.Column,
			Metric:     m.Name,
			Path:       append([]string(nil), m.Path...),
			Type:       m.Type,
			Unit:       m.Unit,
			ColumnAxis: plan.Levels,
			Period:     plan.Period,
			NonNull:    s.counts[plan.Column] - s.nulls[plan.Column],
			Null:       s.nulls[plan.Column],
		}
		if aggregateOf(m) != AggSum {
			mt.Aggregated = false
			mt.Total = Value{Kind: KindNull}
		} else {
			mt.Aggregated = true
			mt.Total = s.result(m, plan.Column)
		}
		out = append(out, mt)
	}
	return out
}

func missingFor(plans []columnPlan, s *sums) []MissingCount {
	out := make([]MissingCount, 0, len(plans))
	for _, plan := range plans {
		out = append(out, MissingCount{
			Column: plan.Column,
			Metric: plan.Metric.Name,
			Path:   append([]string(nil), plan.Metric.Path...),
			Type:   plan.Metric.Type,
			Period: plan.Period,
			Null:   s.nulls[plan.Column],
			Total:  s.counts[plan.Column],
		})
	}
	return out
}

// buildSummary turns an extraction into the published summary. Every number in
// it comes from facts; retained rows are only ever compared against it.
func buildSummary(decl *Declaration, x *Extraction, table *RawTable, mode ReconcileMode) (*Summary, error) {
	if !mode.valid() {
		return nil, failure(KindSemantic, "summary", "unknown reconcile mode %q (want warn, error or off)", mode)
	}
	s := &Summary{
		Version:            SchemaVersion,
		DeclarationVersion: decl.Version,
		Report:             decl.Name,
		Source: SourceRef{
			URL:     table.URL,
			Title:   table.Title,
			Caption: table.Caption,
			Table:   decl.TableSelector,
		},
		Columns: len(x.Columns),
	}
	if table.Bounds != (Rect{}) {
		b := table.Bounds
		s.Source.Bounds = &b
	}

	overall := newSums()
	detailRows := map[int]bool{}
	for _, f := range x.Facts {
		if err := overall.add(f.Cell.Column, f.Value); err != nil {
			return nil, failureAt(KindType, "summary", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "cannot accumulate %q: %s", describePath(f.Path), err)
		}
		detailRows[f.SourceRow] = true
	}
	s.Total = totalsFor(decl, x.Columns, overall)
	s.Missing = missingFor(x.Columns, overall)
	s.Facts = len(x.Facts)
	s.DetailRows = len(detailRows)
	s.SourceRows = len(x.SourceRows)
	s.Empty = s.DetailRows == 0

	for _, axis := range decl.RowAxes() {
		if axis.Role == AxisLabel {
			continue
		}
		groups, err := dimensionGroups(decl, x, axis)
		if err != nil {
			return nil, err
		}
		s.ByDimension = append(s.ByDimension, groups...)
	}

	if len(x.Periods) > 0 {
		periods := append([]Period(nil), x.Periods...)
		sort.Slice(periods, func(i, j int) bool { return periods[i].Canonical < periods[j].Canonical })
		for _, p := range periods {
			acc := newSums()
			count := 0
			for _, f := range x.Facts {
				if f.Period == nil || f.Period.Canonical != p.Canonical {
					continue
				}
				if err := acc.add(f.Cell.Column, f.Value); err != nil {
					return nil, failureAt(KindType, "summary", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "cannot accumulate %q: %s", describePath(f.Path), err)
				}
				count++
			}
			pp := p
			s.ByPeriod = append(s.ByPeriod, GroupTotal{
				Kind:   "period",
				Axis:   periodAxisName(decl),
				Value:  p.Canonical,
				Period: &pp,
				Facts:  count,
				Totals: totalsFor(decl, periodColumns(x.Columns, p.Canonical), acc),
			})
		}
	}

	// Retain published totals and compare them with the detail rollup.
	publishedByRow := map[int][]PublishedValue{}
	for _, src := range x.SourceRows {
		if src.Class == string(rowIgnored) {
			continue
		}
		pt, err := publishedTotalOf(decl, src, x.Columns)
		if err != nil {
			return nil, err
		}
		s.SourceTotals = append(s.SourceTotals, pt)
		publishedByRow[src.Row] = pt.Values
	}

	recs, diags, err := reconcile(decl, x, publishedByRow, mode)
	if err != nil {
		return nil, err
	}
	s.Reconciliation = recs
	s.Diagnostics = append(s.Diagnostics, x.Diagnostics...)
	s.Diagnostics = append(s.Diagnostics, diags...)
	return s, nil
}

func periodColumns(plans []columnPlan, canonical string) []columnPlan {
	var out []columnPlan
	for _, p := range plans {
		if p.Period != nil && p.Period.Canonical == canonical {
			out = append(out, p)
		}
	}
	return out
}

func dimensionGroups(decl *Declaration, x *Extraction, axis Axis) ([]GroupTotal, error) {
	type acc struct {
		values []string
		sums   *sums
		facts  int
	}
	groups := map[string]*acc{}
	var order []string
	for _, f := range x.Facts {
		vals, ok := axisTuple(f, axis)
		if !ok {
			continue
		}
		key := strings.Join(vals, "\x00")
		g := groups[key]
		if g == nil {
			g = &acc{values: vals, sums: newSums()}
			groups[key] = g
			order = append(order, key)
		}
		if err := g.sums.add(f.Cell.Column, f.Value); err != nil {
			return nil, failureAt(KindType, "summary", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "cannot accumulate %q: %s", describePath(f.Path), err)
		}
		g.facts++
	}
	sort.Strings(order)
	out := make([]GroupTotal, 0, len(order))
	for _, key := range order {
		g := groups[key]
		out = append(out, GroupTotal{
			Kind:   "dimension",
			Axis:   axis.Name,
			Levels: axis.LevelNames,
			Value:  strings.Join(g.values, " / "),
			Facts:  g.facts,
			Totals: totalsFor(decl, x.Columns, g.sums),
		})
	}
	return out, nil
}

func axisTuple(f Fact, axis Axis) ([]string, bool) {
	var vals []string
	for _, av := range f.Row {
		if av.Axis != axis.Name {
			continue
		}
		vals = append(vals, av.Value)
	}
	if len(vals) != axis.Levels {
		return nil, false
	}
	return vals, true
}

// publishedTotalOf converts a retained row into typed values.
func publishedTotalOf(decl *Declaration, src SourceRow, plans []columnPlan) (PublishedTotal, error) {
	out := PublishedTotal{Row: src.Row, Class: src.Class, Marker: src.Marker}
	byColumn := map[int]SourceCell{}
	for _, sc := range src.Cells {
		byColumn[sc.Column] = sc
	}
	for _, plan := range plans {
		sc, ok := byColumn[plan.Column]
		if !ok {
			continue
		}
		value, err := parseCell(sc.Cell.Text, plan.Metric.Type, plan.Metric.Scale, plan.Metric.Nulls)
		if err != nil {
			return PublishedTotal{}, failureAt(KindType, "read published total", src.Row, plan.Column, sc.Cell.Text, describePath(plan.Metric.Path),
				"published total cell for %q cannot be typed as %s: %s", describePath(plan.Metric.Path), plan.Metric.Type, err)
		}
		out.Values = append(out.Values, PublishedValue{
			Column: plan.Column,
			Metric: plan.Metric.Name,
			Path:   append([]string(nil), plan.Metric.Path...),
			Type:   plan.Metric.Type,
			Value:  value,
			Cell:   sc.Cell,
		})
	}
	return out, nil
}

// reconcile compares published totals to the rollup of the detail rows they
// cover, column by column.
func reconcile(decl *Declaration, x *Extraction, published map[int][]PublishedValue, mode ReconcileMode) ([]Reconciliation, []Diagnostic, error) {
	if mode == "" {
		mode = ReconcileWarn
	}
	if mode == ReconcileOff || len(published) == 0 {
		return nil, nil, nil
	}

	byRow := map[int][]Fact{}
	for _, f := range x.Facts {
		byRow[f.SourceRow] = append(byRow[f.SourceRow], f)
	}

	// Order every body row so subtotal boundaries follow row order.
	var body []bodyRow
	for _, src := range x.SourceRows {
		body = append(body, bodyRow{row: src.Row, class: rowClass(src.Class)})
	}
	for r := range byRow {
		body = append(body, bodyRow{row: r, class: rowDetail})
	}
	sort.Slice(body, func(i, j int) bool { return body[i].row < body[j].row })

	planByColumn := map[int]columnPlan{}
	for _, p := range x.Columns {
		planByColumn[p.Column] = p
	}

	var out []Reconciliation
	var diags []Diagnostic
	for _, src := range x.SourceRows {
		if src.Class == string(rowIgnored) {
			continue
		}
		covered := coveredDetailRows(body, src.Row, rowClass(src.Class))
		acc := newSums()
		for _, r := range covered {
			for _, f := range byRow[r] {
				if err := acc.add(f.Cell.Column, f.Value); err != nil {
					return nil, nil, failureAt(KindType, "reconcile", f.SourceRow, f.Cell.Column, f.Cell.Text, describePath(f.Path), "cannot accumulate %q: %s", describePath(f.Path), err)
				}
			}
		}
		seenColumn := map[int]bool{}
		for _, v := range published[src.Row] {
			if seenColumn[v.Column] {
				return nil, nil, failureAt(KindSemantic, "reconcile", src.Row, v.Column, v.Cell.Text, describePath(v.Path),
					"retained row %d publishes column %d more than once; its total is ambiguous", src.Row, v.Column)
			}
			seenColumn[v.Column] = true
		}
		for _, v := range published[src.Row] {
			plan, ok := planByColumn[v.Column]
			if !ok {
				return nil, nil, failureAt(KindStructure, "reconcile", src.Row, v.Column, v.Cell.Text, describePath(v.Path),
					"retained row %d has a value in column %d, which is not a declared metric column", src.Row, v.Column)
			}
			// A metric declared as not summable (a rate, a margin) has no
			// additive rollup, so comparing it to a sum would be wrong.
			if aggregateOf(plan.Metric) != AggSum {
				continue
			}
			computed := acc.result(plan.Metric, v.Column)
			agrees, reason := valuesAgree(v.Value, computed)
			out = append(out, Reconciliation{
				Row:       src.Row,
				Class:     src.Class,
				Covered:   covered,
				Column:    v.Column,
				Metric:    plan.Metric.Name,
				Path:      append([]string(nil), plan.Metric.Path...),
				Published: v.Value,
				Computed:  computed,
				Agrees:    agrees,
				Reason:    reason,
			})
			if agrees {
				continue
			}
			msg := fmt.Sprintf("published %s row %d shows %s for column %d (%s) but the %d detail row(s) it covers total %s%s",
				src.Class, src.Row, displayValue(v.Value), v.Column, describePath(plan.Metric.Path), len(covered), displayValue(computed), reasonSuffix(reason))
			if mode == ReconcileError {
				return nil, nil, failureAt(KindSemantic, "reconcile", src.Row, v.Column, src.Marker, describePath(plan.Metric.Path), "%s", msg)
			}
			diags = append(diags, Diagnostic{Code: "reconciliation_mismatch", Message: msg, Row: src.Row, Column: v.Column, Metric: describePath(plan.Metric.Path), Cell: src.Marker})
		}
	}
	return out, diags, nil
}

// bodyRow is one classified body row, used to bound subtotal coverage.
type bodyRow struct {
	row   int
	class rowClass
}

// coveredDetailRows returns the detail rows a retained row summarises. A grand
// total covers everything; a subtotal covers the contiguous run of detail rows
// since the previous retained row.
func coveredDetailRows(body []bodyRow, retainedRow int, class rowClass) []int {
	var covered []int
	if class == rowGrandTotal {
		for _, b := range body {
			if b.class == rowDetail {
				covered = append(covered, b.row)
			}
		}
		return covered
	}
	// Walk backwards from the retained row until another retained row is hit.
	for i := len(body) - 1; i >= 0; i-- {
		b := body[i]
		if b.row >= retainedRow {
			continue
		}
		if b.class != rowDetail {
			break
		}
		covered = append(covered, b.row)
	}
	sort.Ints(covered)
	return covered
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func displayValue(v Value) string {
	switch v.Kind {
	case KindNull:
		return "NULL"
	case KindInt:
		return v.Int
	case KindCents:
		return formatFixed(mustInt(v.Cents), 2) + " (cents " + v.Cents + ")"
	case KindDecimal:
		return v.Decimal
	case KindText:
		return v.Text
	}
	return "<untyped>"
}

func mustInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// valuesAgree compares a published total with a computed one exactly.
func valuesAgree(published, computed Value) (bool, string) {
	if published.IsNull() && computed.IsNull() {
		return true, ""
	}
	if published.IsNull() != computed.IsNull() {
		if published.IsNull() {
			return false, "the published value is NULL but the covered detail rows have values"
		}
		return false, "the published value is present but the covered detail rows have no values"
	}
	pr, pnum, perr := published.numeric()
	cr, cnum, cerr := computed.numeric()
	if perr != nil || cerr != nil {
		return false, "values cannot be compared numerically"
	}
	if !pnum || !cnum {
		if published.Kind == KindText && computed.Kind == KindText {
			return published.Text == computed.Text, ""
		}
		return false, "values are not numeric"
	}
	if pr.Cmp(cr) == 0 {
		return true, ""
	}
	return false, "difference " + new(big.Rat).Sub(pr, cr).String()
}
