// Package alert implements alert dispatch for budget and runaway events via
// generic webhooks and Slack incoming webhooks.
//
// Deliveries go through a bounded in-memory queue drained by worker goroutines,
// so callers on the proxy request path never block on webhook latency. Each HTTP
// attempt is bounded by a timeout of at most ten seconds. Transport errors,
// 408, 429, and 5xx responses are retried up to three times with exponential
// backoff; other 4xx responses are treated as permanent. When the queue is full,
// new alerts are dropped and counted. Destination URLs are validated at
// construction and never appear unredacted in logs or errors.
package alert
