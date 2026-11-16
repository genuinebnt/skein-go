package corpus

// Topic is one generating topic of the procedural notes. Clustering ground
// truth is the topic each note was generated from, so the vocabularies are
// kept largely disjoint: that is the signal a clusterer should recover.
//
// Template syntax, filled by the generator:
//
//	{w}  a word from Vocab (drawn independently each time)
//	{W}  the same, capitalised
//	{n}  a small number
//	{x}  a word from a different, randomly chosen topic (cross-topic noise)
type Topic struct {
	ID    string
	Name  string
	Vocab []string
	// Phrases are sentence templates.
	Phrases []string
	// Titles are note title templates.
	Titles []string
	// Languages are the code languages this topic's notes use, most likely
	// first.
	Languages []string
}

// Topics are the 20 generating topics.
var Topics = []Topic{
	{
		ID: "crdt", Name: "CRDTs and sync",
		Vocab: []string{"crdt", "replica", "tombstone", "convergence", "merge", "causal", "vector", "clock", "lamport", "fugue",
			"interleaving", "sequence", "delta", "anti-entropy", "gossip", "idempotent", "commutative", "offline", "sync", "op-log",
			"version", "snapshot", "compaction", "cursor", "anchor", "conflict", "concurrent", "peer", "awareness", "presence"},
		Phrases: []string{
			"Every {w} applies the same {w} set, so the {w} is guaranteed once delivery is {w}.",
			"We keep the {w} in the {w} until every {w} has acknowledged it.",
			"A {w} that arrives early waits in the buffer until its {w} is applied.",
			"The {w} tie-break orders siblings by {w}, which keeps concurrent runs from {w}.",
			"Garbage collecting a {w} is only safe after the {w} proves no {w} still references it.",
			"Offline edits replay as one {w} batch when the {w} reconnects.",
			"Measured {n} ms to merge a {n}-op {w} on a laptop.",
			"The {w} must be {w} because the network duplicates messages.",
		},
		Titles:    []string{"{W} and {w} notes", "Why the {w} needs a {w}", "{W} garbage collection", "Debugging {w} divergence", "{W} {w} design review"},
		Languages: []string{"rust", "typescript", "go"},
	},
	{
		ID: "consensus", Name: "Raft and consensus",
		Vocab: []string{"raft", "leader", "follower", "candidate", "term", "election", "heartbeat", "quorum", "majority", "log",
			"commit", "index", "append-entries", "vote", "split-brain", "lease", "membership", "joint-consensus", "snapshot", "install",
			"timeout", "linearizable", "read-index", "learner", "paxos", "fencing", "epoch", "durability", "fsync", "partition"},
		Phrases: []string{
			"A {w} steps down as soon as it sees a higher {w}.",
			"The {w} only advances the {w} once a {w} has persisted the entry.",
			"Randomised {w} values between {n} and {n} ms keep {w} storms rare.",
			"{W} reads go through the {w} so they stay {w}.",
			"During a {w} the old {w} keeps serving stale reads unless the {w} expires.",
			"We saw {n} {w} changes in an hour when the {w} was misconfigured.",
			"Adding a node starts it as a {w} before it joins the {w}.",
			"The {w} must {w} before acknowledging, or a crash loses committed entries.",
		},
		Titles:    []string{"{W} {w} timeouts", "{W} during a {w}", "Notes on {w} and {w}", "{W} safety argument", "Operating the {w} cluster"},
		Languages: []string{"go", "rust"},
	},
	{
		ID: "rust-async", Name: "Rust async",
		Vocab: []string{"tokio", "future", "poll", "waker", "executor", "pin", "unpin", "async", "await", "spawn",
			"join", "select", "cancellation", "runtime", "task", "blocking", "send", "sync", "lifetime", "borrow",
			"channel", "mpsc", "backpressure", "semaphore", "mutex", "trait", "generic", "tower", "service", "stream"},
		Phrases: []string{
			"A {w} does nothing until something calls {w} on it.",
			"Holding a {w} guard across an {w} point blocks every other {w} on that thread.",
			"Use {w} for CPU-heavy work so the {w} stays responsive.",
			"Dropping a {w} is how {w} works: there is no separate cancel call.",
			"The {w} bound is required because the {w} may move between threads.",
			"A bounded {w} gives us {w} for free when consumers fall behind.",
			"We cut p99 by {n} ms by removing a {w} inside the {w} loop.",
			"{W} with a {w} lets the compiler check the {w} at build time.",
		},
		Titles:    []string{"{W} pitfalls in our {w}", "Understanding {w} and {w}", "{W} without {w}", "Async {w} patterns", "When to reach for {w}"},
		Languages: []string{"rust"},
	},
	{
		ID: "lsm", Name: "LSM storage engines",
		Vocab: []string{"memtable", "sstable", "compaction", "level", "bloom", "filter", "wal", "flush", "write-amplification", "read-amplification",
			"tombstone", "manifest", "block-cache", "prefix", "iterator", "merge", "tiered", "leveled", "key-range", "fence",
			"checksum", "mmap", "page", "fragment", "l0", "stall", "throttle", "zstd", "restart-point", "sequence-number"},
		Phrases: []string{
			"When {w} files pile up, writes {w} until {w} catches up.",
			"A {w} with {n} bits per key cuts point reads to roughly one {w}.",
			"{W} compaction trades {w} for lower space overhead.",
			"Every {w} is checksummed so a torn {w} is detected on open.",
			"The {w} records which {w} files are live at each version.",
			"Range deletes write a {w} that hides keys until {w} drops them.",
			"We measured {n} MB/s of {w} during the backfill.",
			"The {w} is replayed on startup to rebuild the {w}.",
		},
		Titles:    []string{"{W} tuning", "{W} versus {w}", "Reading the {w} code", "{W} stalls under load", "Designing the {w} format"},
		Languages: []string{"rust", "go", "sql"},
	},
	{
		ID: "vector", Name: "Vector search",
		Vocab: []string{"embedding", "cosine", "hnsw", "ivf", "centroid", "recall", "quantization", "product-quantization", "graph", "neighbor",
			"ef-search", "ef-construction", "dimension", "normalize", "dot-product", "faiss", "index", "shard", "rerank", "hybrid",
			"bm25", "candidate", "probe", "nlist", "layer", "entry-point", "distance", "ann", "brute-force", "k-nearest"},
		Phrases: []string{
			"Raising {w} to {n} lifted {w} at 10 from 0.91 to 0.97.",
			"{W} vectors before indexing so {w} equals the {w}.",
			"The {w} probes {n} lists, which bounds the {w} cost.",
			"Every {w} result is checked against a {w} scan in tests.",
			"{W} retrieval blends {w} scores with {w} similarity.",
			"Memory per vector drops {n}x with {w}, at some cost in {w}.",
			"We rebuild the {w} nightly because deletes fragment the {w}.",
			"A {w} with too few layers degrades into a {w} walk.",
		},
		Titles:    []string{"{W} parameter sweep", "Choosing between {w} and {w}", "{W} recall regression", "{W} for semantic search", "Benchmarking {w}"},
		Languages: []string{"python", "rust"},
	},
	{
		ID: "observability", Name: "Observability",
		Vocab: []string{"trace", "span", "metric", "histogram", "exemplar", "cardinality", "label", "exporter", "collector", "sampling",
			"opentelemetry", "prometheus", "grafana", "alert", "slo", "error-budget", "burn-rate", "dashboard", "log", "structured",
			"correlation", "baggage", "propagation", "jaeger", "percentile", "scrape", "counter", "gauge", "otlp", "tail-sampling"},
		Phrases: []string{
			"Each {w} carries the trace id so a {w} can jump from log to {w}.",
			"High {w} {w} values blew up the {w} bill.",
			"The {w} alert fires when the {w} exceeds {n}x for an hour.",
			"{W} keeps every error {w} and {n} percent of the rest.",
			"We export {w} over {w} to the {w}.",
			"The p99 {w} hides tail latency that the {w} shows clearly.",
			"Add an {w} to the {w} so the {w} links to a real request.",
			"{W} context must cross the queue or the {w} breaks.",
		},
		Titles:    []string{"{W} conventions", "Fixing {w} {w}", "Our {w} pipeline", "{W} and {w} alerts", "{W} cheat sheet"},
		Languages: []string{"yaml", "go", "python"},
	},
	{
		ID: "kubernetes", Name: "Kubernetes and deployment",
		Vocab: []string{"pod", "deployment", "service", "ingress", "helm", "kustomize", "namespace", "node", "taint", "toleration",
			"affinity", "hpa", "readiness", "liveness", "probe", "configmap", "secret", "rollout", "canary", "kubectl",
			"daemonset", "statefulset", "pvc", "operator", "crd", "admission", "sidecar", "cronjob", "limits", "requests"},
		Phrases: []string{
			"The {w} restarted {n} times because the {w} probe was too strict.",
			"Set {w} and {w} on every container or the scheduler guesses.",
			"A {w} rollout shifts {n} percent of traffic before promoting.",
			"The {w} keeps one replica per {w} for the log shipper.",
			"Mount the {w} as a file instead of an env var so rotation works.",
			"An {w} webhook rejects images without a digest.",
			"Use {w} so the {w} lands on nodes with local SSDs.",
			"The {w} scales from {n} to {n} pods on CPU.",
		},
		Titles:    []string{"{W} rollout checklist", "Debugging {w} restarts", "{W} and {w} defaults", "Migrating to {w}", "{W} runbook"},
		Languages: []string{"yaml", "go"},
	},
	{
		ID: "networking", Name: "Networking",
		Vocab: []string{"tcp", "udp", "quic", "tls", "handshake", "latency", "bandwidth", "mtu", "congestion", "window",
			"retransmit", "keepalive", "dns", "anycast", "bgp", "load-balancer", "nat", "socket", "epoll", "backlog",
			"http2", "grpc", "stream", "multiplexing", "head-of-line", "jitter", "packet", "route", "proxy", "connection-pool"},
		Phrases: []string{
			"{W} avoids {w} blocking that {w} suffers from.",
			"The {w} drops idle connections after {n} seconds, so we send a {w}.",
			"Raising the accept {w} fixed the SYN drops during deploys.",
			"A {w} cache with a {n}s TTL hid the failover for minutes.",
			"Long-lived {w} connections pin to one backend behind the {w}.",
			"{W} resumption cuts the {w} to one round trip.",
			"Path {w} problems show up as hung transfers above {n} bytes.",
			"The {w} reuses sockets so we stop paying the {w} cost.",
		},
		Titles:    []string{"{W} tuning notes", "{W} versus {w}", "Tracing a {w} stall", "{W} for internal services", "Understanding {w}"},
		Languages: []string{"go", "rust"},
	},
	{
		ID: "security", Name: "Auth and security",
		Vocab: []string{"oauth", "oidc", "jwt", "token", "refresh", "session", "argon2", "hash", "salt", "csrf",
			"xss", "cors", "scope", "rbac", "permission", "audit", "secret", "rotation", "mfa", "webauthn",
			"pkce", "nonce", "signature", "jwks", "revocation", "phishing", "least-privilege", "tenant", "encryption", "kms"},
		Phrases: []string{
			"Access {w} live {n} minutes; the {w} rotates on every use.",
			"Store only a {w} of the share link, never the {w} itself.",
			"Every service checks the {w} itself instead of trusting the gateway.",
			"{W} with {w} protects the code exchange on public clients.",
			"The {w} log records who changed which {w} and when.",
			"A leaked {w} is useless once {w} propagates.",
			"We moved {w} into the {w} and cut the blast radius.",
			"Passwords use {w} with a per-user {w}.",
		},
		Titles:    []string{"{W} threat model", "{W} rotation plan", "Reviewing {w} and {w}", "{W} incident notes", "{W} hardening"},
		Languages: []string{"go", "typescript", "sql"},
	},
	{
		ID: "frontend", Name: "Frontend and React",
		Vocab: []string{"react", "component", "hook", "state", "effect", "render", "hydration", "suspense", "props", "context",
			"memo", "reducer", "router", "bundle", "vite", "css", "layout", "reflow", "accessibility", "aria",
			"focus", "keyboard", "virtualization", "selection", "caret", "contenteditable", "wasm", "worker", "indexeddb", "service-worker"},
		Phrases: []string{
			"The {w} re-renders on every keystroke because the {w} is recreated.",
			"Move the {w} into a {w} so the main thread stays free.",
			"{W} the list so only {n} rows mount at a time.",
			"An {w} label fixes the screen reader announcing nothing.",
			"Mapping the browser {w} to code points happens at the {w} boundary.",
			"The {w} cuts {n} KB from the first {w}.",
			"Restore {w} after the modal closes or keyboard users get lost.",
			"Persist drafts in {w} so a reload keeps the {w}.",
		},
		Titles:    []string{"{W} performance pass", "{W} and {w} in the editor", "Accessible {w}", "{W} state management", "Fixing {w} jank"},
		Languages: []string{"typescript"},
	},
	{
		ID: "ml-training", Name: "Model training",
		Vocab: []string{"epoch", "batch", "gradient", "learning-rate", "optimizer", "adam", "loss", "overfitting", "regularization", "dropout",
			"validation", "checkpoint", "warmup", "scheduler", "fine-tune", "dataset", "label", "augmentation", "gpu", "mixed-precision",
			"evaluation", "baseline", "ablation", "hyperparameter", "sweep", "tokenizer", "convergence", "weight-decay", "inference", "distillation"},
		Phrases: []string{
			"A {w} schedule with {n} steps of {w} stopped the early divergence.",
			"{W} loss climbed after epoch {n}: classic {w}.",
			"{W} halved memory with no change in {w}.",
			"Every {w} keeps the {w} and the data hash for reproducibility.",
			"The {w} beats the {w} by {n} points on the held-out set.",
			"An {w} without the new {w} shows where the gain comes from.",
			"Lowering the {w} fixed the {w} spikes.",
			"Clean {w} mattered more than a bigger {w}.",
		},
		Titles:    []string{"{W} experiment log", "{W} sweep results", "Why the {w} diverged", "{W} versus {w}", "Training {w} notes"},
		Languages: []string{"python", "yaml"},
	},
	{
		ID: "streaming", Name: "Stream processing",
		Vocab: []string{"kafka", "pubsub", "topic", "partition", "offset", "consumer", "producer", "watermark", "window", "late-data",
			"exactly-once", "at-least-once", "idempotency", "outbox", "dead-letter", "ordering-key", "backfill", "replay", "schema", "avro",
			"protobuf", "lag", "rebalance", "checkpoint", "state-store", "join", "aggregation", "flink", "throughput", "retention"},
		Phrases: []string{
			"Consumer {w} hit {n} million messages during the {w}.",
			"The {w} pattern writes the event in the same transaction as the {w}.",
			"{W} messages go to the {w} after {n} attempts.",
			"A {w} lets late events land in the right {w}.",
			"{W} delivery means every handler must be {w}.",
			"A {w} paused all consumers for {n} seconds.",
			"Set an {w} so events for one note stay in order.",
			"{W} the topic from the start to rebuild the {w}.",
		},
		Titles:    []string{"{W} consumer design", "Handling {w} and {w}", "{W} lag incident", "{W} schema evolution", "Our {w} topology"},
		Languages: []string{"go", "python", "sql"},
	},
	{
		ID: "query", Name: "SQL query planning",
		Vocab: []string{"postgres", "index", "b-tree", "gin", "trigram", "explain", "analyze", "planner", "seq-scan", "index-scan",
			"bitmap", "join", "hash-join", "nested-loop", "statistics", "vacuum", "bloat", "cte", "partitioning", "lock",
			"deadlock", "transaction", "isolation", "serializable", "mvcc", "replica-lag", "connection", "pgbouncer", "migration", "cardinality"},
		Phrases: []string{
			"{W} showed a {w} over {n} thousand rows.",
			"Adding a {w} index turned the {w} into an {w}.",
			"Stale {w} made the {w} pick a {w}.",
			"Autovacuum fell behind and {w} doubled the table size.",
			"{W} isolation retries about {n} transactions an hour.",
			"Run the {w} with a lock timeout so it cannot block writes.",
			"A {w} by month keeps old data off the hot {w}.",
			"{W} in transaction mode caps connections at {n}.",
		},
		Titles:    []string{"{W} slow query review", "When the {w} picks a {w}", "{W} and {w}", "Safe {w} checklist", "{W} tuning"},
		Languages: []string{"sql", "go"},
	},
	{
		ID: "compilers", Name: "Compilers and parsing",
		Vocab: []string{"lexer", "parser", "token", "ast", "grammar", "precedence", "recursive-descent", "pratt", "ir", "ssa",
			"lowering", "codegen", "optimizer", "inlining", "type-check", "inference", "symbol", "scope", "diagnostic", "span",
			"recovery", "bytecode", "interpreter", "closure", "garbage-collector", "register", "constant-folding", "desugar", "macro", "incremental"},
		Phrases: []string{
			"The {w} emits a recovery {w} instead of stopping at the first error.",
			"{W} parsing handles {w} with one table of binding powers.",
			"Every {w} keeps a byte {w} for {w} output.",
			"{W} happens before {w} so later passes see one form.",
			"The {w} is {n} times faster than walking the {w}.",
			"An {w} reparse touches only the edited {w}.",
			"{W} resolves every name to a {w} or a {w}.",
			"Round-trip tests catch dropped {w} nodes.",
		},
		Titles:    []string{"Writing the {w}", "{W} error {w}", "{W} design notes", "From {w} to {w}", "{W} benchmarks"},
		Languages: []string{"rust", "go", "typescript"},
	},
	{
		ID: "testing", Name: "Testing and QA",
		Vocab: []string{"fuzz", "property", "oracle", "fixture", "golden", "snapshot", "flaky", "mutation", "coverage", "invariant",
			"regression", "integration", "unit", "contract", "testcontainers", "mock", "fake", "seed", "shrink", "determinism",
			"race", "timeout", "harness", "benchmark", "e2e", "playwright", "assertion", "corpus", "conformance", "replay"},
		Phrases: []string{
			"The {w} test found a crash in {n} seconds that review missed.",
			"Compare against a slow {w} instead of hand-written examples.",
			"Log the {w} so a {w} failure can be replayed exactly.",
			"A {w} file shared with the port keeps behaviour identical.",
			"{W} tests stopped failing once the {w} was removed.",
			"Check the {w} after every operation, not only at the end.",
			"Use a {w} behind the interface so the {w} needs no network.",
			"{W} testing killed {n} percent of the mutants.",
		},
		Titles:    []string{"{W} strategy", "Taming {w} tests", "{W} and {w} in CI", "Writing a good {w}", "{W} lessons"},
		Languages: []string{"go", "python", "typescript"},
	},
	{
		ID: "incidents", Name: "Incident response",
		Vocab: []string{"incident", "pager", "on-call", "severity", "postmortem", "timeline", "mitigation", "rollback", "root-cause", "blameless",
			"runbook", "escalation", "status-page", "customer-impact", "detection", "outage", "degradation", "action-item", "retro", "war-room",
			"hotfix", "freeze", "toil", "alert-fatigue", "handoff", "commander", "comms", "recovery", "follow-up", "sev2"},
		Phrases: []string{
			"The {w} paged at 03:{n} and the {w} started within minutes.",
			"{W} took {n} minutes; full {w} took two hours.",
			"The {w} names systems, never people.",
			"Every {w} gets an owner and a due date.",
			"A {w} would have shortened {w} by half.",
			"We declared a {w} once {w} passed five percent.",
			"The {w} updated the {w} every thirty minutes.",
			"{W} was the fastest {w}, so we did that first.",
		},
		Titles:    []string{"{W} review", "{W} for the {w}", "{W} process changes", "Lessons from the {w}", "{W} template"},
		Languages: []string{"yaml", "sql"},
	},
	{
		ID: "finops", Name: "Cloud cost",
		Vocab: []string{"budget", "spend", "reserved", "spot", "preemptible", "egress", "storage-class", "rightsizing", "commitment", "invoice",
			"tagging", "chargeback", "showback", "forecast", "anomaly", "savings-plan", "idle", "autoscaling", "cold-storage", "lifecycle",
			"discount", "unit-cost", "cost-per-request", "billing-export", "quota", "overprovisioned", "nat-gateway", "cross-zone", "archive", "retention"},
		Phrases: []string{
			"{W} fees were {n} percent of the bill last month.",
			"Moving logs to {w} after {n} days saves most of the cost.",
			"{W} nodes run batch jobs at a fraction of the price.",
			"Every resource needs a {w} for {w} to work.",
			"The {w} alert caught a runaway job within a day.",
			"{W} cut the compute line by {n} percent.",
			"Our {w} is now tracked per team in the {w}.",
			"Most {w} came from {w} dev clusters.",
		},
		Titles:    []string{"{W} review for Q{n}", "Cutting {w}", "{W} and {w}", "{W} dashboard notes", "{W} policy"},
		Languages: []string{"sql", "python", "yaml"},
	},
	{
		ID: "mobile", Name: "Mobile apps",
		Vocab: []string{"ios", "android", "swiftui", "kotlin", "compose", "push", "notification", "deep-link", "app-store", "review",
			"crash", "symbolication", "battery", "background-fetch", "offline-first", "sqlite", "sync", "widget", "haptics", "permissions",
			"testflight", "release-train", "feature-flag", "cold-start", "frame-drop", "accessibility", "dark-mode", "localization", "keychain", "analytics"},
		Phrases: []string{
			"{W} time dropped to {n} ms after lazy loading the {w}.",
			"The {w} build goes to {w} every Thursday.",
			"A {w} opens the right note even when the app is killed.",
			"{W} rate is {n} per thousand sessions after the fix.",
			"Store tokens in the {w}, never in {w}.",
			"{W} lets us ship dark code and turn it on per cohort.",
			"The {w} drains the {w} when the radio wakes too often.",
			"{W} strings come from the same catalogue as the web app.",
		},
		Titles:    []string{"{W} release notes", "{W} performance", "Shipping {w}", "{W} and {w} on mobile", "{W} bug bash"},
		Languages: []string{"typescript", "yaml"},
	},
	{
		ID: "product", Name: "Product planning",
		Vocab: []string{"roadmap", "milestone", "okr", "persona", "interview", "onboarding", "retention", "activation", "churn", "funnel",
			"experiment", "hypothesis", "feedback", "backlog", "prioritization", "scope", "launch", "beta", "pricing", "positioning",
			"stakeholder", "discovery", "prototype", "usability", "jobs-to-be-done", "north-star", "cohort", "survey", "nps", "requirements"},
		Phrases: []string{
			"Five {w} sessions pointed at the same {w} problem.",
			"{W} improved {n} percent once the empty state explained the {w}.",
			"We cut {w} to hit the {w} date.",
			"The {w} names the user and the outcome, not the feature.",
			"Run the {w} for two weeks before reading the {w}.",
			"{W} is the metric we review every Monday.",
			"The {w} goes to {n} design partners first.",
			"Every {w} item links to the {w} it serves.",
		},
		Titles:    []string{"{W} for next quarter", "{W} findings", "{W} plan", "{W} and {w} review", "Planning the {w}"},
		Languages: []string{"sql", "yaml"},
	},
	{
		ID: "caching", Name: "Caching",
		Vocab: []string{"cache", "redis", "ttl", "eviction", "lru", "lfu", "arc", "hit-rate", "miss", "stampede",
			"invalidation", "write-through", "write-back", "read-through", "memcached", "warmup", "hot-key", "consistent-hashing", "shard", "serialization",
			"singleflight", "stale-while-revalidate", "cdn", "etag", "negative-cache", "admission", "tinylfu", "capacity", "expiry", "jitter"},
		Phrases: []string{
			"{W} on the {w} sent {n} thousand requests to the database at once.",
			"Add {w} to the {w} so keys do not expire together.",
			"{W} rose to {n} percent after switching to {w}.",
			"{W} is the hard part; {w} is easy.",
			"A {w} on one {w} saturated a single shard.",
			"{W} collapses concurrent misses into one load.",
			"Serve {w} data while the {w} refreshes in the background.",
			"The {w} policy keeps one-hit wonders out of the {w}.",
		},
		Titles:    []string{"{W} design", "{W} incident", "{W} versus {w}", "Tuning the {w}", "{W} invalidation rules"},
		Languages: []string{"go", "python", "rust"},
	},
}

