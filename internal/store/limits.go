package store

// MaxSessionListLimit caps the sessions index. Higher than the request list's
// cap because one row is a whole conversation, so a page of them is still a
// readable overview. Exported because the caller has to know what it asked for
// to tell a full page from a truncated one.
const MaxSessionListLimit = 1000

// maxRequestListLimit caps a single list query. The default (when the caller
// asks for no limit) is deliberately the cap: a dashboard shows recent rows,
// and truncating to the newest N is a more useful failure than a slow query.
const maxRequestListLimit = 500
