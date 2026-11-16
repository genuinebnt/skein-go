# 0015. ML and the agent in Python services

Date: 2026-11-16. Status: accepted.

## Context
LangChain and LangGraph, scikit-learn, NumPy, ONNX Runtime and the reference
vector libraries (hnswlib, FAISS) are mature in Python. The learning goal for
ML includes implementing algorithms from scratch and comparing them against
those references.

## Decision
- `ml` and `agent` are Python services (Python 3.12+, `uv` for dependencies,
  `grpcio` servers, `pytest`).
- In `ml`, each algorithm has a from-scratch NumPy implementation and a
  library baseline, run side by side in a benchmark harness: K-means++
  initialisation, Lloyd, Elkan and mini-batch K-means, e-means (Meta FAIR,
  2026; reference in `faiss.contrib.e_means`, notes in
  `docs/research/e-means.md`), Gaussian mixtures by EM, and brute-force, IVF
  and HNSW search. Benchmarks report time, iterations,
  inertia or log-likelihood, recall and memory.
- The Rust port does not port these services; equivalent Rust crates
  (`linfa`, `candle`) are noted in the porting guide for anyone who wants to.

## Consequences
- Two languages for services. Contracts in Protobuf and shared fixtures keep
  them aligned.
