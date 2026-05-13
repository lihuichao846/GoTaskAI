import urllib.request
import json

url = "http://127.0.0.1:11434/api/generate"
data = {
    "model": "qwen2.5:latest",
    "prompt": "你好，我是 GoTaskAI 项目的开发者。请用一句话向我打个招呼！",
    "stream": False
}

req = urllib.request.Request(url, data=json.dumps(data).encode('utf-8'), headers={'Content-Type': 'application/json'})

try:
    with urllib.request.urlopen(req) as response:
        result = json.loads(response.read().decode('utf-8'))
        print("\n===============================")
        print("🤖 本地大模型回复: ")
        print(result.get("response", ""))
        print("===============================\n")
except Exception as e:
    print("访问失败:", e)
