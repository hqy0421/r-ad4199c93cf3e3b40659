# pivotreport

Harvests a **real, already-authenticated** pivot report through agouti and turns
its merged headers and subtotal rows into typed facts, rollups and provenance —
without ever treating a merged header or a subtotal row as a detail row.

The existing agouti API is untouched; this is an additive package.

## Why a declaration is required

A pivot report's HTML does not say which cells are detail, which are subtotals, or
which leaf metric a merged header belongs to. Guessing those from text produces
silently wrong imports. Instead the caller declares the report's shape, and any
structure that does not match the declaration is an **explicit error**, never a
best-effort guess.

## Pipeline

```
page (logged in)  ──HarvestTable──▶  RawTable          (cells kept verbatim, spans intact)
RawTable          ──Extract─────▶    Extraction        (spans resolved, headers aligned, rows typed)
Extraction        ──buildSummary─▶   Summary           (detail-only rollups, reconciliation)
BuildResult       ──Write───────▶    detail.json / summary.json / harvest.json
```

1. **Harvest** (`harvest.go`) — `SignIn` fills the declared fields, submits, and
   refuses to continue until the success selector is actually **visible** (a
   report shell hidden behind a login overlay is not authentication).
   `HarvestTable` reads every row cell with its `rowspan`/`colspan`, hidden flag,
   section and row class, and records the URL, title, caption and bounds.
2. **Grid** (`grid.go`) — expands spans into a fixed grid. Overlapping cells, a
   span past the table end, and ragged rows are structure errors.
3. **Extract** (`extract.go`) — the header row count is declared, so a cell that
   *originates* above a column is inherited (`rowspan`) rather than re-read. Each
   value column is aligned by its **complete header path** to a declared metric;
   the column axis (period) is parsed from its declared format. Same-named leaves
   under different parents are separate columns, and a duplicate declared path is
   rejected. Only rows whose marker column holds a declared `detail` marker become
   facts; `subtotal` / `grand_total` / `ignored` rows are retained verbatim as
   source, never counted.
4. **Numeric** (`numeric.go`) — values are exact: integers, cents scaled by 100,
   and decimals with a declared scale are stored as strings backed by
   `math/big`. No float touches a value. NULL tokens and empty cells become typed
   NULL, kept distinct from `0`. Parenthesised negatives are accounting-negatives.
5. **Rollup** (`rollup.go`) — sums only detail facts, per column (so the same
   metric under two periods is two columns). Counting and grouping are keyed by
   column context. Published subtotals and grand totals are compared against the
   rollup of the detail rows they publish; they are never added. `aggregate:"none"`
   metrics are counted but not summed.
6. **Output** (`output.go`) — versioned envelopes, atomic write, then the detail
   file is **re-opened and verified** value-for-value. `SumFacts` recomputes the
   summary from the re-opened detail alone, so a consumer need not trust the
   summary.

## Error kinds

Every failure is a classified `*Error` and `KindOf(err)` returns one of:

`version`, `structure`, `semantic`, `type`, `write`, `harvest`, `login`.

Mixing documents of another schema version, an ambiguous or undeclared structure,
a value that does not fit its declared type, and a failed write are all errors —
none is downgraded to a warning.

## Declaration

```json
{
  "version": 1,
  "name": "区域销售透视报表",
  "table_selector": "#pivot",
  "axes": [
    {"name": "区域", "role": "dimension", "orientation": "row", "levels": 1},
    {"name": "城市", "role": "dimension", "orientation": "row", "levels": 1},
    {"name": "产品", "role": "label",     "orientation": "row", "levels": 1},
    {"name": "期间", "role": "period",    "orientation": "column", "levels": 1,
     "period_format": "yyyy年第n季度"}
  ],
  "metrics": [
    {"name": "件数", "path": ["销量", "件数"], "type": "int",   "unit": "件", "null_tokens": ["NULL"]},
    {"name": "金额", "path": ["销量", "金额"], "type": "cents", "unit": "元", "null_tokens": ["NULL"]}
  ],
  "layout": {
    "header_rows": 3,
    "marker_column": 3,
    "markers": {"detail": ["明细"], "subtotal": ["小计"], "grand_total": ["总计"]}
  }
}
```

`column-axis depth + len(metric.path)` must equal `header_rows`. Row axes come
before column axes, and there is exactly one period axis.

## Command

```sh
# from a real, logged-in page
pivotreport -decl report.json -login login.json -url https://example/report -out-dir out

# or from a saved harvest (repeatable, no browser)
pivotreport -decl report.json -raw out/harvest.json -out-dir out
```

`-reconcile warn|error|off` controls how a published total that disagrees with its
detail rows is handled. Exit code `2` means a classified extraction failure.

Credentials may be given literally or via `username_env` / `password_env`; they
are never written to any output file.

## Outputs

- `detail.json` — one typed fact per detail row per metric column, each with its
  exact source cell and complete header cells, plus the retained source rows.
- `summary.json` — detail-only totals per column, missing counts (NULL vs zero),
  rollups by dimension and by canonical period, the retained published totals,
  and a reconciliation entry per published value.
- `harvest.json` — the verbatim harvest, as evidence.

Every file re-opens through `ReadDetail`, `ReadSummary` and `LoadRaw`.
