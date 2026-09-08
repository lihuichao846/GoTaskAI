"""GoTaskAI 高并发 API 提交压测：测量 (1) 受理层 dispatcher 延迟与削峰, (2) 任务处理吞吐与 LLM 成本。

用法: python tools/bench_submit.py [--N 20] [--agent 7af4b09d-...]
"""
import argparse
import json
import re
import time
import urllib.request
import urllib.error

BASE = "http://127.0.0.1:8080"
WORKER_METRICS = ["http://127.0.0.1:9101/metrics", "http://127.0.0.1:9102/metrics"]
USERNAME = "bench_user"
PASSWORD = "bench_pass_123"
BENCH_AGENT = "7af4b09d-9cf2-4d1a-9030-3eae58815deb"


def http(method, path, body=None, token=None, timeout=300):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            code = r.status
            payload = r.read().decode()
    except urllib.error.HTTPError as e:
        code = e.code
        payload = e.read().decode(errors="replace")
    dt = (time.perf_counter() - t0) * 1000
    return code, payload, dt


def login():
    code, payload, _ = http("POST", "/api/auth/login", {"username": USERNAME, "password": PASSWORD})
    return json.loads(payload)["token"]


def fetch_metrics():
    total = {"in": 0.0, "out": 0.0, "calls": 0.0, "cost": 0.0, "completed": 0.0}
    for url in WORKER_METRICS:
        try:
            with urllib.request.urlopen(url, timeout=10) as r:
                body = r.read().decode()
        except Exception:
            continue
        for line in body.splitlines():
            if not line or line.startswith("#"):
                continue
            val = float(line.rsplit(" ", 1)[1])
            m = re.match(r"gotaskai_llm_tokens_total\{.*direction=\"(in|out)\"", line)
            if m:
                direction = m.group(1)
                total[direction] += val
                continue
            if line.startswith("gotaskai_llm_calls_total{"):
                total["calls"] += val
            elif line.startswith("gotaskai_llm_cost_usd_total{"):
                total["cost"] += val
            elif line.startswith('gotaskai_task_runs_total{status="completed"'):
                total["completed"] += val
    return total


