// Command pivotreport harvests an authenticated pivot report from a real
// browser session (agouti + PhantomJS), aligns its merged headers to a declared
// schema, and writes typed detail and rollup files.
//
// Usage:
//
//	pivotreport -decl report.json -out-dir out [-url REPORT_URL] [-login login.json] [-raw harvest.json]
//	pivotreport -decl report.json -raw harvest.json -out-dir out
//
// Exit codes: 0 success, 1 invalid invocation, 2 extraction failure (classified
// error printed to stderr).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/sclevine/agouti"
	"github.com/sclevine/agouti/pivotreport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if kind := pivotreport.KindOf(err); kind != "" {
			fmt.Fprintln(os.Stderr, "kind:", kind)
			os.Exit(2)
		}
		os.Exit(1)
	}
}

type invocation struct {
	declPath   string
	rawPath    string
	rawWrite   string
	outDir     string
	reportURL  string
	loginPath  string
	reconcile  string
	driverPath string
}

func run() error {
	var inv invocation
	flag.StringVar(&inv.declPath, "decl", "", "declaration JSON file (required)")
	flag.StringVar(&inv.rawPath, "raw", "", "saved harvest to reuse instead of a live page")
	flag.StringVar(&inv.rawWrite, "save-raw", "", "write the live harvest here for evidence")
	flag.StringVar(&inv.outDir, "out-dir", "", "directory for detail.json and summary.json (required)")
	flag.StringVar(&inv.reportURL, "url", "", "report URL to open after signing in")
	flag.StringVar(&inv.loginPath, "login", "", "login JSON file (see pivotreport.Login)")
	flag.StringVar(&inv.reconcile, "reconcile", "warn", "warn|error|off: how published totals are checked")
	flag.StringVar(&inv.driverPath, "phantomjs", "phantomjs", "phantomjs executable")
	flag.Parse()

	if inv.declPath == "" || inv.outDir == "" {
		flag.Usage()
		return fmt.Errorf("-decl and -out-dir are required")
	}
	decl, err := pivotreport.LoadDeclaration(inv.declPath)
	if err != nil {
		return err
	}

	cfg := pivotreport.Config{Declaration: decl, Reconcile: pivotreport.ReconcileMode(inv.reconcile)}

	switch {
	case inv.rawPath != "":
		table, err := pivotreport.LoadRaw(inv.rawPath)
		if err != nil {
			return err
		}
		cfg.Table = table
	case inv.loginPath != "" || inv.reportURL != "":
		var login *pivotreport.Login
		if inv.loginPath != "" {
			data, err := ioutil.ReadFile(inv.loginPath)
			if err != nil {
				return fmt.Errorf("cannot read login file: %s", err)
			}
			var l pivotreport.Login
			if err := json.Unmarshal(data, &l); err != nil {
				return fmt.Errorf("invalid login file: %s", err)
			}
			login = &l
		}
		driver := agouti.PhantomJS(agouti.Timeout(60), agouti.Browser("phantomjs"))
		if inv.driverPath != "phantomjs" {
			driver = agouti.NewWebDriver("http://{{.Address}}", []string{inv.driverPath, "--webdriver={{.Address}}"}, agouti.Timeout(60))
		}
		if err := driver.Start(); err != nil {
			return fmt.Errorf("cannot start phantomjs: %s", err)
		}
		defer driver.Stop()
		page, err := driver.NewPage()
		if err != nil {
			return fmt.Errorf("cannot open browser page: %s", err)
		}
		defer page.Destroy()

		cfg.Page = page
		cfg.Login = login
		cfg.ReportURL = inv.reportURL
	default:
		return fmt.Errorf("either -raw, -url or -login is required to obtain the report")
	}

	result, err := pivotreport.Build(cfg)
	if err != nil {
		return err
	}

	rawPath := inv.rawWrite
	if rawPath == "" {
		rawPath = filepath.Join(inv.outDir, "harvest.json")
	}
	paths := pivotreport.OutputPaths{
		Detail:  filepath.Join(inv.outDir, "detail.json"),
		Summary: filepath.Join(inv.outDir, "summary.json"),
		Raw:     rawPath,
	}
	if err := result.Write(paths); err != nil {
		return err
	}

	printReport(result.Summary, paths)
	return nil
}

func printReport(s *pivotreport.Summary, paths pivotreport.OutputPaths) {
	var b strings.Builder
	fmt.Fprintf(&b, "report: %s (%s)\n", s.Report, s.Source.URL)
	fmt.Fprintf(&b, "detail rows: %d  facts: %d  columns: %d  retained rows: %d\n",
		s.DetailRows, s.Facts, s.Columns, s.SourceRows)
	if s.Empty {
		fmt.Fprintf(&b, "the report has no detail rows; totals below are typed and remain NULL/zero-distinct\n")
	}
	fmt.Fprintf(&b, "totals (detail facts only):\n")
	for _, t := range s.Total {
		fmt.Fprintf(&b, "  %-28s %-22s non_null=%d null=%d\n", strings.Join(t.Path, " / "), renderValue(t.Total, t.Type), t.NonNull, t.Null)
	}
	for _, g := range s.ByPeriod {
		fmt.Fprintf(&b, "period %s: %d facts\n", g.Value, g.Facts)
	}
	for _, d := range s.Diagnostics {
		fmt.Fprintf(&b, "warning [%s] %s\n", d.Code, d.Message)
	}
	fmt.Fprintf(&b, "wrote %s and %s\n", paths.Detail, paths.Summary)
	fmt.Print(b.String())
}

func renderValue(v pivotreport.Value, mt pivotreport.MetricType) string {
	switch v.Kind {
	case pivotreport.KindNull:
		return "NULL"
	case pivotreport.KindInt:
		return v.Int
	case pivotreport.KindCents:
		neg := strings.HasPrefix(v.Cents, "-")
		digits := strings.TrimPrefix(v.Cents, "-")
		for len(digits) < 3 {
			digits = "0" + digits
		}
		out := digits[:len(digits)-2] + "." + digits[len(digits)-2:]
		if neg && strings.Trim(out, "0.") != "" {
			out = "-" + out
		}
		return out
	case pivotreport.KindDecimal:
		return v.Decimal
	case pivotreport.KindText:
		return v.Text
	}
	return "<untyped>"
}
