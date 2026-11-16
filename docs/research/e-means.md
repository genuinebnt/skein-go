# e-means: notes for the `ml` service implementation

e-means is a mini-batch k-means from Meta FAIR, published in November 2026
as a drop-in replacement for Lloyd's algorithm. Skein implements it from
scratch in the `ml` service and benchmarks it against Lloyd (random and
k-means++ initialisation), Elkan, mini-batch (MacQueen) and the FAISS
reference implementation.

These notes were taken on 2026-11-16 from the FAISS reference code,
`contrib/e_means/` at commit `ebab8f7` (2026-11-13, MIT licence), and
checked against press coverage. Where the code and the paper differ, the code
says so in its comments; we follow the code, because it is what the
benchmarks compare against.

## Sources

- Paper: "E-Means: Scaling Laws for Mini-Batch K-Means", Bessa, Mazaré,
  Szilvasy and others, arXiv 2611.09501 (submitted 2026-11-06).
- Reference code: `facebookresearch/faiss`, `contrib/e_means/base.py` and
  `e_means.py` (`NumpyEmeans`). Shipped in FAISS 1.16.0 (2026-11-10).
- Reported results: below Lloyd's 25-epoch test loss in 1.2 to 3 epochs
  (1.4 to 2.6 in the paper's abstract); 10 to 45 times faster on GPU and 8 to
  15 times on CPU, wall clock to the same loss.

## Algorithm

Each cluster `j` keeps a mass `N_j` and a centroid `c_j` (equivalent to a
sum `S_j` with `c_j = S_j / N_j`). For one mini-batch, `n_j` and `s_j` are the
count and sum of the batch points assigned to `j`.

| Method | Update |
|---|---|
| Lloyd | `S ← s`, `N ← n` (whole data set per update) |
| MacQueen mini-batch | `S ← S + s`, `N ← N + n` |
| e-means | `S ← (1 − α)S + αs`, `N ← (1 − α)N + αn` |

With `B = n` and `α = 1`, one e-means update is exactly one Lloyd update.

The reference code applies the update in mass-and-centroid form:

```
γ = 1 − α
N_j ← γ · N_j                                  (every cluster, every update)
for each cluster with n_j > 0:
    N_new = N_j + α · n_j
    c_j  ← c_j + (α · n_j / N_new) · (s_j / n_j − c_j)
    N_j  ← N_new
clusters with n_j = 0 keep their position (their mass still decays)
```

### Run structure

1. **Initialise:** `k` distinct points sampled uniformly at random (a seeded
   permutation), not k-means++. Every `N_j` starts at `2^-16` (the paper uses
   `1e-10`).
2. **Epochs:** each epoch shuffles the data with a seed derived from
   `(seed · 1000003 + epoch) mod 2^32` and walks it in batches of `B`. The
   remainder batch is kept. Fractional budgets are supported: `T = 2.5` runs
   one full pass, a half pass, then a full closing pass.
3. **Step schedule:** cosine annealing over the total number of updates,
   from `α0` down to `0.1 · α0`:
   `α(t) = α0 · (0.1 + 0.9 · ½(1 + cos(π · t / total)))`.
4. **Revival every 5 updates:** clusters with `N_j < 0.1 · mean(N)` are dead.
   Donors (live clusters with `N_j > 0`) are sampled without replacement in
   proportion to `N_j` (Gumbel top-k on `log N_j`). Each donor is split in
   two: the donor moves to `c · (1 − δ·σ)` and the dead cluster to
   `c · (1 + δ·σ)`, with `δ = 1e-4` and `σ` alternating `+1, −1` across
   coordinates. Both get half the donor's mass. Revival is off when `d ≤ 2`,
   where it hurt held-out error in the authors' tests.
5. **Finishing stage:** during the last epoch, the plain per-cluster sums and
   counts of assigned points are accumulated alongside the EMA. The returned
   centroids are those exact means; clusters assigned nothing keep their
   position. No extra assignment pass is made, so labels from the last epoch
   can differ slightly from the final centroids.

### Choosing `B` and `α0` (scaling laws)

With `n` points, `k` clusters and `T` epochs:

```
B (throughput law) = 0.035134   · k^0.134749 · n^0.876473 · T^0.813344
B (quality law)    = 0.00439175 · k^0.134749 · n^0.876473 · T^0.813344
α0                 = 0.8244 · k^−0.4617 · B^0.9003 · n^−0.4498 · T^−0.1057
```

- `B` is rounded to the nearest power of two, then capped: throughput at the
  power of two nearest `n/2`, quality at `n // 2`, and both at `n`.
- The default law is quality when `d ≤ 16` and throughput otherwise.
- `α0` is computed from the resolved `B` and clamped to `[1e-4, 1]`.
- These are the shipped fits; the code notes that they differ from the
  paper's Table 2.

## What Skein implements

- `ml/clustering/e_means.py`: a NumPy implementation of the above, with the
  same defaults, seeded and deterministic, with `check_invariants` (masses
  non-negative, centroids finite, mass sum consistent with the decay).
- Tests: equals one Lloyd step when `B = n` and `α = 1`; the scaling laws
  reproduce the reference recipe for a grid of `(n, k, T)`; revival never
  leaves a cluster below the threshold when donors exist; on fixed data and
  seed, the final loss is within a tolerance of `faiss.contrib.e_means`.
- Benchmark rows: Lloyd with random and k-means++ initialisation, Elkan,
  MacQueen mini-batch, Gaussian mixtures by EM, e-means (scratch) and
  e-means (FAISS), reporting epochs, wall time, distance computations, final
  test loss and dead clusters.

## Open points to check against the paper

- The paper's own coefficients (Table 2) versus the shipped fits.
- Whether the paper evaluates initialisations other than random sampling;
  coverage mentioned "new initialisation methods".
