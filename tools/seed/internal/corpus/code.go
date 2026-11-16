package corpus

// CodeTemplate is a code block template. Slots, filled by the generator from
// the note's topic vocabulary (so code carries topic signal too) and from
// the workspace's planted codenames:
//
//	{Type}   PascalCase type name, e.g. "ReplicaMerge"
//	{func}   camelCase function name, e.g. "mergeReplica"
//	{snake}  snake_case name, e.g. "merge_replica"
//	{table}  snake_case plural table name, e.g. "replica_merges"
//	{kebab}  kebab-case name, e.g. "replica-merge"
//	{n}      a small number
type CodeTemplate struct {
	Language string
	Source   string
}

// CodeTemplates are the code block templates, grouped by language.
var CodeTemplates = []CodeTemplate{
	{"go", `type {Type} struct {
	mu    sync.Mutex
	items map[string]int
	limit int
}

func (s *{Type}) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = nil
	return nil
}`},
	{"go", `func {func}(ctx context.Context, id string) (*{Type}, error) {
	ctx, cancel := context.WithTimeout(ctx, {n}*time.Second)
	defer cancel()
	row, err := store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("{snake} %s: %w", id, err)
	}
	return row, nil
}`},
	{"go", `func Test{Type}RejectsStaleInput(t *testing.T) {
	got := {func}(nil)
	if got != nil {
		t.Fatalf("{func}(nil) = %v, want nil", got)
	}
}`},
	{"go", `for attempt := 0; attempt < {n}; attempt++ {
	if err := {func}(ctx); err == nil {
		break
	}
	time.Sleep(backoff(attempt))
}`},
	{"rust", `pub struct {Type} {
    inner: Vec<u64>,
    cap: usize,
}

impl Drop for {Type} {
    fn drop(&mut self) {
        self.inner.clear();
    }
}`},
	{"rust", `pub async fn {snake}(pool: &PgPool, id: Uuid) -> Result<{Type}, Error> {
    let row = sqlx::query_as::<_, {Type}>("SELECT * FROM {table} WHERE id = $1")
        .bind(id)
        .fetch_one(pool)
        .await?;
    Ok(row)
}`},
	{"rust", `#[test]
fn {snake}_survives_restart() {
    let mut s = {Type}::new({n});
    s.insert(b"k", b"v");
    assert_eq!(s.get(b"k"), Some(b"v".as_slice()));
}`},
	{"rust", `impl {Type} {
    pub fn {snake}(&self, key: &[u8]) -> Option<&[u8]> {
        self.index.get(key).map(|v| v.as_slice())
    }
}`},
	{"python", `def {snake}(rows: list[dict], k: int = {n}) -> list[dict]:
    """Return the top k rows by score."""
    ranked = sorted(rows, key=lambda r: r["score"], reverse=True)
    return ranked[:k]`},
	{"python", `def test_{snake}_handles_empty_input():
    assert {snake}([]) == []`},
	{"python", `class {Type}:
    def __init__(self, path: str) -> None:
        self.path = path
        self.cache: dict[str, float] = {}

    def load(self) -> None:
        with open(self.path) as f:
            for line in f:
                key, value = line.split("\t")
                self.cache[key] = float(value)`},
	{"python", `import numpy as np

def {snake}(vectors: np.ndarray) -> np.ndarray:
    norms = np.linalg.norm(vectors, axis=1, keepdims=True)
    return vectors / np.maximum(norms, 1e-{n})`},
	{"sql", `SELECT count(*) AS total, date_trunc('day', created_at) AS day
FROM {table}
WHERE created_at > now() - interval '{n} days'
GROUP BY day
ORDER BY day;`},
	{"sql", `CREATE INDEX CONCURRENTLY IF NOT EXISTS {table}_created_idx
    ON {table} (workspace_id, created_at DESC);`},
	{"sql", `EXPLAIN (ANALYZE, BUFFERS)
SELECT n.id, n.title
FROM {table} n
JOIN memberships m ON m.workspace_id = n.workspace_id
WHERE m.user_id = $1
LIMIT {n};`},
	{"sql", `UPDATE {table}
SET status = 'archived'
WHERE updated_at < now() - interval '{n} days'
RETURNING id;`},
	{"typescript", `export function {func}(items: {Type}[], limit = {n}): {Type}[] {
  return items
    .filter((item) => item.visible)
    .slice(0, limit);
}`},
	{"typescript", `export async function {func}Handler(req: Request): Promise<Response> {
  const body = await req.json();
  if (!body.id) {
    return new Response("missing id", { status: 400 });
  }
  return Response.json(await load{Type}(body.id));
}`},
	{"typescript", `interface {Type} {
  id: string;
  updatedAt: number;
  tags: string[];
}

const empty{Type}: {Type} = { id: "", updatedAt: 0, tags: [] };`},
	{"typescript", `useEffect(() => {
  const id = setInterval(() => {func}(), {n}000);
  return () => clearInterval(id);
}, []);`},
	{"yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {kebab}
spec:
  replicas: {n}
  template:
    spec:
      containers:
        - name: {kebab}
          image: registry.example/{kebab}:1.{n}.0
          resources:
            requests: {cpu: 250m, memory: 256Mi}`},
	{"yaml", `groups:
  - name: {kebab}
    rules:
      - alert: {Type}ErrorBudgetBurn
        expr: sum(rate({snake}_errors_total[5m])) > {n}
        for: 10m
        labels:
          severity: page`},
	{"yaml", `{snake}:
  enabled: true
  batch_size: {n}
  retry:
    attempts: 3
    backoff: 200ms`},
}
