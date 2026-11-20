package pivotreport

import (
	"fmt"
	"strings"
)

// CellRef is the exact source of one rendered cell. Row and Column are the
// position where the cell originates, and the spans are what the DOM declared.
// Nothing here is recomputed from the declared schema.
type CellRef struct {
	Row     int    `json:"row"`
	Column  int    `json:"column"`
	RowSpan int    `json:"row_span"`
	ColSpan int    `json:"col_span"`
	Text    string `json:"text"`
}

func refOf(cl *cell) CellRef {
	return CellRef{Row: cl.OriginR, Column: cl.OriginC, RowSpan: cl.RowSpan, ColSpan: cl.ColSpan, Text: cl.Text}
}

// AxisValue is a resolved axis level on one fact, with the header/row cell it
// came from. Inherited describes a value that was carried down from a
// rowspanning cell above the fact's own row.
type AxisValue struct {
	Axis      string  `json:"axis"`
	Level     int     `json:"level"`
	LevelName string  `json:"level_name,omitempty"`
	Value     string  `json:"value"`
	Cell      CellRef `json:"cell"`
	Inherited bool    `json:"inherited,omitempty"`
}

// Fact is one typed detail fact: exactly one declared detail row crossed with
// exactly one declared metric column.
type Fact struct {
	// ID is stable within a run: source row plus metric path.
	ID string `json:"id"`
	// SourceRow is the zero-based row index in the harvested table.
	SourceRow  int     `json:"source_row"`
	Marker     string  `json:"marker"`
	MarkerCell CellRef `json:"marker_cell"`

	// Row holds the row-axis levels in declaration order.
	Row []AxisValue `json:"row"`
	// Columns holds the column-axis levels in declaration order (from the
	// header tree, top to bottom).
	Columns []AxisValue `json:"columns,omitempty"`
	// Period is the canonicalised period when a period axis is declared.
	Period *Period `json:"period,omitempty"`

	Metric string     `json:"metric"`
	Path   []string   `json:"metric_path"`
	Type   MetricType `json:"type"`
	Unit   string     `json:"unit,omitempty"`
	Value  Value      `json:"value"`
	// Cell is the single rendered cell this value came from.
	Cell CellRef `json:"cell"`
	// HeaderCells are every header cell contributing to this column.
	HeaderCells []CellRef `json:"header_cells"`
}

// SourceCell is one rendered cell of a retained (non-fact) row.
type SourceCell struct {
	Column int     `json:"column"`
	Value  *Value  `json:"value,omitempty"`
	Cell   CellRef `json:"cell"`
}

// SourceRow is a subtotal, grand-total or ignored row. It is retained verbatim
// so the published totals can be audited, but it never becomes a fact and is
// never counted in any rollup.
type SourceRow struct {
	Row        int          `json:"row"`
	Class      string       `json:"class"`
	Marker     string       `json:"marker"`
	MarkerCell CellRef      `json:"marker_cell"`
	Cells      []SourceCell `json:"cells"`
}

// Extraction is the outcome of aligning a harvest to a declaration.
type Extraction struct {
	Facts       []Fact
	SourceRows  []SourceRow
	Diagnostics []Diagnostic
	Metrics     []metricColumn
	Columns     []columnPlan
	Grid        *grid
	ColumnAxis  []AxisValue // distinct column-axis combinations, in table order
	Periods     []Period
}

// metricColumn is one resolved metric leaf and the value columns it occupies.
type metricColumn struct {
	Metric  Metric
	Index   int
	Columns []int
}

// columnPlan describes one value column in the harvested table.
type columnPlan struct {
	Column int
	Metric Metric
	Header []CellRef
	// Column-level values (one per column-axis level).
	Levels []AxisValue
	Period *Period
}

// key identifies the column combination.
func (p columnPlan) key() string {
	parts := make([]string, len(p.Levels))
	for i, l := range p.Levels {
		parts[i] = normaliseText(l.Value)
	}
	return strings.Join(parts, "\x00")
}

