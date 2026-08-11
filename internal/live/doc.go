// Package live is the dashboard's read side.
//
// The gateway is CQRS-shaped around usage: the request path is the single
// writer — it records a usage event once, after the upstream returns its
// authoritative token count — and everything that displays usage is a reader.
// This package is where those readers live.
//
//	proxy handler ──record()──▶ usage store        (write side, one writer)
//	                    └──────▶ Hub.Publish()     (notify)
//	                                 │
//	                            key actor          (owns that key's read model)
//	                                 │
//	                            subscribers        (one SSE stream per viewer)
//
// One goroutine per key owns that key's report and is the only thing that
// mutates it, so the read model needs no lock: credits from the write side and
// periodic resyncs from the store are both applied by that single goroutine, in
// order. Subscribers receive snapshots — immutable copies — so a slow reader can
// never hold up the writer.
//
// An actor exists only while somebody is watching: the first subscriber starts
// it, the last one to leave stops it. Nothing accumulates for keys nobody has
// open, and a closed browser tab reaps its goroutine.
//
// The point of all this is latency. A dashboard that polls shows a number that
// is up to one poll interval stale, and pays a database query per viewer per
// tick to stay that stale. Here a recorded call reaches the screen as fast as
// the network allows, and the store is read once per resync per *watched* key
// rather than once per tick per viewer.
package live
