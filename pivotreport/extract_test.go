package pivotreport_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sclevine/agouti/pivotreport"
)

var _ = Describe("Span resolution", func() {
	It("expands rowspans and colspans exactly", func() {
		b := newTable()
		b.row(spans("R", 2, 1), spans("H", 1, 2))
		b.row(td("A"), td("B"))
		rows, cols, err := pivotreport.GridDimsForTest(b.table())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal(2))
		Expect(cols).To(Equal(3))
	})

	It("rejects overlapping spans instead of picking a winner", func() {
		b := newTable()
		b.row(spans("R", 2, 1), td("x"))
		b.row(td("covers R"), td("y"))
		_, _, err := pivotreport.GridDimsForTest(b.table())
		expectKind(err, pivotreport.KindStructure)
	})

	It("rejects a rowspan that runs past the table", func() {
		b := newTable()
		b.row(spans("R", 5, 1), td("x"))
		b.row(td("y"))
		_, _, err := pivotreport.GridDimsForTest(b.table())
		expectKind(err, pivotreport.KindStructure)
	})

	It("rejects ragged rows", func() {
		b := newTable()
		b.row(td("a"), td("b"), td("c"))
		b.row(td("d"), td("e"))
		_, _, err := pivotreport.GridDimsForTest(b.table())
		expectKind(err, pivotreport.KindStructure)
	})
})

var _ = Describe("Header alignment", func() {
	var decl *pivotreport.Declaration

	BeforeEach(func() { decl = baseDeclaration() })

	It("aligns periods and metrics from the full header path", func() {
		x := mustExtract(decl, baseTable())
		Expect(x.Columns).To(HaveLen(4))
		Expect(x.Columns[0].Period.Canonical).To(Equal("2026-01"))
		Expect(x.Columns[0].Metric.Path).To(Equal([]string{"件数"}))
		Expect(x.Columns[2].Period.Canonical).To(Equal("2026-02"))
		Expect(x.Columns[2].Metric.Path).To(Equal([]string{"件数"}))
	})

	It("inherits a colspanned period rather than treating it as one column", func() {
		// "2026-01" spans two metric columns; both must resolve to that period.
		x := mustExtract(decl, baseTable())
		Expect(x.Columns[0].Period.Canonical).To(Equal(x.Columns[1].Period.Canonical))
		Expect(x.Columns[0].Header).To(HaveLen(2))
	})

	It("never merges same-named leaves with different paths", func() {
		d := quoteDeclaration()
		x, err := pivotreport.Extract(d, quoteTable())
		Expect(err).NotTo(HaveOccurred())
		Expect(x.Columns).To(HaveLen(8))
		Expect(x.Facts).To(HaveLen(8))
		// Column 2 is 退货/件数 under 2026-01, not 销量/件数.
		Expect(x.Columns[2].Metric.Path).To(Equal([]string{"退货", "件数"}))
		Expect(x.Facts[2].Value.Int).To(Equal("2"))
		// The two 件数 leaves stay separate facts with separate values.
		Expect(x.Facts[0].Path).To(Equal([]string{"销量", "件数"}))
		Expect(x.Facts[0].Value.Int).To(Equal("1"))
	})

	It("rejects a duplicate declared metric path", func() {
		d := baseDeclaration()
		d.Metrics = append(d.Metrics, pivotreport.Metric{Name: "件数", Path: []string{"件数"}, Type: pivotreport.TypeInt})
		expectKind(d.Validate(), pivotreport.KindSemantic)
	})

	It("rejects a header path that matches no declared metric", func() {
		b := newTable()
		b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 2), spans("2026-02", 1, 2))
		b.row(td(""), td(""), td(""), td(""), td("件数"), td("毛利"), td("件数"), td("金额"))
		b.row(td("华东"), td("上海"), td("A"), td("明细"),
			td("1"), td("9"), td("1"), td("1.00"))
		_, err := pivotreport.Extract(decl, b.table())
		expectKind(err, pivotreport.KindStructure)
	})

	It("rejects an undeclared period format instead of guessing", func() {
		d := baseDeclaration()
		b := baseTable()
		b.Rows[0].Cells[4].Text = "Q1"
		_, err := pivotreport.Extract(d, b)
		expectKind(err, pivotreport.KindSemantic)
	})

	It("requires every declared metric in every column context", func() {
		b := newTable()
		b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 2), spans("2026-02", 1, 2))
		b.row(td(""), td(""), td(""), td(""), td("件数"), td("金额"), td("件数"), td("件数"))
		b.row(td("华东"), td("上海"), td("A"), td("明细"),
			td("1"), td("1.00"), td("1"), td("1"))
		_, err := pivotreport.Extract(decl, b.table())
		expectKind(err, pivotreport.KindSemantic)
	})
})

var _ = Describe("Row classification", func() {
	It("classifies ONLY declared detail rows as facts", func() {
		x := mustExtract(baseDeclaration(), baseTable())
		Expect(x.Facts).To(HaveLen(12)) // 3 detail rows x 4 metric columns
	})

	It("retains subtotal and grand-total rows as source rows", func() {
		x := mustExtract(baseDeclaration(), baseTable())
		Expect(x.SourceRows).To(HaveLen(2))
		Expect(x.SourceRows[0].Class).To(Equal("subtotal"))
		Expect(x.SourceRows[1].Class).To(Equal("grand_total"))
	})

	It("fails on an undeclared row marker", func() {
		b := newTable()
		b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 2))
		b.row(td(""), td(""), td(""), td(""), td("件数"), td("金额"))
		b.row(td("华东"), td("上海"), td("A 款"), td("其它"), td("1"), td("0.01"))
		_, err := pivotreport.Extract(baseDeclaration(), b.table())
		expectKind(err, pivotreport.KindSemantic)
	})

	It("inherits rowspanning dimensions for every covered row", func() {
		x := mustExtract(baseDeclaration(), baseTable())
		// Rows 2 and 3 share 上海 via a rowspan.
		var second []string
		for _, av := range x.Facts[2].Row {
			second = append(second, av.Axis+"="+av.Value)
		}
		Expect(second).To(ContainElement("城市=上海"))
		Expect(second).To(ContainElement("区域=华东"))
		// Row 3's facts start at index 4.
		for _, av := range x.Facts[4].Row {
			if av.Axis == "城市" {
				Expect(av.Value).To(Equal("上海"))
				Expect(av.Inherited).To(BeTrue())
			}
		}
	})
})