// Extract aligns a harvested table to a declaration. Every ambiguity is an
// error: this function never guesses a column, a row class or a value.
func Extract(decl *Declaration, table *RawTable) (*Extraction, error) {
	if decl == nil || table == nil {
		return nil, failure(KindStructure, "extract", "declaration and table are both required")
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	g, err := buildGrid(table)
	if err != nil {
		return nil, err
	}
	if g.rows < decl.Layout.HeaderRows {
		return nil, failure(KindStructure, "extract",
			"the table has %d rows but %d rows are declared as header; the header tree itself is incomplete", g.rows, decl.Layout.HeaderRows)
	}

	valueStart := decl.rowAxisWidth() + 1
	if valueStart > g.cols {
		return nil, failure(KindStructure, "extract",
			"the declaration needs %d leading columns (%d axis levels plus the marker column) but the table has only %d columns", valueStart, decl.rowAxisWidth(), g.cols)
	}
	if decl.Layout.MarkerColumn >= valueStart {
		return nil, failure(KindSemantic, "extract",
			"layout.marker_column %d points into the value region (leading columns are 0..%d); a marker column cannot also be a metric column",
			decl.Layout.MarkerColumn, valueStart-1)
	}

	rowAxisColumns := make([]int, 0, decl.rowAxisWidth())
	for c := 0; c < valueStart; c++ {
		if c == decl.Layout.MarkerColumn {
			continue
		}
		rowAxisColumns = append(rowAxisColumns, c)
	}

	// --- resolve the column header tree ------------------------------------
	plans, colAxisValues, err := resolveColumns(decl, g, valueStart)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return nil, failure(KindStructure, "extract", "the table has no metric value columns to the right of the declared leading columns")
	}

	// --- complete and unique column grid -----------------------------------
	if err := checkColumnCompleteness(decl, plans); err != nil {
		return nil, err
	}

	// --- classify and read body rows ---------------------------------------
	out := &Extraction{Grid: g, Columns: plans, ColumnAxis: colAxisValues}
	byMetric := map[string][]int{}
	for i, m := range decl.Metrics {
		byMetric[pathKey(m.Path)] = append(byMetric[pathKey(m.Path)], i)
	}
	for i, m := range decl.Metrics {
		mc := metricColumn{Metric: m, Index: i}
		for _, p := range plans {
			if pathKey(p.Metric.Path) == pathKey(m.Path) {
				mc.Columns = append(mc.Columns, p.Column)
			}
		}
		out.Metrics = append(out.Metrics, mc)
	}

	seenPeriods := map[string]bool{}
	for _, p := range plans {
		if p.Period != nil && !seenPeriods[p.Period.Canonical] {
			seenPeriods[p.Period.Canonical] = true
			out.Periods = append(out.Periods, *p.Period)
		}
	}

	for r := decl.Layout.HeaderRows; r < g.rows; r++ {
		markerCell := g.at(r, decl.Layout.MarkerColumn)
		if markerCell.Hidden {
			return nil, failureAt(KindStructure, "extract", r, decl.Layout.MarkerColumn, markerCell.Text, "", "the row marker cell is hidden; the row cannot be classified")
		}
		class, err := decl.classify(markerCell.Text)
		if err != nil {
			if e, ok := err.(*Error); ok {
				e.Row = r
				e.Column = decl.Layout.MarkerColumn
				e.Cell = strings.TrimSpace(markerCell.Text)
			}
			return nil, err
		}

		switch class {
		case rowDetail:
			facts, err := readDetailRow(decl, g, r, rowAxisColumns, plans)
			if err != nil {
				return nil, err
			}
			out.Facts = append(out.Facts, facts...)
		case rowSubtotal, rowGrandTotal, rowIgnored:
			src, err := readSourceRow(decl, g, r, class, plans, rowAxisColumns)
			if err != nil {
				return nil, err
			}
			out.SourceRows = append(out.SourceRows, src)
		}
	}
	return out, nil
}

// rowAxisValues reads the row-axis levels of row r, inheriting rowspans.
func rowAxisValues(decl *Declaration, g *grid, r int, rowAxisColumns []int) ([]AxisValue, error) {
	var out []AxisValue
	pos := 0
	for _, axis := range decl.RowAxes() {
		for level := 0; level < axis.Levels; level++ {
			col := rowAxisColumns[pos]
			pos++
			cl := g.at(r, col)
			if cl == nil {
				return nil, failureAt(KindStructure, "read row axis", r, col, "", "", "row %d has no cell for axis %q level %d", r, axis.Name, level+1)
			}
			value := normaliseText(cl.Text)
			inherited := cl.OriginR != r
			levelName := ""
			if len(axis.LevelNames) == axis.Levels {
				levelName = axis.LevelNames[level]
			}
			out = append(out, AxisValue{
				Axis:      axis.Name,
				Level:     level + 1,
				LevelName: levelName,
				Value:     value,
				Cell:      refOf(cl),
				Inherited: inherited,
			})
		}
	}
	return out, nil
}

