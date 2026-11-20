package pivotreport_test

import (
	"io/ioutil"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sclevine/agouti/pivotreport"
)

func totalFor(summary *pivotreport.Summary, column int) pivotreport.MetricTotal {
	for _, t := range summary.Total {
		if t.Column == column {
			return t
		}
	}
	Fail("no total for column")
	return pivotreport.MetricTotal{}
}

var _ = Describe("Rollups", func() {
	It("sums only detail rows, never subtotal or grand-total rows", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileOff)
		// Detail rows: 10+0+5 in 2026-01, 20+NULL+5 in 2026-02.
		Expect(totalFor(summary, 4).Total.Int).To(Equal("15"))
		Expect(totalFor(summary, 6).Total.Int).To(Equal("25"))
		Expect(totalFor(summary, 4).NonNull).To(Equal(3))
		Expect(totalFor(summary, 6).NonNull).To(Equal(2))
	})

	It("counts missing values separately from zero", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileOff)
		Expect(totalFor(summary, 6).Null).To(Equal(1))
		Expect(totalFor(summary, 4).Null).To(Equal(0))
		for _, missing := range summary.Missing {
			if missing.Column == 6 {
				Expect(missing.Null).To(Equal(1))
				Expect(missing.Total).To(Equal(3))
			}
		}
	})

	It("keeps cents exact across the rollup", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileOff)
		Expect(totalFor(summary, 5).Total.Cents).To(Equal("150"))
		Expect(totalFor(summary, 7).Total.Cents).To(Equal("250"))
	})

	It("groups by dimension without double counting", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileOff)
		var regionTotal int64
		for _, g := range summary.ByDimension {
			if g.Axis != "区域" {
				continue
			}
			for _, t := range g.Totals {
				if t.Column == 4 {
					var n int64
					Expect(parseInt(t.Total.Int, &n)).To(Succeed())
					regionTotal += n
				}
			}
		}
		Expect(regionTotal).To(BeEquivalentTo(15))
	})

	It("groups by canonical period, not raw label", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileOff)
		Expect(summary.ByPeriod).To(HaveLen(2))
		Expect(summary.ByPeriod[0].Value).To(Equal("2026-01"))
		Expect(summary.ByPeriod[1].Value).To(Equal("2026-02"))
	})

	It("reconciles matching published totals without adding them", func() {
		_, summary := mustBuild(baseDeclaration(), baseTable(), pivotreport.ReconcileWarn)
		Expect(summary.SourceTotals).To(HaveLen(2))
		Expect(summary.Reconciliation).NotTo(BeEmpty())
		for _, r := range summary.Reconciliation {
			Expect(r.Agrees).To(BeTrue(), "row %d column %d: %s", r.Row, r.Column, r.Reason)
		}
		Expect(summary.Diagnostics).To(BeEmpty())
	})

	It("warns when a published subtotal disagrees with its detail rows", func() {
		b := baseTable()
		// Tamper: the 华东 subtotal claims 99 units.
		b.Rows[4].Cells[4].Text = "99"
		_, summary := mustBuild(baseDeclaration(), b, pivotreport.ReconcileWarn)
		Expect(summary.Diagnostics).NotTo(BeEmpty())
		Expect(summary.Diagnostics[0].Code).To(Equal("reconciliation_mismatch"))
		// The published value is retained verbatim but was not counted.
		Expect(totalFor(summary, 4).Total.Int).To(Equal("15"))
	})

	It("errors when a published subtotal disagrees and the mode is error", func() {
		b := baseTable()
		b.Rows[4].Cells[4].Text = "99"
		_, err := pivotreport.Build(pivotreport.Config{Declaration: baseDeclaration(), Table: b, Reconcile: pivotreport.ReconcileError})
		expectKind(err, pivotreport.KindSemantic)
	})

	It("does not sum a metric declared aggregate:none", func() {
		d := baseDeclaration()
		d.Metrics[1].Aggregate = pivotreport.AggNone
		_, summary := mustBuild(d, baseTable(), pivotreport.ReconcileOff)
		t := totalFor(summary, 5)
		Expect(t.Aggregated).To(BeFalse())
		Expect(t.Total.IsNull()).To(BeTrue())
		Expect(t.NonNull).To(Equal(3))
	})

	It("returns typed empty totals for a report with no detail rows", func() {
		b := newTable()
		b.row(td("区域"), td("城市"), td("产品"), td("行类型"), spans("2026-01", 1, 2))
		b.row(td(""), td(""), td(""), td(""), td("件数"), td("金额"))
		b.row(td("合计"), td(""), td(""), td("总计"), td("0"), td("0.00"))
		detail, summary := mustBuild(baseDeclaration(), b.table(), pivotreport.ReconcileOff)
		Expect(summary.Empty).To(BeTrue())
		Expect(summary.DetailRows).To(Equal(0))
		Expect(detail.Empty).To(BeTrue())
		for _, t := range summary.Total {
			Expect(t.Total.IsNull()).To(BeTrue())
			Expect(t.NonNull).To(Equal(0))
		}
	})
})

