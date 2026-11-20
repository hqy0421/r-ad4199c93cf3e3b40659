package pivotreport_test

import (
	"io/ioutil"
	"path/filepath"

	. "github.com/onsi/gomega"
	"github.com/sclevine/agouti/pivotreport"
)

// tableBuilder makes small, explicit harvested tables. Spans are declared
// exactly as written; nothing is auto-expanded.
type tableBuilder struct {
	rows  []*pivotreport.RawRow
	class string
}

func newTable() *tableBuilder { return &tableBuilder{} }

type raw struct {
	text    string
	rowSpan int
	colSpan int
	hidden  bool
}

func td(text string) raw                { return raw{text: text, rowSpan: 1, colSpan: 1} }
func spans(text string, rs, cs int) raw { return raw{text: text, rowSpan: rs, colSpan: cs} }

func (b *tableBuilder) row(cells ...raw) *tableBuilder {
	r := &pivotreport.RawRow{Class: b.class}
	for _, c := range cells {
		rs, cs := c.rowSpan, c.colSpan
		if rs == 0 {
			rs = 1
		}
		if cs == 0 {
			cs = 1
		}
		r.Cells = append(r.Cells, &pivotreport.RawCell{Text: c.text, Tag: "td", RowSpan: rs, ColSpan: cs, Hidden: c.hidden, Row: len(b.rows), Column: len(r.Cells)})
	}
	b.rows = append(b.rows, r)
	b.class = ""
	return b
}

func (b *tableBuilder) withClass(class string) *tableBuilder { b.class = class; return b }

func (b *tableBuilder) table() *pivotreport.RawTable {
	return &pivotreport.RawTable{Caption: "test", Rows: b.rows}
}

// baseDeclaration is the canonical fixture: row axes region/city/product with a
// marker column, then a period axis and a two-level metric tree in the header.
func baseDeclaration() *pivotreport.Declaration {
	return &pivotreport.Declaration{
		Version:       pivotreport.SchemaVersion,
		Name:          "test report",
		TableSelector: "#pivot",
		Axes: []pivotreport.Axis{
			{Name: "区域", Role: pivotreport.AxisDimension, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "城市", Role: pivotreport.AxisDimension, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "产品", Role: pivotreport.AxisLabel, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "期间", Role: pivotreport.AxisPeriod, Orientation: pivotreport.OrientationColumn, Levels: 1, PeriodFormat: "yyyy-MM"},
		},
		Metrics: []pivotreport.Metric{
			{Name: "件数", Path: []string{"件数"}, Type: pivotreport.TypeInt, Nulls: []string{"NULL"}},
			{Name: "金额", Path: []string{"金额"}, Type: pivotreport.TypeCents, Nulls: []string{"NULL"}},
		},
		Layout: pivotreport.Layout{
			HeaderRows:   2,
			MarkerColumn: 3,
			Markers: pivotreport.Markers{
				Detail:     []string{"明细"},
				Subtotal:   []string{"小计"},
				GrandTotal: []string{"总计"},
			},
		},
	}
}

// baseTable has two periods (2026-01 and 2026-02), two region groups, one
// subtotal and one grand total. The 2026-01 detail row "B 款" has a genuine
// zero and a NULL, so zero/NULL confusion would show up immediately.
func baseTable() *pivotreport.RawTable {
	b := newTable()
	b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 2), spans("2026-02", 1, 2))
	b.row(td(""), td(""), td(""), td(""), td("件数"), td("金额"), td("件数"), td("金额"))
	b.row(spans("华东", 2, 1), spans("上海", 2, 1), td("A 款"), td("明细"),
		td("10"), td("1.00"), td("20"), td("2.00"))
	b.row(td("B 款"), td("明细"),
		td("0"), td("0.00"), td("NULL"), td("0.00"))
	b.row(td("华东小计"), td(""), td(""), td("小计"),
		td("10"), td("1.00"), td("20"), td("2.00"))
	b.row(td("华南"), td("广州"), td("A 款"), td("明细"),
		td("5"), td("0.50"), td("5"), td("0.50"))
	b.row(td("合计"), td(""), td(""), td("总计"),
		td("15"), td("1.50"), td("25"), td("2.50"))
	return b.table()
}

// quoteDeclaration declares one period axis plus a 销量/退货 measure level, so
// two leaves named 件数 exist under different paths.
func quoteDeclaration() *pivotreport.Declaration {
	d := baseDeclaration()
	d.Axes[3] = pivotreport.Axis{Name: "期间", Role: pivotreport.AxisPeriod, Orientation: pivotreport.OrientationColumn, Levels: 1, PeriodFormat: "yyyy-MM"}
	d.Metrics = []pivotreport.Metric{
		{Name: "件数", Path: []string{"销量", "件数"}, Type: pivotreport.TypeInt},
		{Name: "金额", Path: []string{"销量", "金额"}, Type: pivotreport.TypeCents},
		{Name: "件数", Path: []string{"退货", "件数"}, Type: pivotreport.TypeInt},
		{Name: "金额", Path: []string{"退货", "金额"}, Type: pivotreport.TypeCents},
	}
	d.Layout.HeaderRows = 3
	return d
}

func quoteTable() *pivotreport.RawTable {
	b := newTable()
	b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 4), spans("2026-02", 1, 4))
	b.row(td(""), td(""), td(""), td(""), spans("销量", 1, 2), spans("退货", 1, 2), spans("销量", 1, 2), spans("退货", 1, 2))
	b.row(td(""), td(""), td(""), td(""),
		td("件数"), td("金额"), td("件数"), td("金额"),
		td("件数"), td("金额"), td("件数"), td("金额"))
	b.row(td("华东"), td("上海"), td("A 款"), td("明细"),
		td("1"), td("0.01"), td("2"), td("0.02"), td("3"), td("0.03"), td("4"), td("0.04"))
	return b.table()
}

func mustExtract(decl *pivotreport.Declaration, table *pivotreport.RawTable) *pivotreport.Extraction {
	x, err := pivotreport.Extract(decl, table)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return x
}

func mustBuild(decl *pivotreport.Declaration, table *pivotreport.RawTable, mode pivotreport.ReconcileMode) (*pivotreport.Detail, *pivotreport.Summary) {
	res, err := pivotreport.Build(pivotreport.Config{Declaration: decl, Table: table, Reconcile: mode})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return res.Detail, res.Summary
}

func expectKind(err error, kind pivotreport.ErrorKind) {
	ExpectWithOffset(1, err).To(HaveOccurred())
	ExpectWithOffset(1, pivotreport.KindOf(err)).To(Equal(kind), "error was: %v", err)
}

func tempDir() string {
	dir, err := ioutil.TempDir("", "pivotreport-test")
	Expect(err).NotTo(HaveOccurred())
	return dir
}

func fixturePath(name string) string {
	return filepath.Join("testdata", name)
}
