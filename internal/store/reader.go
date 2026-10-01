// The read side is split by question, one file per concern, so a reader can
// find "how does the sessions list work" without grepping a monolith:
//
//	reader.go    the Reader handle itself, Window, the request-list row types
//	             (RequestRow/RequestDetail/RequestFilter), and ListRequests/GetRequest
//	stats.go     window aggregates — Overall, ByProvider, ByEpoch, Tools
//	sessions.go  one conversation's trajectory, index, and per-session totals
//	blocks.go    captured content blocks, guardrail diffs, per-request content
//	repeated.go  repeated-content discovery (the Discovery page's subject)
//	limits.go    the list-size caps shared across those queries
//
// The queries are plain Go rather than a generated layer or a separate .sql
// file. An earlier version generated them with sqlc; that was retired after
// the read side turned out to need json_each(), a table-valued function sqlc's
// sqlite parser cannot resolve (see README's "Event store").
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reader answers questions about recorded requests. It is a thin layer over
// the same sqlite database the writer feeds, opening its own handle so a read
// never contends with the writer's single drain goroutine.
type Reader struct {
	db *sql.DB
}

// OpenReader opens the event store at path for reading. It does not create or
// migrate the schema — the writer owns that; a read against a database that
// was never written to simply returns empty results.
func OpenReader(path string) (*Reader, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open event store for reading %q: %w", path, err)
	}
	return &Reader{db: db}, nil
}

// Close releases the reader's database handle.
func (r *Reader) Close() error { return r.db.Close() }

// Window is a time range for a query. Since is the inclusive lower bound;
// Until is the *exclusive* upper bound, and a zero Until means "open ended" —
// every row at or after Since, which is what every caller predating the
// Overview page's compare mode wants and gets without saying so.
//
// The half-open shape [Since, Until) is deliberate: it makes two adjacent
// windows tile without double-counting the row that sits exactly on the
// boundary, which is precisely what compare mode does (an anchor's before-side
// ends where its after-side begins). A closed upper bound would count that row
// twice and quietly inflate both sides of every delta.
//
// Both bounds bind as time.Time. The ts column is TEXT holding Go's
// time.Time.String() layout, and the sqlite driver binds a time.Time to that
// same layout, so string comparison and chronological order agree — verified
// against the live store before Until was added (a bound at a known instant
// partitions the table exactly: the >= and < counts sum to the total).
type Window struct {
	Since time.Time
	Until time.Time
}

// WindowFrom turns an operator-friendly duration ("24h", "7d" handled by the
// caller) into a window ending now. The upper bound is left zero rather than
// set to time.Now(): "now" moves between the call and the query, and an
// open-ended window is both cheaper and exactly what a trailing window means.
func WindowFrom(d time.Duration) Window {
	return Window{Since: time.Now().UTC().Add(-d)}
}

// Bounded reports whether the window has an upper bound at all, so a query
// builder can skip the clause instead of binding a zero time that would match
// nothing.
func (w Window) Bounded() bool { return !w.Until.IsZero() }

// tsClause renders the window's own SQL predicate against a ts column, plus
// the args to bind for it. The column is named by the caller because some
// queries join and must qualify it (`r.ts`) while most do not.
//
// It exists so the half-open convention above is written once. Every query
// that grew an upper bound got it by calling this rather than by hand-editing
// its WHERE, which is what keeps "exclusive upper bound" a property of the
// type instead of a habit each query might break.
func (w Window) tsClause(col string) (string, []interface{}) {
	if !w.Bounded() {
		return col + " >= ?", []interface{}{w.Since}
	}
	return col + " >= ? AND " + col + " < ?", []interface{}{w.Since, w.Until}
}

