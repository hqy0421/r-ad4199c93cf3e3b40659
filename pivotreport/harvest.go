package pivotreport

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Page is the subset of *agouti.Page that harvesting needs. It is an interface
// only so that callers can wrap the real page (for tracing, retries or another
// WebDriver); the production path is the *agouti.Page from this module.
type Page interface {
	Navigate(url string) error
	RunScript(body string, arguments map[string]interface{}, result interface{}) error
	Title() (string, error)
	URL() (string, error)
}

// Login describes how to reach an authenticated report page. Every field is
// explicit: nothing is inferred from the page, and a failed sign-in is an error
// rather than an empty harvest.
type Login struct {
	// URL of the sign-in page.
	URL string `json:"url"`
	// UsernameField and PasswordField are CSS selectors for the credential
	// inputs; SubmitSelector selects the submit control.
	UsernameField  string `json:"username_field"`
	PasswordField  string `json:"password_field"`
	SubmitSelector string `json:"submit_selector"`
	// UsernameEnv and PasswordEnv name environment variables holding the
	// credentials. Username and Password may be given literally instead. The
	// literal credentials are never written to any output file.
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	UsernameEnv string `json:"username_env,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
	// SuccessSelector must be present after submitting. It is the only signal
	// that the session is authenticated.
	SuccessSelector string `json:"success_selector"`
	// Timeout bounds each sign-in wait. Zero means 30s.
	Timeout time.Duration `json:"timeout_ns,omitempty"`
}

func (l Login) credentials() (string, string, error) {
	user, pass := l.Username, l.Password
	if user == "" && l.UsernameEnv != "" {
		user = os.Getenv(l.UsernameEnv)
	}
	if pass == "" && l.PasswordEnv != "" {
		pass = os.Getenv(l.PasswordEnv)
	}
	if user == "" || pass == "" {
		return "", "", failure(KindLogin, "login", "both username and password are required (set the login fields or their environment variables)")
	}
	return user, pass, nil
}

// SignIn authenticates the page. It refuses to continue unless the declared
// success selector is present afterwards.
func SignIn(page Page, login Login) error {
	if strings.TrimSpace(login.URL) == "" {
		return failure(KindLogin, "login", "login URL is required")
	}
	if login.UsernameField == "" || login.PasswordField == "" || login.SubmitSelector == "" {
		return failure(KindLogin, "login", "username field, password field and submit selector are all required")
	}
	if login.SuccessSelector == "" {
		return failure(KindLogin, "login", "a success selector is required; an unverified sign-in is not accepted")
	}
	user, pass, err := login.credentials()
	if err != nil {
		return err
	}
	if err := page.Navigate(login.URL); err != nil {
		return failure(KindLogin, "login", "navigating to the sign-in page failed: %s", err)
	}
	const script = `
		var u = document.querySelector(uField);
		var p = document.querySelector(pField);
		var s = document.querySelector(submit);
		if (!u || !p || !s) {
			return {ok: false, reason: "missing credential field or submit control"};
		}
		u.value = user;
		p.value = pass;
		s.click();
		return {ok: true};
	`
	var result struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	err = page.RunScript(script, map[string]interface{}{
		"user":   user,
		"pass":   pass,
		"uField": login.UsernameField,
		"pField": login.PasswordField,
		"submit": login.SubmitSelector,
	}, &result)
	if err != nil {
		return failure(KindLogin, "login", "sign-in script failed: %s", err)
	}
	if !result.OK {
		return failure(KindLogin, "login", "sign-in form is unusable: %s", result.Reason)
	}
	return waitForSelector(page, login.SuccessSelector, timeoutOf(login.Timeout))
}

func timeoutOf(d time.Duration) time.Duration {
	if d <= 0 {
		return 30 * time.Second
	}
	return d
}

func waitForSelector(page Page, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	// A hidden element is not evidence of authentication: report shells are
	// often present in the DOM behind the login overlay. Require it to be
	// rendered and visible.
	const script = `
		var el;
		try {
			el = document.querySelector(selector);
		} catch (e) {
			return false;
		}
		if (!el) { return false; }
		var style = window.getComputedStyle(el);
		if (style.display === "none" || style.visibility === "hidden" || style.opacity === "0") { return false; }
		var rect = el.getBoundingClientRect();
		return rect.width > 0 && rect.height > 0;
	`
	for {
		var present bool
		if err := page.RunScript(script, map[string]interface{}{"selector": selector}, &present); err != nil {
			return failure(KindLogin, "login", "checking for %q failed: %s", selector, err)
		}
		if present {
			return nil
		}
		if time.Now().After(deadline) {
			return failure(KindLogin, "login", "sign-in was not confirmed: %q never became visible (credentials or success selector are wrong)", selector)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// RawCell is one <td>/<th> exactly as it appears in the DOM, including the
// declared span attributes. It is never resized or duplicated during harvest.
type RawCell struct {
	Text    string `json:"text"`
	Tag     string `json:"tag"`
	RowSpan int    `json:"rowSpan"`
	ColSpan int    `json:"colSpan"`
	Hidden  bool   `json:"hidden"`
	Row     int    `json:"row"`
	Column  int    `json:"column"`
	// Section is the HTML section the row belonged to (thead/tbody/tfoot).
	Section string `json:"section"`
}

// RawRow is a table row as harvested.
type RawRow struct {
	Cells [](*RawCell) `json:"cells"`
	// Class is the row's class attribute, kept as evidence.
	Class string `json:"class,omitempty"`
}

// RawTable is the untouched harvest of one table.
type RawTable struct {
	Caption string    `json:"caption"`
	Rows    []*RawRow `json:"rows"`
	// Headers lists the effective <th> texts of the table, for evidence.
	Headers []string `json:"headers"`
	// URL and Title record where the harvest came from.
	URL   string `json:"url"`
	Title string `json:"title"`
	// Bounds are the table's pixel coordinates at harvest time.
	Bounds Rect `json:"bounds"`
}

// Rect is a rectangle in CSS pixels.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// HarvestTable extracts every row cell of the first table matching selector
// from the already-authenticated page. It captures row/col spans verbatim.
func HarvestTable(page Page, selector string) (*RawTable, error) {
	if strings.TrimSpace(selector) == "" {
		return nil, failure(KindHarvest, "harvest", "table selector is required")
	}
	const script = `
		var table;
		try {
			table = document.querySelector(sel);
		} catch (e) {
			return {found: false, reason: "invalid selector: " + e.message};
		}
		if (!table) { return {found: false, reason: "no matching table"}; }
		var rows = table.rows, out = [], heads = [];
		for (var i = 0; i < rows.length; i++) {
			var row = rows[i], cells = [];
			for (var j = 0; j < row.cells.length; j++) {
				var c = row.cells[j];
				var style = window.getComputedStyle(c);
				var hidden = c.hidden || style.display === "none" || style.visibility === "hidden";
				cells.push({
					text: c.textContent,
					tag: c.tagName.toLowerCase(),
					rowSpan: c.rowSpan || 1,
					colSpan: c.colSpan || 1,
					hidden: !!hidden,
					row: i,
					column: j,
					section: (row.parentNode && row.parentNode.tagName) ? row.parentNode.tagName.toLowerCase() : ""
				});
				if (c.tagName.toLowerCase() === "th") { heads.push(String(c.textContent)); }
			}
			out.push({cells: cells, class: row.className || ""});
		}
		var rect = table.getBoundingClientRect();
		var tstyle = window.getComputedStyle(table);
		var visible = tstyle.display !== "none" && tstyle.visibility !== "hidden" && tstyle.opacity !== "0" && rect.width > 0 && rect.height > 0;
		var cap = table.caption ? String(table.caption.textContent) : "";
		return {
			found: true,
			visible: visible,
			rows: out,
			headers: heads,
			caption: cap,
			url: String(location.href),
			title: String(document.title),
			bounds: {x: Math.round(rect.left), y: Math.round(rect.top), width: Math.round(rect.width), height: Math.round(rect.height)}
		};
	`
	var payload struct {
		Found   bool      `json:"found"`
		Visible bool      `json:"visible"`
		Reason  string    `json:"reason"`
		Rows    []*RawRow `json:"rows"`
		Headers []string  `json:"headers"`
		Caption string    `json:"caption"`
		URL     string    `json:"url"`
		Title   string    `json:"title"`
		Bounds  Rect      `json:"bounds"`
	}
	if err := page.RunScript(script, map[string]interface{}{"sel": selector}, &payload); err != nil {
		return nil, failure(KindHarvest, "harvest", "cannot read table %q from the page: %s", selector, err)
	}
	if !payload.Found {
		url, _ := page.URL()
		reason := payload.Reason
		if reason == "" {
			reason = "no matching table"
		}
		return nil, failure(KindHarvest, "harvest", "table %q cannot be harvested from %s: %s (the session may not be authenticated, or the report has not loaded)", selector, url, reason)
	}
	if !payload.Visible {
		return nil, failure(KindHarvest, "harvest", "table %q is present but not visible on %s; harvesting a hidden table would publish a report the user cannot see (is the session authenticated?)", selector, payload.URL)
	}
	if len(payload.Rows) == 0 {
		return nil, failure(KindStructure, "harvest", "table %q has no rows at all; a report must have a declared header even when it has no detail rows", selector)
	}
	return &RawTable{
		Caption: payload.Caption,
		Rows:    payload.Rows,
		Headers: payload.Headers,
		URL:     payload.URL,
		Title:   payload.Title,
		Bounds:  payload.Bounds,
	}, nil
}

// rawEnvelope versions a saved harvest so documents from another version are
// rejected, never mixed.
type rawEnvelope struct {
	Version int       `json:"version"`
	Table   *RawTable `json:"table"`
}

// SaveRaw writes a harvested table as evidence that can be reloaded.
func SaveRaw(path string, table *RawTable) error {
	data, err := json.MarshalIndent(rawEnvelope{Version: SchemaVersion, Table: table}, "", "  ")
	if err != nil {
		return failure(KindWrite, "save raw", "cannot encode harvest: %s", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return failure(KindWrite, "save raw", "cannot create directory %s: %s", dir, err)
		}
	}
	tmp := path + ".tmp"
	if err := ioutil.WriteFile(tmp, data, 0644); err != nil {
		return failure(KindWrite, "save raw", "cannot write %s: %s", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return failure(KindWrite, "save raw", "cannot replace %s: %s", path, err)
	}
	return nil
}

// LoadRaw reads a harvest written by SaveRaw, rejecting other versions.
func LoadRaw(path string) (*RawTable, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, failure(KindStructure, "load raw", "cannot read %s: %s", path, err)
	}
	var env rawEnvelope
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return nil, failure(KindStructure, "load raw", "invalid harvest %s: %s", path, err)
	}
	if env.Version != SchemaVersion {
		return nil, failure(KindVersion, "load raw", "harvest version %d is not supported (this build supports version %d)", env.Version, SchemaVersion)
	}
	if env.Table == nil {
		return nil, failure(KindStructure, "load raw", "harvest %s contains no table", path)
	}
	return env.Table, nil
}
