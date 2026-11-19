#!/usr/bin/env python3
"""Seed a local Taskloom with demo tasks in every visible state.

Usage: TASKLOOM_ADMIN_PASSWORD=... python3 scripts/seed-demo.py [base-url]
Default base URL: http://localhost:8899.
Administrator login: the password set by `taskloom-admin -password-stdin`
(TASKLOOM_ADMIN_USER defaults to "admin"). It is never printed; no
administrator credential file exists to fall back on.

Creates (idempotent per run only; re-running adds another set of tasks):
- users zhangsan / lisi (password DEMO_PASSWORD below, local demo only)
- Agent demo-agent (owner admin) with a short-lived credential kept in memory
- projects and tasks covering ready, executing (agent / human / stale basis),
  waiting on a question, accepting (pending / rejected), completed, cancelled,
  plus one task for the real worker-1 Agent if it exists.
"""
import json
import os
import sys
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone

DEMO_PASSWORD = "Demo-Passw0rd-2026"
BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8899").rstrip("/") + "/v1"


class Cookie(str):
    """A browser-style session; sent as a Cookie header, not a Bearer token."""


def admin_login():
    password = os.environ.get("TASKLOOM_ADMIN_PASSWORD")
    if not password:
        raise SystemExit("set TASKLOOM_ADMIN_PASSWORD to the administrator password")
    body = json.dumps({"Username": os.environ.get("TASKLOOM_ADMIN_USER", "admin"), "Password": password}).encode()
    req = urllib.request.Request(BASE + "/session", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("X-Taskloom-Request", "1")
    try:
        with urllib.request.urlopen(req) as resp:
            for header in resp.headers.get_all("Set-Cookie") or []:
                if header.startswith("taskloom_session="):
                    return Cookie(header.split(";", 1)[0])
    except urllib.error.HTTPError as e:
        raise SystemExit(f"login failed: {e.code}")
    raise SystemExit("login returned no session")


def call(token, method, path, body=None, ok=(200,)):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if isinstance(token, Cookie):
        req.add_header("Cookie", token)
        req.add_header("X-Taskloom-Request", "1")
    else:
        req.add_header("Authorization", "Bearer " + token)
    req.add_header("Content-Type", "application/json")
    if method != "GET":
        req.add_header("Idempotency-Key", uuid.uuid4().hex)
    try:
        with urllib.request.urlopen(req) as resp:
            text = resp.read().decode()
            return json.loads(text) if text.strip() else None
    except urllib.error.HTTPError as e:
        if e.code in ok:
            return None
        raise SystemExit(f"{method} {path} -> {e.code} {e.read().decode().strip()}")


def main():
    admin = admin_login()
    me = call(admin, "GET", "/me")
    members = {m["Subject"] for m in call(admin, "GET", "/members")}

    for user in ("zhangsan", "lisi"):
        if user not in members:
            call(admin, "POST", "/identities", {"Subject": user, "Kind": "human", "Password": DEMO_PASSWORD})
    if "demo-agent" not in members:
        call(admin, "POST", "/identities", {"Subject": "demo-agent", "Kind": "agent", "OwnerID": me["Subject"]})
    expires = (datetime.now(timezone.utc) + timedelta(hours=2)).isoformat()
    agent = call(admin, "POST", "/credentials", {"Subject": "demo-agent", "ExpiresAt": expires})["Secret"]

    web = call(admin, "POST", "/projects", {"Name": "官网改版", "Description": "新版官网上线前的内容与样式工作", "Owner": me["Subject"]})["ID"]
    data = call(admin, "POST", "/projects", {"Name": "数据平台", "Description": "报表、备份与数据接口", "Owner": me["Subject"]})["ID"]

    def task(title, goal, criteria, project=""):
        return call(admin, "POST", "/tasks", {"Title": title, "Goal": goal, "Criteria": criteria, "ProjectID": project})["Task"]

    def flow(t):
        return call(admin, "GET", f"/tasks/{t['ID']}/workflow")

    def assign(t, subject):
        return call(admin, "POST", f"/tasks/{t['ID']}/execution/assign", {"Subject": subject})

    def claim(t):
        return call(agent, "POST", f"/tasks/{t['ID']}/execution/open", {"BasisVersion": t["BasisVersion"]})

    def submit(t, snap, body):
        return call(agent, "POST", f"/tasks/{t['ID']}/results", {"ExecutionID": snap["Execution"]["ID"], "Body": body})

    def review(t, snap, outcome, reason=""):
        sub = snap["Submission"]
        return call(admin, "POST", f"/tasks/{t['ID']}/review", {"SubmissionID": sub["ID"], "CommitID": sub["CommitID"], "Outcome": outcome, "Reason": reason})

    def comment(t, token, body):
        call(token, "POST", f"/tasks/{t['ID']}/commits", {"Body": body})

    # 1. ready: goal and criteria still missing.
    task("客户反馈汇总（待补充需求）", "", "", web)

    # 2. executing: Agent claimed and is working.
    t = task("整理 Q4 竞品分析", "对比三家主要竞品的定价与功能", "输出一页对比表，含数据来源", web)
    assign(t, "demo-agent")
    claim(t)
    comment(t, agent, "已收集两家竞品的公开定价，正在整理第三家。")

    # 3. executing: assigned to a person, manual work starts at once.
    t = task("撰写 1.2 版本发布说明", "面向客户说明新功能与修复", "中英文各一版，不超过 500 字", web)
    assign(t, "lisi")
    comment(t, admin, "lisi，周五前给我初稿。")

    # 4. accepting: result submitted, waiting for the owner's review.
    t = task("修复登录页样式错位", "移动端登录按钮被遮挡", "iOS 与 Android 主流机型显示正常", web)
    assign(t, "demo-agent")
    snap = claim(t)
    submit(t, snap, "已调整登录容器的安全区内边距，iPhone 15 与 Pixel 8 验证通过，截图见附件说明。")

    # 5. accepting, rejected: the owner sent it back with a reason.
    t = task("导出用户留存报表", "按周统计新用户 7 日留存", "覆盖最近 8 周，口径与看板一致", data)
    assign(t, "demo-agent")
    snap = claim(t)
    snap = submit(t, snap, "已导出最近 4 周留存数据（CSV）。")
    review(t, snap, "rejected", "只有 4 周，需要覆盖最近 8 周。")

    # 6. executing, stale basis: the owner changed the criteria after a result.
    t = task("更新开放 API 文档", "补全订单相关接口说明", "包含请求示例", data)
    assign(t, "demo-agent")
    snap = claim(t)
    submit(t, snap, "订单接口文档已补全请求示例。")
    call(admin, "PATCH", f"/tasks/{t['ID']}", {"Title": t["Title"], "Goal": t["Goal"], "Criteria": "包含请求与响应示例及错误码", "BasisVersion": t["BasisVersion"]})

    # 7. waiting on a question to the owner.
    t = task("制定数据库备份方案", "生产库每日备份与恢复演练", "给出备份频率、保留期和恢复步骤", data)
    assign(t, "demo-agent")
    snap = claim(t)
    call(agent, "POST", f"/tasks/{t['ID']}/questions", {"RoundID": snap["Position"]["RoundID"], "Body": "备份需要保留 30 天还是 90 天？是否要求异地存储？"})

    # 8. completed: result approved.
    t = task("设计周报模板", "统一团队周报格式", "包含进展、风险、下周计划三部分", web)
    assign(t, "demo-agent")
    snap = claim(t)
    snap = submit(t, snap, "模板已完成：进展 / 风险 / 下周计划，附填写示例。")
    review(t, snap, "approved", "格式清晰，可以直接使用。")

    # 9. cancelled.
    t = task("迁移旧版用户脚本", "把 2019 年的导入脚本迁到新平台", "迁移后全量回归通过", data)
    assign(t, "demo-agent")
    claim(t)
    call(admin, "POST", f"/tasks/{t['ID']}/cancel", {})

    # 10. real Agent: worker-1 picks it up on its next wake-up, if enrolled.
    if "worker-1" in members:
        t = task("生成本周例会纪要模板", "为周一例会准备纪要模板", "包含议题、结论、待办与负责人四栏")
        assign(t, "worker-1")

    for d in call(admin, "GET", "/tasks"):
        p = d["Position"]
        state = p["Status"] if p["Status"] != "active" else p["State"]
        print(f"{state:<10} {d['Task']['Title']}  {p.get('Waiting', '')}")


if __name__ == "__main__":
    main()