// readDetailRow turns one detail row into one fact per declared metric column.
func readDetailRow(decl *Declaration, g *grid, r int, rowAxisColumns []int, plans []columnPlan) ([]Fact, error) {
	axes, err := rowAxisValues(decl, g, r, rowAxisColumns)
	if err != nil {
		return nil, err
	}
	for _, av := range axes {
		if av.Value == "" {
			return nil, failureAt(KindSemantic, "read detail row", r, av.Cell.Column, av.Cell.Text, av.Axis,
				"detail row %d has no value for dimension %q level %d; a fact without its declared dimension cannot be published",
				r, av.Axis, av.Level)
		}
	}
	markerCell := g.at(r, decl.Layout.MarkerColumn)
	var period *Period
	facts := make([]Fact, 0, len(plans))
	for _, plan := range plans {
		cl := g.at(r, plan.Column)
		if cl == nil {
			return nil, failureAt(KindStructure, "read detail row", r, plan.Column, "", describePath(plan.Metric.Path), "row %d has no cell for metric %q", r, describePath(plan.Metric.Path))
		}
		if cl.Hidden {
			return nil, failureAt(KindStructure, "read detail row", r, plan.Column, cl.Text, describePath(plan.Metric.Path),
				"the value cell for %q is hidden; a hidden value cannot be published as a fact", describePath(plan.Metric.Path))
		}
		if cl.RowSpan != 1 || cl.ColSpan != 1 {
			return nil, failureAt(KindStructure, "read detail row", r, plan.Column, cl.Text, describePath(plan.Metric.Path),
				"the value cell for %q spans %d rows by %d columns; a merged value cell cannot be attributed to a single detail fact, and copying it would invent facts",
				describePath(plan.Metric.Path), cl.RowSpan, cl.ColSpan)
		}
		value, err := parseCell(cl.Text, plan.Metric.Type, plan.Metric.Scale, plan.Metric.Nulls)
		if err != nil {
			return nil, failureAt(KindType, "read detail row", r, plan.Column, cl.Text, describePath(plan.Metric.Path),
				"cannot type the cell for %q as %s: %s", describePath(plan.Metric.Path), plan.Metric.Type, err)
		}
		if plan.Period != nil {
			p := *plan.Period
			period = &p
		}
		fact := Fact{
			ID:          fmt.Sprintf("r%d|%s", r, describePath(plan.Metric.Path)),
			SourceRow:   r,
			Marker:      strings.TrimSpace(markerCell.Text),
			MarkerCell:  refOf(markerCell),
			Row:         append([]AxisValue(nil), axes...),
			Columns:     append([]AxisValue(nil), plan.Levels...),
			Period:      period,
			Metric:      plan.Metric.Name,
			Path:        append([]string(nil), plan.Metric.Path...),
			Type:        plan.Metric.Type,
			Unit:        plan.Metric.Unit,
			Value:       value,
			Cell:        refOf(cl),
			HeaderCells: append([]CellRef(nil), plan.Header...),
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// readSourceRow retains a subtotal / grand-total / ignored row verbatim.
func readSourceRow(decl *Declaration, g *grid, r int, class rowClass, plans []columnPlan, rowAxisColumns []int) (SourceRow, error) {
	markerCell := g.at(r, decl.Layout.MarkerColumn)
	out := SourceRow{
		Row:        r,
		Class:      string(class),
		Marker:     strings.TrimSpace(markerCell.Text),
		MarkerCell: refOf(markerCell),
	}
	seen := map[[2]int]bool{}
	record := func(col int) error {
		cl := g.at(r, col)
		if cl == nil {
			return failureAt(KindStructure, "read source row", r, col, "", "", "row %d has no cell at column %d", r, col)
		}
		origin := [2]int{cl.OriginR, cl.OriginC}
		if seen[origin] {
			return nil
		}
		seen[origin] = true
		out.Cells = append(out.Cells, SourceCell{Column: col, Cell: refOf(cl)})
		return nil
	}
	for _, col := range rowAxisColumns {
		if err := record(col); err != nil {
			return SourceRow{}, err
		}
	}
	for _, plan := range plans {
		if err := record(plan.Column); err != nil {
			return SourceRow{}, err
		}
	}
	return out, nil
}

// resolveColumns maps every value column to exactly one declared metric and one
// column-axis combination by matching the complete header path.
func resolveColumns(decl *Declaration, g *grid, valueStart int) ([]columnPlan, []AxisValue, error) {
	const op = "align columns"
	colAxes := decl.ColumnAxes()
	depth := decl.columnAxisDepth()

	// Flatten column-axis levels for naming.
	type levelRef struct {
		axis  Axis
		level int
	}
	var levels []levelRef
	for _, axis := range colAxes {
		for l := 0; l < axis.Levels; l++ {
			levels = append(levels, levelRef{axis, l})
		}
	}

	var plans []columnPlan
	var combos []AxisValue
	comboSeen := map[string]bool{}
	for c := valueStart; c < g.cols; c++ {
		segs := g.headerSegments(decl.Layout.HeaderRows, c)
		if len(segs) < depth+1 {
			return nil, nil, failureAt(KindStructure, op, 0, c, "", "",
				"value column %d has only %d header cells but the declaration needs %d; the header tree is ambiguous at this column",
				c, len(segs), depth+1)
		}
		levelCells := segs[:depth]
		leafPath := segs[depth:]

		var matched []Metric
		for _, m := range decl.Metrics {
			if len(m.Path) != len(leafPath) {
				continue
			}
			ok := true
			for i := range m.Path {
				if normaliseText(m.Path[i]) != normaliseText(leafPath[i].Text) {
					ok = false
					break
				}
			}
			if ok {
				matched = append(matched, m)
			}
		}
		if len(matched) == 0 {
			return nil, nil, failureAt(KindStructure, op, 0, c, "", "",
				"header path %q of value column %d matches no declared metric; nothing may be imported from an undeclared column",
				describePath(headerTexts(leafPath)), c)
		}
		if len(matched) > 1 {
			return nil, nil, failureAt(KindSemantic, op, 0, c, "", "",
				"header path %q of value column %d matches %d declared metrics; the declaration cannot distinguish them", describePath(headerTexts(leafPath)), c, len(matched))
		}
		plan := columnPlan{Column: c, Metric: matched[0]}
		var period *Period
		for i, cl := range levelCells {
			text := normaliseText(cl.Text)
			if text == "" {
				return nil, nil, failureAt(KindStructure, op, 0, c, "", "",
					"value column %d has an empty header cell at header row %d; the column's context cannot be resolved", c, cl.OriginR)
			}
			axis := levels[i].axis
			levelName := ""
			if len(axis.LevelNames) == axis.Levels {
				levelName = axis.LevelNames[levels[i].level]
			}
			av := AxisValue{
				Axis:      axis.Name,
				Level:     levels[i].level + 1,
				LevelName: levelName,
				Value:     text,
				Cell:      refOf(cl),
			}
			if axis.Role == AxisPeriod {
				p, perr := ParsePeriod(text, axis.PeriodFormat)
				if perr != nil {
					return nil, nil, failureAt(KindSemantic, op, 0, c, cl.Text, axis.Name,
						"period axis %q cannot align column %d: %s", axis.Name, c, perr)
				}
				period = &p
			}
			plan.Levels = append(plan.Levels, av)
		}
		plan.Period = period
		for _, cl := range segs {
			plan.Header = append(plan.Header, refOf(cl))
		}
		plans = append(plans, plan)

		if !comboSeen[plan.key()] {
			comboSeen[plan.key()] = true
			if len(plan.Levels) > 0 {
				combos = append(combos, plan.Levels...)
			}
		}
	}
	if len(colAxes) == 0 {
		combos = nil
	}
	return plans, combos, nil
}

func headerTexts(cells []*cell) []string {
	out := make([]string, len(cells))
	for i, cl := range cells {
		out[i] = cl.Text
	}
	return out
}

// checkColumnCompleteness requires every declared metric to appear exactly once
// for every column combination. A missing or duplicated pair is ambiguous: it
// would silently misalign a period to the wrong metric.
func checkColumnCompleteness(decl *Declaration, plans []columnPlan) error {
	type pair struct{ combo, metric string }
	seen := map[pair][]int{}
	combos := map[string]bool{}
	comboOrder := []string{}
	metricsByCombo := map[string]map[string]bool{}
	for _, p := range plans {
		k := p.key()
		if !combos[k] {
			combos[k] = true
			comboOrder = append(comboOrder, k)
			metricsByCombo[k] = map[string]bool{}
		}
		metricsByCombo[k][pathKey(p.Metric.Path)] = true
		key := pair{p.key(), pathKey(p.Metric.Path)}
		seen[key] = append(seen[key], p.Column)
	}
	for _, p := range plans {
		key := pair{p.key(), pathKey(p.Metric.Path)}
		if len(seen[key]) > 1 {
			return failureAt(KindSemantic, "align columns", 0, p.Column, "", describePath(p.Metric.Path),
				"metric %q appears more than once under the same column context (%v); same-named leaves may never be merged or double-counted",
				describePath(p.Metric.Path), seen[key])
		}
	}
	for _, combo := range comboOrder {
		for _, m := range decl.Metrics {
			if !metricsByCombo[combo][pathKey(m.Path)] {
				return failure(KindSemantic, "align columns",
					"column context %q has no column for declared metric %q; the table and the declaration disagree", combo, describePath(m.Path))
			}
		}
	}
	return nil
}
