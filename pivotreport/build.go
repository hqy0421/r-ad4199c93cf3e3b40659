package pivotreport

import (
	"strings"
)

// Config is one complete extraction run.
type Config struct {
	Declaration *Declaration
	// Reconcile defaults to warn.
	Reconcile ReconcileMode
	// Table is a harvest (live or previously saved). When nil, Page and Login
	// are used to harvest from the live site.
	Table *RawTable
	// Page is an authenticated agouti page. When Login is set it is signed in
	// first.
	Page  Page
	Login *Login
	// ReportURL is navigated to after signing in, before harvesting.
	ReportURL string
}

// BuildResult bundles the artefacts of a run.
type BuildResult struct {
	Detail  *Detail
	Summary *Summary
	Raw     *RawTable
}

// Build performs the whole pipeline: (optional) authenticated harvest, span
// resolution, header alignment, typing, rollup, and write verification.
func Build(cfg Config) (*BuildResult, error) {
	if cfg.Declaration == nil {
		return nil, failure(KindSemantic, "build", "a declaration is required")
	}
	if err := cfg.Declaration.Validate(); err != nil {
		return nil, err
	}

	table := cfg.Table
	if table == nil {
		if cfg.Page == nil {
			return nil, failure(KindStructure, "build", "either a harvested table or an authenticated page is required")
		}
		if cfg.Login != nil {
			if err := SignIn(cfg.Page, *cfg.Login); err != nil {
				return nil, err
			}
		}
		if strings.TrimSpace(cfg.ReportURL) != "" {
			if err := cfg.Page.Navigate(cfg.ReportURL); err != nil {
				return nil, failure(KindHarvest, "build", "navigating to the report failed: %s", err)
			}
		}
		harvested, err := HarvestTable(cfg.Page, cfg.Declaration.TableSelector)
		if err != nil {
			return nil, err
		}
		table = harvested
	}

	extraction, err := Extract(cfg.Declaration, table)
	if err != nil {
		return nil, err
	}

	detail := buildDetail(cfg.Declaration, extraction, table)
	summary, err := buildSummary(cfg.Declaration, extraction, table, cfg.Reconcile)
	if err != nil {
		return nil, err
	}
	return &BuildResult{Detail: detail, Summary: summary, Raw: table}, nil
}

// Write persists the run's artefacts and verifies the detail file re-opens.
func (r *BuildResult) Write(paths OutputPaths) error {
	if paths.Raw != "" {
		if err := SaveRaw(paths.Raw, r.Raw); err != nil {
			return err
		}
	}
	return WriteOutputs(paths, r.Detail, r.Summary)
}

func buildDetail(decl *Declaration, x *Extraction, table *RawTable) *Detail {
	d := &Detail{
		Version:            SchemaVersion,
		DeclarationVersion: decl.Version,
		Report:             decl.Name,
		Source: SourceRef{
			URL:     table.URL,
			Title:   table.Title,
			Caption: table.Caption,
			Table:   decl.TableSelector,
		},
		Axes:        decl.Axes,
		Metrics:     decl.Metrics,
		DetailRows:  len(uniqueRows(x.Facts)),
		Empty:       len(x.Facts) == 0,
		Facts:       x.Facts,
		SourceRows:  x.SourceRows,
		Diagnostics: append([]Diagnostic(nil), x.Diagnostics...),
	}
	if table.Bounds != (Rect{}) {
		b := table.Bounds
		d.Source.Bounds = &b
	}
	for _, plan := range x.Columns {
		headerTexts := make([]string, len(plan.Header))
		for i, h := range plan.Header {
			headerTexts[i] = normaliseText(h.Text)
		}
		d.Columns = append(d.Columns, ColumnInfo{
			Column:      plan.Column,
			Metric:      plan.Metric.Name,
			Path:        append([]string(nil), plan.Metric.Path...),
			Type:        plan.Metric.Type,
			Scale:       plan.Metric.Scale,
			Unit:        plan.Metric.Unit,
			HeaderPath:  headerTexts,
			HeaderCells: plan.Header,
			ColumnAxis:  plan.Levels,
			Period:      plan.Period,
		})
	}
	return d
}

func uniqueRows(facts []Fact) map[int]bool {
	out := map[int]bool{}
	for _, f := range facts {
		out[f.SourceRow] = true
	}
	return out
}