// RequestFilter selects rows for the request list. Every field is optional;
// the zero value means "no restriction". Empty strings and 0 therefore cannot
// be asked for as *values* (a status of 0, a provider named ""), which is
// correct for this store: the recorded provider is never empty and a status
// code of 0 never occurs.
type RequestFilter struct {
	Since      time.Time // inclusive lower bound on ts; zero means all time
	Provider   string
	SessionKey string // "none" is not special-cased — see ListRequests
	Alias      string
	StatusCode int
	ErrorsOnly bool // status_code >= 400
	Limit      int  // clamped to [1, maxRequestListLimit]; 0 means the default

	// Kind filters to an exact requests.kind value ("client", "classifier",
	// ...). Empty means no filter — every other field's zero-value
	// convention, unlike the presentation-layer default of "client only"
	// applied by the HTTP/UI handlers before a filter reaches here (see
	// stats.RequestsHandler and ui.RequestsHandler): this type has no
	// opinion on what "no kind specified" should mean, it just filters or
	// doesn't.
	Kind string

	// RequestKind filters to an exact requests.request_kind value ("title",
	// later "subagent"). Empty means no filter, like every other field here.
	// It is a separate filter from Kind above because the two answer
	// different questions: Kind is who sent the request, RequestKind is what
	// it is.
	RequestKind string

	// SessionKeyless selects the requests that have *no* session key —
	// `session_key IS NULL OR session_key = ''`. It exists because the zero
	// value of SessionKey means "any" (above), so the absent case is otherwise
	// unexpressible. It is reachable in practice: the affinity derivation
	// declines to pin a conversation whose first message is too short, and
	// those requests must still be visible in a list rather than silently
	// grouped under a session they do not belong to.
	SessionKeyless bool

	// BeforeTs/BeforeID are the keyset cursor: return only rows strictly older
	// than this (arrival_ts, id) pair under the list's own
	// `arrival_ts DESC, id DESC` ordering. Both must be set together; either
	// alone is ignored.
	//
	// BeforeTs is compared as the *stored text*, not as a bound built from a Go
	// time, because the store's arrival_ts column is TEXT in a layout SQLite's
	// date functions cannot parse and a fraction-free bound sorts below every
	// row in its own second. The caller therefore echoes back the exact value
	// the list handed it (RequestRow.ArrivalTsRaw) rather than reformatting a
	// timestamp.
	BeforeTs string
	BeforeID int64
}

