package pivotreport_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/sclevine/agouti"
	"github.com/sclevine/agouti/pivotreport"
)

// The live specs drive a real PhantomJS browser through agouti against a local
// report that only renders after a real sign-in. They are skipped when no
// PhantomJS binary is available. The fixture is served over HTTP rather than
// file:// because a real session (cookies/session storage) needs a real origin.
var _ = Describe("Live authenticated pivot report", func() {
	var (
		driver *agouti.WebDriver
		page   *agouti.Page
		server *httptest.Server
		url    string
	)

	BeforeEach(func() {
		if _, err := os.Stat(phantomBinary()); err != nil {
			Skip("phantomjs is not installed")
		}
		driver = agouti.PhantomJS(agouti.Timeout(60))
		Expect(driver.Start()).To(Succeed())
		var err error
		page, err = driver.NewPage()
		Expect(err).NotTo(HaveOccurred())
		server = httptest.NewServer(http.FileServer(http.Dir(filepath.Join("testdata"))))
		url = server.URL + "/pivot_report.html"
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		if driver == nil {
			return
		}
		page.Destroy()
		driver.Stop()
	})

	It("refuses to harvest a hidden table before the session is authenticated", func() {
		Expect(page.Navigate(url)).To(Succeed())
		// The table exists in the DOM but sits behind the login panel; the
		// harvest must not silently publish a hidden report.
		_, err := pivotreport.HarvestTable(page, "#pivot")
		expectKind(err, pivotreport.KindHarvest)
	})

	It("rejects a failed sign-in instead of publishing an empty report", func() {
		err := pivotreport.SignIn(page, fixtureLogin(url, "analyst", "wrong-password"))
		expectKind(err, pivotreport.KindLogin)
	})

	It("harvests a complete report from the authenticated page", func() {
		err := pivotreport.SignIn(page, fixtureLogin(url, "analyst", "s3cret-pass"))
		Expect(err).NotTo(HaveOccurred())

		decl := fixtureDeclaration()
		res, err := pivotreport.Build(pivotreport.Config{
			Declaration: decl,
			Page:        page,
			ReportURL:   url,
			Reconcile:   pivotreport.ReconcileError,
		})
		Expect(err).NotTo(HaveOccurred())

		// Independent oracle: facts, typed totals, missing counts.
		Expect(res.Summary.DetailRows).To(Equal(9))
		Expect(res.Summary.Facts).To(Equal(90))
		Expect(res.Summary.Columns).To(Equal(10))

		// Q1 columns 4..8, Q2 columns 9..13.
		assertTotal(res.Summary, 4, pivotreport.KindInt, "5350", 9, 0)
		assertTotal(res.Summary, 6, pivotreport.KindCents, "16050050", 9, 0)
		assertTotal(res.Summary, 7, pivotreport.KindInt, "179", 9, 0)
		assertTotal(res.Summary, 8, pivotreport.KindCents, "537000", 9, 0)
		assertTotal(res.Summary, 9, pivotreport.KindInt, "4900", 8, 1)
		assertTotal(res.Summary, 11, pivotreport.KindCents, "14700025", 8, 1)
		assertTotal(res.Summary, 12, pivotreport.KindInt, "32", 9, 0)
		assertTotal(res.Summary, 13, pivotreport.KindCents, "96000", 9, 0)

		// Both quarters are present; periods are canonicalised from Chinese
		// labels, not guessed from the raw text.
		Expect(res.Summary.ByPeriod).To(HaveLen(2))
		Expect(res.Summary.ByPeriod[0].Value).To(Equal("2026-Q1"))
		Expect(res.Summary.ByPeriod[1].Value).To(Equal("2026-Q2"))

		// Published subtotals and the grand total are compared, never counted.
		Expect(res.Summary.Reconciliation).NotTo(BeEmpty())
		for _, r := range res.Summary.Reconciliation {
			Expect(r.Agrees).To(BeTrue(), "row %d col %d: %s", r.Row, r.Column, r.Reason)
		}
		Expect(res.Summary.Diagnostics).To(BeEmpty())

		// Two detail rows carry identical business values; both must survive as
		// separate facts with distinct source rows.
		perRow := map[int]pivotreport.Value{}
		for _, f := range res.Detail.Facts {
			if f.Cell.Column == 4 {
				perRow[f.SourceRow] = f.Value
			}
		}
		Expect(perRow).To(HaveLen(9))
		var identical int
		for _, v := range perRow {
			if v.Int == "700" {
				identical++
			}
		}
		Expect(identical).To(BeNumerically(">=", 2))

		// Every fact points at the exact rendered cell and header cells.
		for _, f := range res.Detail.Facts {
			Expect(f.Cell.Text).NotTo(BeEmpty())
			Expect(f.HeaderCells).NotTo(BeEmpty())
			Expect(f.Period).NotTo(BeNil())
		}

		// A merged dimension cell is inherited, and its origin span is recorded.
		var inherited int
		for _, f := range res.Detail.Facts {
			for _, av := range f.Row {
				if av.Axis == "区域" && av.Cell.RowSpan > 1 {
					Expect(av.Cell.ColSpan).To(Equal(1))
					if av.Inherited {
						inherited++
					}
				}
			}
		}
		Expect(inherited).To(BeNumerically(">", 0))

		// Files are reopened and re-summed to prove they are readable.
		dir := tempDir()
		paths := pivotreport.OutputPaths{
			Detail:  filepath.Join(dir, "detail.json"),
			Summary: filepath.Join(dir, "summary.json"),
			Raw:     filepath.Join(dir, "harvest.json"),
		}
		Expect(res.Write(paths)).To(Succeed())
		detail, err := pivotreport.ReadDetail(paths.Detail)
		Expect(err).NotTo(HaveOccurred())
		Expect(detail.Facts).To(HaveLen(90))
		reopened, err := pivotreport.ReadSummary(paths.Summary)
		Expect(err).NotTo(HaveOccurred())
		Expect(totalFor(reopened, 4).Total.Int).To(Equal("5350"))
		recomputed, err := pivotreport.SumFacts(decl, detail)
		Expect(err).NotTo(HaveOccurred())
		Expect(totalFor(recomputed, 11).Total.Cents).To(Equal("14700025"))
	})

	It("harvests an empty report with explicit types and keeps NULL distinct from 0", func() {
		Expect(pivotreport.SignIn(page, fixtureLogin(url, "analyst", "s3cret-pass"))).To(Succeed())
		decl := emptyDeclaration()
		res, err := pivotreport.Build(pivotreport.Config{Declaration: decl, Page: page, Reconcile: pivotreport.ReconcileWarn})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Summary.Empty).To(BeTrue())
		Expect(res.Detail.Facts).To(BeEmpty())
		for _, t := range res.Summary.Total {
			Expect(t.Total.IsNull()).To(BeTrue())
			Expect(t.NonNull).To(Equal(0))
			Expect(t.Type).NotTo(BeEmpty())
		}
		// The published "0" / "0.00" grand total is retained verbatim, but it is
		// NOT equal to the computed NULL from zero detail rows: the difference
		// is surfaced rather than silently reconciled.
		Expect(res.Summary.SourceTotals).To(HaveLen(1))
		Expect(res.Summary.Reconciliation).NotTo(BeEmpty())
		for _, r := range res.Summary.Reconciliation {
			Expect(r.Published.Kind).NotTo(Equal(pivotreport.KindNull))
			Expect(r.Computed.Kind).To(Equal(pivotreport.KindNull))
			Expect(r.Agrees).To(BeFalse())
		}
		Expect(res.Summary.Diagnostics).NotTo(BeEmpty())
		Expect(res.Summary.Diagnostics[0].Code).To(Equal("reconciliation_mismatch"))
	})
})

