"""本地联调用的模拟服务，同时扮演飞书开放平台与 OpenAI 格式的模型接口

用法:
    python3 scripts/fake_feishu.py [端口] [请求日志文件]

再让 tiro 指向它:
    TIRO_FEISHU_OPEN_BASE=http://127.0.0.1:8477 TIRO_FEISHU_ACCOUNTS_BASE=http://127.0.0.1:8477 \
        TIRO_DATA_DIR=/tmp/tiro-dev ./bin/tiro

扫码步骤用 GET /control?approve_app=1 与 /control?approve_auth=1 模拟确认，
fail_llm、fail_doc、fail_refresh 置 1 可以模拟对应环节出错，hang_doc 置 1 让建文档的异步任务一直停在处理中，
deny_oauth 置 1 模拟用户在网页授权页拒绝
网页授权页直接带着授权码跳回 redirect_uri；浏览器 UA 里带上 Lark/ 即可模拟在飞书客户端内打开后台
后台里的模型接口地址填 http://127.0.0.1:8477/v1
"""
import json
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8477
LOG = sys.argv[2] if len(sys.argv) > 2 else "fake.log"
START = time.time()

state = {
    "approve_app": False,
    "approve_auth": False,
    "auth_scope": "",
    "refresh_count": 0,
    "doc_count": 0,
    "fail_llm": False,
    "fail_doc": False,
    "fail_refresh": False,
    "hang_doc": False,
    "deny_oauth": False,
}

MINUTES = {
    "obcnfake0000000000000001": {
        "title": "soundcore Work_11-06 16:38",
        "created": START + 3600,
        "duration": 2696,
        "transcript": "2026-11-06 16:38:39 CST|44min 56s\n\nKeywords:\n接口、联调\n\n" + "\n\n".join(
            f"Speaker {i % 3 + 1} 00:{i:02d}:05.200\n这是第 {i} 段逐字稿，讨论接口改版的进度与联调安排，23 号前完成测试。" for i in range(30)
        ),
        "summary": "会议讨论了接口改版与联调安排。",
    },
    "obcnfake0000000000000002": {
        "title": "授权前的旧会议",
        "created": START - 7200,
        "duration": 600,
        "transcript": "旧内容" * 200,
        "summary": "",
    },
    "obcnfake0000000000000003": {
        "title": "误触录音",
        "created": START + 3600,
        "duration": 12,
        "transcript": "Speaker 1 00:00:01.000\n喂，听得到吗",
        "summary": "",
    },
}

REPLY = """# 接口改版与联调安排

参与人：张三、说话人 2、说话人 3

## 结论

- 新版接口文档已提交评审，等待反馈
- 测试环境就绪前用模拟服务完成联调

## 决策

| 决策 | 依据 | 出处 |
| --- | --- | --- |
| 先用轮询同步状态 | 推送通道尚未上线，无法实测 | 09:44 |

## 待办

- [ ] 跟进接口评审结果（张三，11 月 10 日）
- [ ] 完成小程序端测试（负责人待确认，11 月 23 日）

## 进展

**接口**

- 鉴权方案已确定

## 风险与卡点

- 测试环境未就绪，联调只能依赖模拟服务

## 待确认

- 「DVD 文件」疑为 DBD 文件，需与会者确认
"""


