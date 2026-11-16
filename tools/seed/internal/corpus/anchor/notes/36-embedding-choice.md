---
title: Embedding model choice
author: ml
editors: owner
tags: vector-search, ml
created: 130
---
We compared three open embedding models on our own notes, labelled by two people for 300 query and note pairs.

| Model | Dimensions | nDCG@10 | Throughput on CPU |
| --- | --- | --- | --- |
| small-en | 384 | 0.68 | 950 blocks/s |
| base-multilingual | 768 | 0.74 | 310 blocks/s |
| large-en | 1024 | 0.76 | 90 blocks/s |

We chose base-multilingual: nearly the best quality, and it handles the Hindi, Japanese and German notes that the English models score poorly on.

Vectors are normalised to unit length at write time, so cosine similarity is a dot product. Back to [[Vector search overview]].