var _ = Describe("Output files", func() {
	It("writes detail and summary that can be re-opened and re-summed", func() {
		decl := baseDeclaration()
		res, err := pivotreport.Build(pivotreport.Config{Declaration: decl, Table: baseTable(), Reconcile: pivotreport.ReconcileWarn})
		Expect(err).NotTo(HaveOccurred())

		dir := tempDir()
		paths := pivotreport.OutputPaths{
			Detail:  filepath.Join(dir, "detail.json"),
			Summary: filepath.Join(dir, "summary.json"),
			Raw:     filepath.Join(dir, "harvest.json"),
		}
		Expect(res.Write(paths)).To(Succeed())

		detail, err := pivotreport.ReadDetail(paths.Detail)
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Facts).To(HaveLen(12))

		resummed, err := pivotreport.SumFacts(decl, detail)
		Expect(err).NotTo(HaveOccurred())
		Expect(totalFor(resummed, 4).Total.Int).To(Equal(totalFor(res.Summary, 4).Total.Int))
		Expect(totalFor(resummed, 7).Total.Cents).To(Equal(totalFor(res.Summary, 7).Total.Cents))

		raw, err := pivotreport.LoadRaw(paths.Raw)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.Rows).To(HaveLen(7))

		// The files must be re-readable JSON with a version envelope.
		data, err := ioutil.ReadFile(paths.Summary)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"version": 1`))
	})

	It("creates missing output directories for every artefact", func() {
		res, err := pivotreport.Build(pivotreport.Config{Declaration: baseDeclaration(), Table: baseTable(), Reconcile: pivotreport.ReconcileOff})
		Expect(err).NotTo(HaveOccurred())
		// A fresh nested directory: neither the detail, summary nor harvest file
		// may fail because its parent does not exist yet.
		dir := filepath.Join(tempDir(), "nested", "run-1")
		paths := pivotreport.OutputPaths{
			Detail:  filepath.Join(dir, "detail.json"),
			Summary: filepath.Join(dir, "summary.json"),
			Raw:     filepath.Join(dir, "harvest.json"),
		}
		Expect(res.Write(paths)).To(Succeed())
		for _, p := range []string{paths.Detail, paths.Summary, paths.Raw} {
			_, err := os.Stat(p)
			Expect(err).NotTo(HaveOccurred(), p)
		}
	})

	It("rejects a detail file from another version", func() {
		dir := tempDir()
		path := filepath.Join(dir, "detail.json")
		Expect(ioutil.WriteFile(path, []byte(`{"version":2,"declaration_version":2,"report":"x"}`), 0644)).To(Succeed())
		_, err := pivotreport.ReadDetail(path)
		expectKind(err, pivotreport.KindVersion)
	})

	It("rejects a harvest from another version", func() {
		dir := tempDir()
		path := filepath.Join(dir, "harvest.json")
		Expect(ioutil.WriteFile(path, []byte(`{"version":99,"table":{"rows":[]}}`), 0644)).To(Succeed())
		_, err := pivotreport.LoadRaw(path)
		expectKind(err, pivotreport.KindVersion)
	})

	It("rejects a detail fact whose value does not match its column type", func() {
		data := `{
		  "version":1,"declaration_version":1,"report":"x","source":{},
		  "axes":[],"metrics":[],
		  "columns":[{"column":4,"metric":"件数","metric_path":["件数"],"type":"int","header_path":["件数"],"header_cells":[]}],
		  "facts":[{"id":"r1","source_row":1,"marker":"明细","marker_cell":{},"row":[],"metric":"件数","metric_path":["件数"],
		            "type":"int","value":{"kind":"decimal","decimal":"1.5"},"cell":{"column":4},"header_cells":[]}],
		  "detail_rows":1,"empty":false
		}`
		_, err := pivotreport.DecodeDetail([]byte(data))
		expectKind(err, pivotreport.KindType)
	})
})

func parseInt(s string, out *int64) error {
	var n int64
	neg := false
	for i, r := range s {
		if i == 0 && r == '-' {
			neg = true
			continue
		}
		if r < '0' || r > '9' {
			return filepath.ErrBadPattern
		}
		n = n*10 + int64(r-'0')
	}
	if neg {
		n = -n
	}
	*out = n
	return nil
}
