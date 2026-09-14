#!/usr/bin/env python3
"""从 T2Retrieval 全量 JSONL 反选一个「相关段落 + 负样本池」的子集。

目的：召回率评测需要在完整候选集上检索才有意义，但全量 11.8 万段向量化太慢。
本脚本按 qrels 反选，保证选中 query 的所有相关段落都在候选集里，再补入负样本，
从而用可控规模得到可靠的 Recall@K / MRR / NDCG。

用法：
  py select_subset.py [--num_queries 100] [--num_neg 2000] [--data_dir ...] [--out_dir ...]
"""
import argparse
import json
import os


def load_jsonl(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data_dir", default="data/t2retrieval_jsonl")
    ap.add_argument("--out_dir", default="data/t2retrieval_subset")
    ap.add_argument("--num_queries", type=int, default=100)
    ap.add_argument("--num_neg", type=int, default=2000)
    args = ap.parse_args()

    corpus = load_jsonl(os.path.join(args.data_dir, "corpus.jsonl"))
    queries = load_jsonl(os.path.join(args.data_dir, "queries.jsonl"))
    qrels = load_jsonl(os.path.join(args.data_dir, "qrels.jsonl"))

    # 前 N 条 query
    sel_queries = queries[: args.num_queries]
    sel_qids = {q["id"] for q in sel_queries}

    # 选中 query 的所有相关 pid
    rel_pids = set()
    for q in qrels:
        if q["qid"] in sel_qids:
            rel_pids.add(q["pid"])

    # 相关段落 + 负样本
    rel_corpus = [c for c in corpus if c["id"] in rel_pids]
    neg_corpus = []
    for c in corpus:
        if c["id"] in rel_pids:
            continue
        neg_corpus.append(c)
        if len(neg_corpus) >= args.num_neg:
            break

    subset_corpus = rel_corpus + neg_corpus
    subset_pids = {c["id"] for c in subset_corpus}

    os.makedirs(args.out_dir, exist_ok=True)
    with open(os.path.join(args.out_dir, "corpus.jsonl"), "w", encoding="utf-8") as f:
        for c in subset_corpus:
            f.write(json.dumps(c, ensure_ascii=False) + "\n")
    with open(os.path.join(args.out_dir, "queries.jsonl"), "w", encoding="utf-8") as f:
        for q in sel_queries:
            f.write(json.dumps(q, ensure_ascii=False) + "\n")

    n_qrels = 0
    with open(os.path.join(args.out_dir, "qrels.jsonl"), "w", encoding="utf-8") as f:
        for q in qrels:
            if q["qid"] in sel_qids and q["pid"] in subset_pids:
                f.write(json.dumps(q, ensure_ascii=False) + "\n")
                n_qrels += 1

    print(f"queries={len(sel_queries)} rel_pids={len(rel_pids)} neg={len(neg_corpus)} "
          f"corpus={len(subset_corpus)} qrels={n_qrels} -> {args.out_dir}")


if __name__ == "__main__":
    main()
