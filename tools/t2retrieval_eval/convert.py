#!/usr/bin/env python3
"""T2Retrieval parquet -> JSONL 转换脚本。

功能：
1. 读取 corpus / queries / qrels 三个 parquet；
2. 清洗 corpus 文本中的 HTML 标签（<br>/<img> 等）；
3. 可选抽取前 N 条 query 子集（默认全量），并同步过滤 qrels；
4. 输出 JSONL，供 Go 检索评测工具加载。

用法：
  py convert.py [--top_queries 2000] [--data_dir ...] [--qrels_dir ...] [--out_dir ...]
"""
import argparse
import json
import re
import os

import pyarrow.parquet as pq


def clean_text(t):
    if t is None:
        return ""
    t = t.replace("<br>", "\n").replace("<br/>", "\n").replace("<br />", "\n")
    t = re.sub(r"<[^>]+>", "", t)  # 去除所有 HTML 标签
    t = re.sub(r"[ \t]+", " ", t)  # 压缩空白（保留换行）
    t = re.sub(r"\n{3,}", "\n\n", t)  # 压缩多余换行
    return t.strip()


def iter_rows(table):
    names = table.column_names
    cols = {n: table.column(n).to_pylist() for n in names}
    n = table.num_rows
    for i in range(n):
        yield {n: cols[n][i] for n in names}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data_dir", default="data/T2Retrieval")
    ap.add_argument("--qrels_dir", default="data/T2Retrieval-qrels")
    ap.add_argument("--out_dir", default="data/t2retrieval_jsonl")
    ap.add_argument("--top_queries", type=int, default=0, help="0=全部 query")
    args = ap.parse_args()

    corpus_pq = next(
        os.path.join(args.data_dir, "data", f)
        for f in os.listdir(os.path.join(args.data_dir, "data"))
        if f.startswith("corpus-") and f.endswith(".parquet")
    )
    queries_pq = next(
        os.path.join(args.data_dir, "data", f)
        for f in os.listdir(os.path.join(args.data_dir, "data"))
        if f.startswith("queries-") and f.endswith(".parquet")
    )
    qrels_pq = next(
        os.path.join(args.qrels_dir, "data", f)
        for f in os.listdir(os.path.join(args.qrels_dir, "data"))
        if f.endswith(".parquet")
    )

    os.makedirs(args.out_dir, exist_ok=True)

    corpus_tbl = pq.read_table(corpus_pq)
    corpus_out = os.path.join(args.out_dir, "corpus.jsonl")
    n_corpus = 0
    with open(corpus_out, "w", encoding="utf-8") as f:
        for row in iter_rows(corpus_tbl):
            f.write(json.dumps(
                {"id": str(row["id"]), "text": clean_text(row["text"])},
                ensure_ascii=False,
            ) + "\n")
            n_corpus += 1
    print(f"corpus: {n_corpus} 段 -> {corpus_out}")

    queries_tbl = pq.read_table(queries_pq)
    q_rows = [(int(row["id"]), str(row["id"]), row["text"]) for row in iter_rows(queries_tbl)]
    q_rows.sort(key=lambda x: x[0])
    if args.top_queries > 0:
        q_rows = q_rows[: args.top_queries]
    selected_qids = {qid for _, qid, _ in q_rows}
    queries_out = os.path.join(args.out_dir, "queries.jsonl")
    with open(queries_out, "w", encoding="utf-8") as f:
        for _, qid, text in q_rows:
            f.write(json.dumps({"id": qid, "text": text}, ensure_ascii=False) + "\n")
    print(f"queries: {len(q_rows)} 条 -> {queries_out}")

    qrels_tbl = pq.read_table(qrels_pq)
    qrels_out = os.path.join(args.out_dir, "qrels.jsonl")
    n_qrels = 0
    with open(qrels_out, "w", encoding="utf-8") as f:
        for row in iter_rows(qrels_tbl):
            if str(row["qid"]) not in selected_qids:
                continue
            f.write(json.dumps(
                {"qid": str(row["qid"]), "pid": str(row["pid"]), "score": int(row["score"])},
                ensure_ascii=False,
            ) + "\n")
            n_qrels += 1
    print(f"qrels: {n_qrels} 条 -> {qrels_out}")


if __name__ == "__main__":
    main()