// RequestRow is one request as it appears in a list: enough to see what
// happened and where it went, without the prompt/response bodies. It is what
// a dashboard's main table renders.
type RequestRow struct {
	ID      int64  `json:"id"`
	TraceID string `json:"trace_id"`
	Ts      string `json:"ts"`

	// ArrivalTs is when the request reached Arbiter (Execute's own entry),
	// distinct from Ts (when it finished and this row was written). Empty
	// for rows that predate the column, or for kinds that never set it.
	// See schema.sql's comment on requests.arrival_ts and issue #8.
	ArrivalTs string `json:"arrival_ts,omitempty"`

	// ArrivalTsRaw is arrival_ts exactly as stored, the keyset cursor's
	// carry-forward value now that the list orders by arrival — see
	// RequestFilter.BeforeTs. Unmarshalled off the wire: a handle for the
	// next page, not a second rendering of the same instant.
	ArrivalTsRaw string `json:"-"`

	// TsRaw is the timestamp exactly as stored, kept for callers still
	// reasoning about finish time (e.g. #14's cost/epoch aggregates).
	// Unmarshalled off the wire: it is a handle for the next page, not a
	// second rendering of the same instant.
	TsRaw      string `json:"-"`
	SessionKey string `json:"session_key,omitempty"`
	Format     string `json:"format"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	// ActualModel is the upstream-reported model, present only when it
	// differs from Model (a meta-router alias like OpenRouter's
	// "openrouter/auto" picked something concrete) — see store.Event.ActualModel.
	ActualModel      string `json:"actual_model,omitempty"`
	AliasUsed        string `json:"alias_used,omitempty"`
	RoutingRationale string `json:"routing_rationale"`
	Domain           string `json:"domain,omitempty"`
	Difficulty       string `json:"difficulty,omitempty"`
	CostClass        string `json:"cost_class,omitempty"`
	// RequiredCapabilities is nil when classification did not produce a result,
	// and an empty non-nil slice when classification found no capabilities.
	RequiredCapabilities []string `json:"required_capabilities"`
	InputTokens          int64    `json:"input_tokens"`
	OutputTokens         int64    `json:"output_tokens"`
	CostUSD              float64  `json:"cost_usd"`
	LatencyMs            int64    `json:"latency_ms"`
	StatusCode           int64    `json:"status_code"`
	Error                string   `json:"error,omitempty"`
	Stream               bool     `json:"stream"`
	ConfigEpoch          string   `json:"config_epoch,omitempty"`
	// Kind is "client" (real traffic, the default) or one of Arbiter's own
	// internal request kinds ("classifier", and later "subagent") — see
	// store.Event.Kind.
	Kind string `json:"kind"`

	// RequestKind is what the request IS — "title" today, "subagent" later —
	// as opposed to Kind above, which is who sent it. See
	// store.Event.RequestKind.
	RequestKind string `json:"request_kind,omitempty"`
}

// HasClassification reports whether a classifier result was recorded for this
// request at all — as opposed to a result that fills no axis, which is a real
// and common outcome ("classified, nothing matched"). It is the ONE definition
// of that predicate: the list projection, the detail projection and the
// transcript inspector all call this rather than each testing their own subset
// of columns, which is how the three of them drifted apart.
//
// RequiredCapabilities is the load-bearing field, not the axes: the merge
// initializes it to an empty non-nil slice, so it is non-nil whenever
// classification ran and stays nil when it never did (an unclassified row is
// stored as SQL NULL). The axis checks are a fallback for rows written before
// required_capabilities_json existed, where a filled axis is the only surviving
// evidence that anything classified them.
//
// Confidence is deliberately NOT consulted. confidence is a non-nullable REAL
// column written as a bare float64, so an unset confidence reads back as 0, not
// NULL — `conf.Valid` is always false and a check on it would silently never
// fire. RequestKind is likewise excluded: it is an identification stamp, not a
// classification output, and a title request is recorded by the stamp alone.
func (r RequestRow) HasClassification() bool {
	return r.RequiredCapabilities != nil ||
		r.Domain != "" || r.Difficulty != "" || r.CostClass != ""
}

// RequestDetail is the full record for one request. Unlike RequestRow it
// carries the low-confidence classification score, the cache token counts,
// the tool calls, and — only when explicitly asked for — the request and
// response payloads.
//
// Prompts and responses are *not stored* by the event store (see the store's
// schema): it records routing, usage and outcome, not content. The two text
// fields are therefore always empty and exist so the shape of "eventually,
// optionally" is visible rather than surprising. Filling them means capturing
// request/response bodies in the schema first, which is a storage and privacy
// decision, not a read-side one.
type RequestDetail struct {
	RequestRow
	Confidence       float64 `json:"confidence"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	ToolCalls        string  `json:"tool_calls,omitempty"` // raw JSON array
	ClientID         string  `json:"client_id,omitempty"`  // NULL until per-client keys land

	// Headers is the inbound request's headers, credential-shaped values
	// already redacted before storage (see store.Event.Headers). Nil when
	// nothing was captured.
	Headers map[string]string `json:"headers,omitempty"`

	RequestText  string `json:"request_text,omitempty"`
	ResponseText string `json:"response_text,omitempty"`

	// Content is the captured request/response blocks, present only when
	// capture was on and something was stored. Omitted otherwise so "capture
	// off" and "nothing captured" are not confused on the wire — the
	// request_text/response_text fields above remain always-empty
	// placeholders from before capture existed and are superseded by this.
	Content []ContentBlock `json:"content,omitempty"`
}

// requestRowColumns is the list projection, shared by ListRequests and
// GetRequest so the two cannot drift into returning differently-shaped rows.
const requestRowColumns = `
    id, trace_id, ts, CAST(ts AS TEXT), session_key, format, provider, model, actual_model, alias_used,
    routing_rationale, domain, difficulty, cost_class, input_tokens, output_tokens,
    cost_usd, latency_ms, status_code, error, stream, config_epoch, kind, request_kind, arrival_ts,
    CAST(arrival_ts AS TEXT), required_capabilities_json`