func assertTotal(s *pivotreport.Summary, column int, kind pivotreport.ValueKind, value string, nonNull, null int) {
	t := totalFor(s, column)
	ExpectWithOffset(1, t.Total.Kind).To(Equal(kind), "column %d", column)
	switch kind {
	case pivotreport.KindInt:
		ExpectWithOffset(1, t.Total.Int).To(Equal(value), "column %d", column)
	case pivotreport.KindCents:
		ExpectWithOffset(1, t.Total.Cents).To(Equal(value), "column %d", column)
	}
	ExpectWithOffset(1, t.NonNull).To(Equal(nonNull), "column %d", column)
	ExpectWithOffset(1, t.Null).To(Equal(null), "column %d", column)
}

func phantomBinary() string {
	return "/usr/local/bin/phantomjs"
}

func fixtureLogin(url, user, pass string) pivotreport.Login {
	return pivotreport.Login{
		URL:             url,
		UsernameField:   "#username",
		PasswordField:   "#password",
		SubmitSelector:  "#signin",
		Username:        user,
		Password:        pass,
		SuccessSelector: "#pivot",
		Timeout:         30,
	}
}

func fixtureDeclaration() *pivotreport.Declaration {
	return &pivotreport.Declaration{
		Version:       pivotreport.SchemaVersion,
		Name:          "区域销售透视报表",
		TableSelector: "#pivot",
		Axes: []pivotreport.Axis{
			{Name: "区域", Role: pivotreport.AxisDimension, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "城市", Role: pivotreport.AxisDimension, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "产品", Role: pivotreport.AxisLabel, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "期间", Role: pivotreport.AxisPeriod, Orientation: pivotreport.OrientationColumn, Levels: 1, PeriodFormat: "yyyy年第n季度"},
		},
		Metrics: []pivotreport.Metric{
			{Name: "件数", Path: []string{"销量", "件数"}, Type: pivotreport.TypeInt, Unit: "件", Nulls: nulls()},
			{Name: "目标", Path: []string{"销量", "目标"}, Type: pivotreport.TypeInt, Unit: "件", Nulls: nulls()},
			{Name: "金额", Path: []string{"销量", "金额"}, Type: pivotreport.TypeCents, Unit: "元", Nulls: nulls()},
			{Name: "件数", Path: []string{"退货", "件数"}, Type: pivotreport.TypeInt, Unit: "件", Nulls: nulls()},
			{Name: "金额", Path: []string{"退货", "金额"}, Type: pivotreport.TypeCents, Unit: "元", Nulls: nulls()},
		},
		Layout: pivotreport.Layout{
			HeaderRows:   3,
			MarkerColumn: 3,
			Markers: pivotreport.Markers{
				Detail:     []string{"明细"},
				Subtotal:   []string{"小计"},
				GrandTotal: []string{"总计"},
			},
		},
	}
}

func emptyDeclaration() *pivotreport.Declaration {
	return &pivotreport.Declaration{
		Version:       pivotreport.SchemaVersion,
		Name:          "空报表",
		TableSelector: "#empty",
		Axes: []pivotreport.Axis{
			{Name: "区域", Role: pivotreport.AxisDimension, Orientation: pivotreport.OrientationRow, Levels: 1},
			{Name: "期间", Role: pivotreport.AxisPeriod, Orientation: pivotreport.OrientationColumn, Levels: 1, PeriodFormat: "yyyy年第n季度"},
		},
		Metrics: []pivotreport.Metric{
			{Name: "销量件数", Path: []string{"销量件数"}, Type: pivotreport.TypeInt, Nulls: nulls()},
			{Name: "销量金额", Path: []string{"销量金额"}, Type: pivotreport.TypeCents, Nulls: nulls()},
		},
		Layout: pivotreport.Layout{
			HeaderRows:   2,
			MarkerColumn: 1,
			Markers: pivotreport.Markers{
				Detail:     []string{"明细"},
				GrandTotal: []string{"总计"},
			},
		},
	}
}

func nulls() []string { return []string{"NULL", "-", "—", "N/A"} }
