package pivotreport

import "fmt"

// cell is a single table cell after spans have been resolved onto a grid.
type cell struct {
	Text    string
	Tag     string
	OriginR int
	OriginC int
	RowSpan int
	ColSpan int
	Hidden  bool
}

// grid models the resolved table: cells[r][c] is the cell covering that slot.
type grid struct {
	rows       int
	cols       int
	cells      [][]*cell
	rowClasses []string
}

func (g *grid) at(r, c int) *cell { return g.cells[r][c] }

// buildGrid expands the raw rows into a rectangular grid, honouring rowspan and
// colspan exactly as the browser lays them out. Every structural defect that
// would make a cell ambiguous is an error: overlapping spans, spans running
// past the table edge, and ragged rows.
func buildGrid(table *RawTable) (*grid, error) {
	const op = "expand spans"
	if table == nil {
		return nil, failure(KindStructure, op, "no table to expand")
	}
	if len(table.Rows) == 0 {
		return &grid{}, nil
	}

	occupied := map[[2]int]*cell{}
	g := &grid{rows: len(table.Rows), rowClasses: make([]string, len(table.Rows))}
	maxCol := 0

	for r, row := range table.Rows {
		g.rowClasses[r] = row.Class
		cursor := 0
		for idx, raw := range row.Cells {
			if raw == nil {
				return nil, failureAt(KindStructure, op, r, cursor, "", "", "cell %d of row %d is null", idx, r)
			}
			for occupied[[2]int{r, cursor}] != nil {
				cursor++
			}
			rowSpan := raw.RowSpan
			if rowSpan == 0 {
				rowSpan = len(table.Rows) - r
			}
			colSpan := raw.ColSpan
			if colSpan == 0 {
				colSpan = 1
			}
			if rowSpan < 1 {
				return nil, failureAt(KindStructure, op, r, cursor, raw.Text, "", "cell %d of row %d declares rowspan %d", idx, r, raw.RowSpan)
			}
			if colSpan < 1 {
				return nil, failureAt(KindStructure, op, r, cursor, raw.Text, "", "cell %d of row %d declares colspan %d", idx, r, raw.ColSpan)
			}
			if r+rowSpan > len(table.Rows) {
				return nil, failureAt(KindStructure, op, r, cursor, raw.Text, "", "rowspan %d runs past the last table row (%d of %d); the merged cell cannot be attributed", rowSpan, r+rowSpan, len(table.Rows))
			}
			cl := &cell{
				Text:    raw.Text,
				Tag:     raw.Tag,
				OriginR: r,
				OriginC: cursor,
				RowSpan: rowSpan,
				ColSpan: colSpan,
				Hidden:  raw.Hidden,
			}
			for rr := r; rr < r+rowSpan; rr++ {
				for cc := cursor; cc < cursor+colSpan; cc++ {
					if other, exists := occupied[[2]int{rr, cc}]; exists {
						return nil, failureAt(KindStructure, op, rr, cc, raw.Text, "",
							"two cells claim row %d, column %d: this cell and the cell originating at row %d, column %d; the table is not a valid layout",
							rr, cc, other.OriginR, other.OriginC)
					}
					occupied[[2]int{rr, cc}] = cl
				}
			}
			if cursor+colSpan > maxCol {
				maxCol = cursor + colSpan
			}
			cursor += colSpan
		}
	}

	g.cols = maxCol
	g.cells = make([][]*cell, g.rows)
	for r := 0; r < g.rows; r++ {
		g.cells[r] = make([]*cell, g.cols)
	}
	for pos, cl := range occupied {
		g.cells[pos[0]][pos[1]] = cl
	}
	for r := 0; r < g.rows; r++ {
		for c := 0; c < g.cols; c++ {
			if g.cells[r][c] == nil {
				return nil, failureAt(KindStructure, op, r, c, "", "", "row %d has no cell at column %d (the rows are ragged); every row must cover all %d columns", r, c, g.cols)
			}
		}
	}
	return g, nil
}

// headerSegments returns the header tree segments above a value column: one
// entry per header row where a cell originates in this column. Continuations of
// a rowspan are skipped, which is exactly how a spanning dimension is inherited
// by every column it covers.
func (g *grid) headerSegments(headerRows, c int) []*cell {
	var out []*cell
	for r := 0; r < headerRows && r < g.rows; r++ {
		cl := g.at(r, c)
		if cl == nil || cl.OriginR != r {
			continue
		}
		out = append(out, cl)
	}
	return out
}

func describeCell(cl *cell) string {
	if cl == nil {
		return "<none>"
	}
	return fmt.Sprintf("r%d c%d (%dx%d) %q", cl.OriginR, cl.OriginC, cl.RowSpan, cl.ColSpan, cl.Text)
}