def log(kind, payload):
    with open(LOG, "a", encoding="utf-8") as handle:
        handle.write(json.dumps({"at": round(time.time() - START, 1), "kind": kind, "payload": payload}, ensure_ascii=False) + "\n")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, payload, status=200):
        raw = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def body(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length).decode() if length else ""
        if "json" in (self.headers.get("Content-Type") or ""):
            return json.loads(raw or "{}")
        return {key: values[0] for key, values in urllib.parse.parse_qs(raw).items()}

    def do_GET(self):
        url = urllib.parse.urlparse(self.path)
        path = url.path
        query = {key: values[0] for key, values in urllib.parse.parse_qs(url.query).items()}

        if path == "/control":
            for key, value in query.items():
                if key in state:
                    state[key] = value == "1"
            return self.reply(state)

        if path == "/open-apis/authen/v1/authorize":
            log("authorize", query)
            result = {"error": "access_denied"} if state["deny_oauth"] else {"code": "c-fake-oauth"}
            result["state"] = query.get("state", "")
            self.send_response(302)
            self.send_header("Location", query["redirect_uri"] + "?" + urllib.parse.urlencode(result))
            self.end_headers()
            return

        if path == "/open-apis/authen/v1/user_info":
            return self.reply({"code": 0, "data": {"open_id": "ou_fake_user", "name": "测试用户", "avatar_url": ""}})

        if path.startswith("/open-apis/minutes/v1/minutes/"):
            parts = path.split("/")
            token = parts[5]
            minute = MINUTES.get(token)
            if not minute:
                return self.reply({"code": 2091004, "msg": "minute not found"}, 404)
            if len(parts) > 6 and parts[6] == "artifacts":
                return self.reply({"code": 0, "data": {"summary": minute["summary"], "transcript": minute["transcript"], "keywords": ["联调"]}})
            return self.reply({"code": 0, "data": {"minute": {
                "token": token, "title": minute["title"], "url": f"https://example.feishu.cn/minutes/{token}",
                "owner_id": "ou_fake_user", "create_time": str(int(minute["created"] * 1000)),
                "duration": str(minute["duration"] * 1000), "status": 0,
            }}})

        if path.startswith("/open-apis/docs_ai/v1/async_tasks/"):
            if state["hang_doc"]:
                return self.reply({"code": 0, "data": {"task": {"task_id": path.rsplit("/", 1)[1], "status": "processing", "poll_after_ms": 300}}})
            document = {"document": {"document_id": f"doc_fake_{state['doc_count']}", "url": f"https://example.feishu.cn/docx/doc_fake_{state['doc_count']}"}}
            return self.reply({"code": 0, "data": {"task": {"task_id": path.rsplit("/", 1)[1], "status": "success", "result": {"create_document": json.dumps(document)}}}})

        self.reply({"code": 404, "msg": "unknown " + path}, 404)

    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        body = self.body()

        if path == "/oauth/v1/app/registration":
            if body.get("action") == "begin":
                return self.reply({"device_code": "dc_app", "user_code": "FAKE-APP1", "verification_uri": "http://x", "expire_in": 600, "interval": 1})
            if not state["approve_app"]:
                return self.reply({"error": "authorization_pending"}, 400)
            return self.reply({"client_id": "cli_fake0000000001", "client_secret": "fake-secret", "user_info": {"open_id": "ou_fake_user", "tenant_brand": "feishu"}})

        if path == "/oauth/v1/device_authorization":
            state["auth_scope"] = body.get("scope", "")
            log("device_authorization", {"scope": body.get("scope"), "auth": self.headers.get("Authorization", "")[:12]})
            return self.reply({"device_code": "dc_auth", "user_code": "FAKE-AUTH", "verification_uri": "http://x",
                               "verification_uri_complete": f"http://127.0.0.1:{PORT}/verify?user_code=FAKE-AUTH", "expires_in": 240, "interval": 1})

        if path == "/oauth/v3/token":
            if body.get("grant_type") == "refresh_token":
                if state["fail_refresh"]:
                    return self.reply({"error": "invalid_grant", "error_description": "refresh token expired"}, 400)
                state["refresh_count"] += 1
                log("refresh", {"refresh_token": body.get("refresh_token")})
                return self.reply({"access_token": f"u-fake-r{state['refresh_count']}", "refresh_token": f"r-fake-{state['refresh_count']}",
                                   "expires_in": 7200, "refresh_token_expires_in": 604800, "token_type": "Bearer"})
            if body.get("grant_type") == "authorization_code":
                log("authorization_code", {"code": body.get("code"), "redirect_uri": body.get("redirect_uri")})
                if body.get("code") != "c-fake-oauth":
                    return self.reply({"code": 20003, "error": "invalid_grant", "error_description": "code is invalid"}, 400)
                return self.reply({"code": 0, "access_token": "u-fake-oauth", "refresh_token": "r-fake-oauth", "expires_in": 7200,
                                   "refresh_token_expires_in": 604800, "token_type": "Bearer"})
            if not state["approve_auth"]:
                return self.reply({"error": "authorization_pending"}, 400)
            return self.reply({"access_token": "u-fake-0", "refresh_token": "r-fake-0", "expires_in": 7200,
                               "refresh_token_expires_in": 604800, "token_type": "Bearer", "scope": state["auth_scope"]})

        if path == "/open-apis/auth/v3/tenant_access_token/internal":
            if body.get("app_secret") != "fake-secret":
                return self.reply({"code": 10014, "msg": "app secret invalid"})
            return self.reply({"code": 0, "msg": "ok", "tenant_access_token": "t-fake", "expire": 7200})

        if path == "/open-apis/minutes/v1/minutes/search":
            log("search", body)
            return self.reply({"code": 0, "data": {"items": [{"token": token} for token in MINUTES], "has_more": False}})

        if path == "/open-apis/drive/v1/files/create_folder":
            log("create_folder", body)
            return self.reply({"code": 0, "data": {"token": "fld_fake", "url": "https://example.feishu.cn/drive/folder/fld_fake"}})

        if path == "/open-apis/docs_ai/v1/documents":
            if state["fail_doc"]:
                return self.reply({"code": 99991672, "msg": "Access denied. scope docx:document:create required"}, 400)
            state["doc_count"] += 1
            log("create_document", body)
            return self.reply({"code": 0, "data": {"task": {"task_id": f"task_{state['doc_count']}", "status": "processing", "poll_after_ms": 300}}})

        if path == "/open-apis/im/v1/messages":
            log("message", {"receive_id": body.get("receive_id"), "card": json.loads(body.get("content", "{}"))})
            return self.reply({"code": 0, "data": {"message_id": "om_fake"}})

        if path == "/v1/chat/completions":
            if state["fail_llm"]:
                return self.reply({"error": {"message": "invalid api key"}}, 401)
            user = body["messages"][-1]["content"]
            log("chat", {"model": body.get("model"), "auth": self.headers.get("Authorization", ""), "system_len": len(body["messages"][0]["content"]), "user_head": user[:160]})
            content = "连接正常" if "连接测试" in user else REPLY
            return self.reply({"choices": [{"message": {"role": "assistant", "content": content}}]})

        self.reply({"code": 404, "msg": "unknown " + path}, 404)


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
