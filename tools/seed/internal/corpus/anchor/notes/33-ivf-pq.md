---
title: IVF and product quantization
author: ml
editors: storage
tags: vector-search, compression
created: 160
---
An inverted file index clusters vectors with k-means and searches only the nprobe closest clusters. Product quantization compresses each vector into a few bytes by quantizing sub-vectors separately.

- nlist = 4,096 clusters for 1M vectors (about 4 times the square root of n)
- nprobe = 32 gives recall@10 of 0.91 without re-ranking
- PQ with 64 sub-quantizers of 8 bits: 64 bytes per vector instead of 3 KiB

Re-ranking the top 200 PQ candidates with exact distances brings recall@10 back to 0.97.

Compared with [[HNSW parameters]], IVF-PQ uses a twentieth of the memory and is about three times slower at the same recall. Back to [[Vector search overview]].