// ListRequests returns requests newest first, narrowed by f.
//
// A note on the session filter: an empty SessionKey means "any" — see
// RequestFilter.SessionKeyless for the absent case, which is expressed as its
// own boolean rather than as a magic string.
func (r *Reader) ListRequests(ctx context.Context, f RequestFilter) ([]RequestRow, error) {
	where := []string{"1 = 1"}
	args := []interface{}{}

	if !f.Since.IsZero() {
		where = append(where, "arrival_ts >= ?")
		args = append(args, f.Since)
	}
	if f.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, f.Provider)
	}
	if f.SessionKey != "" {
		where = append(where, "session_key = ?")
		args = append(args, f.SessionKey)
	}
	if f.SessionKeyless {
		where = append(where, "(session_key IS NULL OR session_key = '')")
	}
	if f.Alias != "" {
		where = append(where, "alias_used = ?")
		args = append(args, f.Alias)
	}
	if f.StatusCode != 0 {
		where = append(where, "status_code = ?")
		args = append(args, f.StatusCode)
	}
	if f.ErrorsOnly {
		where = append(where, "status_code >= 400")
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.RequestKind != "" {
		where = append(where, "request_kind = ?")
		args = append(args, f.RequestKind)
	}
	// Keyset continuation. The row-value comparison matches the ordering below
	// exactly, which is what makes paging stable: an id-only cursor would skip
	// or repeat rows whenever arrival_ts is not monotonic in id (a backfill,
	// an import, a clock step). Both halves of the cursor are required; a
	// half-set cursor is ignored rather than silently mis-paging.
	if f.BeforeTs != "" && f.BeforeID > 0 {
		where = append(where, "(arrival_ts, id) < (?, ?)")
		args = append(args, f.BeforeTs, f.BeforeID)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = maxRequestListLimit
	}
	if limit > maxRequestListLimit {
		limit = maxRequestListLimit
	}

	// id as the tiebreaker matters: arrival_ts has sub-second precision, and
	// rows written within the same tick would otherwise come back in an
	// arbitrary order, making paging and "what just happened" both
	// unreliable.
	q := "SELECT" + requestRowColumns + " FROM requests WHERE " +
		strings.Join(where, " AND ") + " ORDER BY arrival_ts DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []RequestRow{}
	for rows.Next() {
		s, err := scanRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RequestExtra is the page-batched subset of a request row that the transcript
// inspector needs beyond RequestRow: the confidence and captured headers that
// live only on the detail row. It exists so a transcript page can fetch them
// for every turn in one round trip instead of one GetRequest call per turn —
// see RequestExtras. Whether a request was classified is NOT carried here: that
// answer is fully determined by the row's own fields, so RequestRow.
// HasClassification computes it rather than a second copy of the predicate
// being shipped alongside every row.
type RequestExtra struct {
	Confidence float64
	Headers    map[string]string
}

// RequestExtras batches RequestExtra lookups for a whole page of ids in one
// round trip — the same "one query for everyone on the page" shape
// SessionChildren and ContentForRequests already use, and the other half of
// #59's per-turn N+1 (GetRequest was previously called once per turn just for
// these two columns). An id absent from requests, or with no headers, is
// simply missing/zero-valued in the result — the caller already treats a
// zero RequestExtra as "no verdict, no headers".
func (r *Reader) RequestExtras(ctx context.Context, ids []int64) (map[int64]RequestExtra, error) {
	out := make(map[int64]RequestExtra, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `SELECT id, confidence, headers_json FROM requests WHERE id IN (` +
		strings.Join(placeholders, ", ") + `)`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("request extras: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			id      int64
			conf    sql.NullFloat64
			headers sql.NullString
		)
		if err := rows.Scan(&id, &conf, &headers); err != nil {
			return nil, fmt.Errorf("scan request extra: %w", err)
		}
		e := RequestExtra{Confidence: conf.Float64}
		if headers.Valid {
			// Same degrade-on-malformed-JSON behavior as GetRequest: never
			// fails the whole page over one bad headers_json.
			_ = json.Unmarshal([]byte(headers.String), &e.Headers)
		}
		out[id] = e
	}
	return out, rows.Err()
}

// GetRequest returns one request by id. ok=false means no such row, which the
// caller reports as 404 — an unknown id is a normal outcome, not an error.
func (r *Reader) GetRequest(ctx context.Context, id int64) (RequestDetail, bool, error) {
	const q = `SELECT` + requestRowColumns + `,
    confidence, cache_read_tokens, cache_write_tokens, tool_calls_json, client_id, headers_json
FROM requests WHERE id = ?`

	var (
		d            RequestDetail
		tsRaw        interface{}
		session      sql.NullString
		actual       sql.NullString
		alias        sql.NullString
		domain       sql.NullString
		difficulty   sql.NullString
		costCl       sql.NullString
		capabilities sql.NullString
		errText      sql.NullString
		epoch        sql.NullString
		reqKind      sql.NullString
		arrivalTs    interface{}
		arrivalTsRaw sql.NullString
		conf         sql.NullFloat64
		tools        sql.NullString
		client       sql.NullString
		headers      sql.NullString
	)
	err := r.db.QueryRowContext(ctx, q, id).Scan(
		&d.ID, &d.TraceID, &tsRaw, &d.TsRaw, &session, &d.Format, &d.Provider, &d.Model, &actual, &alias,
		&d.RoutingRationale, &domain, &difficulty, &costCl, &d.InputTokens, &d.OutputTokens,
		&d.CostUSD, &d.LatencyMs, &d.StatusCode, &errText, &d.Stream, &epoch, &d.Kind, &reqKind,
		&arrivalTs, &arrivalTsRaw, &capabilities,
		&conf, &d.CacheReadTokens, &d.CacheWriteTokens, &tools, &client, &headers)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestDetail{}, false, nil
	}
	if err != nil {
		return RequestDetail{}, false, fmt.Errorf("get request %d: %w", id, err)
	}

	d.Ts = formatTime(tsRaw)
	d.ArrivalTs = formatTime(arrivalTs)
	d.ArrivalTsRaw = arrivalTsRaw.String
	d.SessionKey = session.String
	d.ActualModel = actual.String
	d.AliasUsed = alias.String
	d.Domain = domain.String
	d.Difficulty = difficulty.String
	d.CostClass = costCl.String
	if capabilities.Valid {
		if err := json.Unmarshal([]byte(capabilities.String), &d.RequiredCapabilities); err != nil {
			// A malformed legacy value degrades to unavailable signals rather
			// than failing the entire transcript detail request.
			d.RequiredCapabilities = nil
		}
	}
	d.Error = errText.String
	d.ConfigEpoch = epoch.String
	d.RequestKind = reqKind.String
	d.Confidence = conf.Float64
	d.ToolCalls = tools.String
	d.ClientID = client.String
	if headers.Valid {
		// A malformed headers_json (there shouldn't be one — it's only ever
		// written by headersJSON) degrades to "no headers shown" rather than
		// failing the whole detail lookup.
		_ = json.Unmarshal([]byte(headers.String), &d.Headers)
	}
	// RequestText/ResponseText stay empty: content is not captured. See the
	// type's doc comment.
	return d, true, nil
}

// scanRequestRow reads the shared list projection. The nullable columns come
// back as sql.Null* and are flattened to "" in the JSON — a NULL session key
// and an empty-string one are not distinguished on the wire, matching how the
// aggregate queries already behave.
func scanRequestRow(rows *sql.Rows) (RequestRow, error) {
	var (
		s            RequestRow
		tsRaw        interface{}
		session      sql.NullString
		actual       sql.NullString
		alias        sql.NullString
		domain       sql.NullString
		difficulty   sql.NullString
		costCl       sql.NullString
		errText      sql.NullString
		epoch        sql.NullString
		reqKind      sql.NullString
		arrivalTs    interface{}
		arrivalTsRaw sql.NullString
		capabilities sql.NullString
	)
	if err := rows.Scan(&s.ID, &s.TraceID, &tsRaw, &s.TsRaw, &session, &s.Format, &s.Provider,
		&s.Model, &actual, &alias, &s.RoutingRationale, &domain, &difficulty, &costCl,
		&s.InputTokens, &s.OutputTokens, &s.CostUSD, &s.LatencyMs, &s.StatusCode,
		&errText, &s.Stream, &epoch, &s.Kind, &reqKind, &arrivalTs, &arrivalTsRaw, &capabilities); err != nil {
		return RequestRow{}, fmt.Errorf("scan request row: %w", err)
	}
	s.Ts = formatTime(tsRaw)
	s.ArrivalTs = formatTime(arrivalTs)
	s.ArrivalTsRaw = arrivalTsRaw.String
	s.SessionKey = session.String
	s.ActualModel = actual.String
	s.AliasUsed = alias.String
	s.Domain = domain.String
	s.Difficulty = difficulty.String
	s.CostClass = costCl.String
	if capabilities.Valid {
		if err := json.Unmarshal([]byte(capabilities.String), &s.RequiredCapabilities); err != nil {
			s.RequiredCapabilities = nil
		}
	}
	s.Error = errText.String
	s.ConfigEpoch = epoch.String
	s.RequestKind = reqKind.String
	return s, nil
}

// formatTime renders a sqlite timestamp, which the driver may hand back as a
// time.Time, a string (TEXT column), or nil. RFC3339 in UTC is what the JSON
// surface wants either way.
func formatTime(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case string:
		// MIN()/MAX() over a TIMESTAMP column lose the driver's usual
		// time.Time conversion and come back as time.Time.String()'s own
		// layout — ParseStoredTime knows the layouts.
		if parsed, ok := ParseStoredTime(t); ok {
			return parsed.Format(time.RFC3339)
		}
		return t
	default:
		return fmt.Sprint(t)
	}
}