def status_of(task_id, token):
    code, payload, _ = http("GET", "/api/tasks", token=token)
    if code != 200:
        return None
    try:
        tasks = json.loads(payload)
    except Exception:
        return None
    for t in tasks:
        if t.get("id") == task_id:
            return t.get("status")
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--N", type=int, default=20)
    ap.add_argument("--agent", default=BENCH_AGENT)
    args = ap.parse_args()

    token = login()
    print(f"[auth] logged in, token ok")

    base = fetch_metrics()
    print(f"[metrics] baseline  in={base['in']:.0f} out={base['out']:.0f} calls={base['calls']:.0f} "
          f"cost=${base['cost']:.6f} completed={base['completed']:.0f}")

    # ---- Phase B: 批量提交 N 个任务，测量吞吐与成本 ----
    prompts = [
        "用一句话解释什么是 HTTP 协议。",
        "用一句话解释什么是进程与线程的区别。",
        "用一句话解释什么是 TCP 三次握手。",
        "用一句话解释什么是数据库索引。",
        "用一句话解释什么是 JWT。",
        "用一句话解释什么是微服务。",
        "用一句话解释什么是消息队列。",
        "用一句话解释什么是缓存击穿。",
        "用一句话解释什么是负载均衡。",
        "用一句话解释什么是 CDN。",
        "用一句话解释什么是 RESTful API。",
        "用一句话解释什么是 Docker 容器。",
        "用一句话解释什么是 K8s。",
        "用一句话解释什么是 Redis 持久化。",
        "用一句话解释什么是分布式事务。",
        "用一句话解释什么是向量数据库。",
        "用一句话解释什么是大模型幻觉。",
        "用一句话解释什么是 RAG。",
        "用一句话解释什么是知识图谱。",
        "用一句话解释什么是 GraphRAG。",
        "用一句话解释什么是 NATS。",
        "用一句话解释什么是 MCP 协议。",
    ]
    n = min(args.N, len(prompts))
    tasks = []
    for i in range(n):
        tasks.append({
            "type": "custom",
            "session_id": f"bench-sess-{i}",
            "agent_id": args.agent,
            "system_prompt": "你是测试助手，请用一句话简洁回答用户问题，不要调用任何工具。",
            "payload": prompts[i],
            "priority": 2,
        })
    body = {"tasks": tasks}
    t_submit0 = time.perf_counter()
    code, payload, dispatch_ms = http("POST", "/api/tasks/batch-submit", body, token=token)
    t_submit1 = time.perf_counter()
    print(f"[dispatch] batch-submit of {n} tasks -> HTTP {code}, dispatch latency = {dispatch_ms:.0f} ms")
    if code != 202:
        print(f"[dispatch] FATAL, batch rejected: {payload}")
        return
    try:
        results = json.loads(payload)["results"]
    except Exception as e:
        print("parse error", payload)
        return
    ids = [r["id"] for r in results]
    failed_submit = [r.get("error") for r in results if "error" in r]
    if failed_submit:
        print(f"[submit] {len(failed_submit)} tasks failed to enqueue: {failed_submit[:3]}")
    print(f"[submit] accepted {len(ids)}/{n} tasks")

    # 轮询直到所有 task 进入终态
    final_states = {}
    done = 0
    poll_start = time.perf_counter()
    deadline = time.perf_counter() + 600
    while time.perf_counter() < deadline:
        code, payload, _ = http("GET", "/api/tasks", token=token)
        if code == 200:
            tasks_all = {t["id"]: t.get("status") for t in json.loads(payload)}
            final_states = {tid: tasks_all.get(tid, "missing") for tid in ids}
            done = sum(1 for s in final_states.values() if s in ("completed", "failed"))
            if done >= len(ids):
                break
        time.sleep(2)
    drain_s = time.perf_counter() - poll_start
    terminal_target = (time.perf_counter() - t_submit1)

    from collections import Counter
    state_ct = Counter(final_states.values())
    print(f"\n[throughput] submitted {len(ids)} tasks in {submit_drain_secs(t_submit0, t_submit1)}s")
    print(f"             final states: {dict(state_ct)}")
    print(f"             wall-clock from submit to all-terminal = {drain_s:.1f} s")
    print(f"             throughput = {len(ids)/drain_s:.2f} tasks/sec")

    after = fetch_metrics()
    d_in = after["in"] - base["in"]
    d_out = after["out"] - base["out"]
    d_calls = after["calls"] - base["calls"]
    d_cost = after["cost"] - base["cost"]
    print(f"\n[cost] LLM 增量: calls={d_calls:.0f}, in_tokens={d_in:.0f}, out_tokens={d_out:.0f}, cost=${d_cost:.6f}")
    if len(ids) > 0:
        print(f"       per-task: calls={d_calls/len(ids):.2f}, in_tokens={d_in/len(ids):.1f}, "
              f"out_tokens={d_out/len(ids):.1f}, cost=${d_cost/len(ids):.6f}")

    # ---- Phase A: 受理层并发突发，测 dispatcher 延迟与 429 削峰 ----
    print("\n[dispatcher] burst submit x8 (shared per-user fixed-window rate limit)")
    lat = []
    codes = []
    for i in range(8):
        code, _, dt = http("POST", "/api/tasks/submit",
                          {"session_id": f"burst-{i}", "agent_id": args.agent, "payload": "1+1=?", "priority": 2},
                          token=token)
        lat.append(dt)
        codes.append(code)
    import statistics
    ok = [l for c, l in zip(codes, lat) if c == 202]
    ned = [l for c, l in zip(codes, lat) if c == 429]
    print(f"       statuses       : {codes}")
    print(f"       202 accepted   : {len(ok)}, avg {statistics.mean(ok):.1f} ms" if ok else f"       202 accepted   : 0")
    if ned:
        print(f"       429 rejected   : {len(ned)}, avg {statistics.mean(ned):.1f} ms")
    print(f"       p50 dispatch(202): {statistics.median(ok):.1f} ms" if ok else "")


def submit_drain_secs(t0, t1):
    return t1 - t0


if __name__ == "__main__":
    main()