// GeneralWords are topic-neutral words mixed into every note, so topic words
// are signal but not the only words.
var GeneralWords = []string{
	"team", "week", "plan", "question", "decision", "follow-up", "draft", "update", "review", "context",
	"problem", "approach", "tradeoff", "summary", "owner", "deadline", "risk", "assumption", "example", "detail",
}

// Connectives start or join sentences in generated paragraphs.
var Connectives = []string{"Also,", "In practice,", "Note that", "So far,", "Last week", "For now,", "Honestly,", "Next,"}

// Typos maps correctly spelled words to a common misspelling. Generated
// notes keep some of these uncorrected, so typo-tolerant search has real
// misspellings to find.
var Typos = []struct{ Right, Wrong string }{
	{"replica", "replcia"}, {"convergence", "convergance"}, {"leader", "laeder"}, {"election", "elction"},
	{"compaction", "compacton"}, {"embedding", "embeding"}, {"recall", "recal"}, {"histogram", "histgram"},
	{"deployment", "deploymnet"}, {"handshake", "handshkae"}, {"permission", "permision"}, {"component", "compnent"},
	{"gradient", "gradiant"}, {"partition", "partiton"}, {"transaction", "transacton"}, {"grammar", "grammer"},
	{"regression", "regresion"}, {"postmortem", "post-mortem"}, {"budget", "budjet"}, {"notification", "notifcation"},
	{"retention", "retension"}, {"eviction", "evicton"}, {"separate", "seperate"}, {"definitely", "definately"},
}
