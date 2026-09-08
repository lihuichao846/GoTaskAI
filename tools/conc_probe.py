"""实测 worker 有效并发度：提交小波任务，采样在途(未终态)数量的时间曲线。"""
import json, re, time, urllib.request, urllib.error

BASE = "http://127.0.0.1:8080"
USERNAME = "bench_user"
PASSWORD = "bench_pass_123"
AGENT = "7af4b09d-9cf2-4d1a-9030-3eae58815deb"


def http(method, path, body=None, token=None, timeout=300):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            code, payload = r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        code, payload = e.code, e.read().decode(errors="replace")
    dt = (time.perf_counter() - t0) * 1000
    return code, payload, dt


def login():
    code, payload, _ = http("POST", "/api/auth/login", {"username": USERNAME, "password": PASSWORD})
    return json.loads(payload)["token"]


def tasks_by_status(token):
    code, payload, _ = http("GET", "/api/tasks", token=token)
    if code != 200:
        return {}, 0
    arr = json.loads(payload)
    counter = {"pending": 0, "processing": 0, "completed": 0, "failed": 0}
    for t in arr:
        s = t.get("status", "")
        if s in counter:
            counter[s] += 1
    return counter, len(arr)


def main():
    token = login()
    N = 12
    tasks = [{"type": "custom", "session_id": f"wave2-{i}", "agent_id": AGENT,
              "system_prompt": "一句话回答，不要调工具。", "payload": f"解释概念 {i}。", "priority": 2}
             for i in range(N)]
    code, payload, _ = http("POST", "/api/tasks/batch-submit", {"tasks": tasks}, token=token)
    print(f"[submit] {N} tasks -> HTTP {code}")
    ids = [r["id"] for r in json.loads(payload)["results"]]
    idset = set(ids)

    start = time.perf_counter()
    samples = []
    while time.perf_counter() - start < 90:
        code, payload, _ = http("GET", "/api/tasks", token=token)
        if code == 200:
            arr = json.loads(payload)
            planned = [t for t in arr if t.get("id") in idset]
            inflight = sum(1 for t in planned if t.get("status") in ("pending", "processing"))
            done = sum(1 for t in planned if t.get("status") in ("completed", "failed"))
            samples.append((round(time.perf_counter() - start, 2), inflight, done, len(planned)))
            if done == N and len(planned) == N:
                break
        time.sleep(1)

    peak = max(s[1] for s in samples) if samples else 0
    last = samples[-1] if samples else None
    print(f"\n峰值并行在途: {peak} 个")
    print(f"末次采样: {last}")
    print(f"排空耗时: {last[0]}s, {last[2]}/{last[3]} 终态")
    print(f"有效并发度(peak) vs 配置10: {peak}/10 = {peak/10*100:.0f}%")
    # 打印时间曲线(每5秒采样)
    print("\n时间曲线 t(s) | in-flight | done/total")
    for idx, s in enumerate(samples):
        if idx % 5 == 0:
            print(f"  {s[0]:6.1f} | {s[1]:3d} | {s[2]}/{s[3]}")


if __name__ == "__main__":
    main()
