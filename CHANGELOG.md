# 变更日志

## 2026-11-17（v1.15.11-panel）

### 新增：国服 `skill_share`（分享一个技能给好友）从「需人工」变为**可一键完成**

**旧结论（原文保留）**：面板把 `skill_share` 归到 manual 桶（无自动动作），README 记的是
「18 个成长任务里 17 个可自动完成，剩余展示操作指引」。

**⚠️ 2026-11-17 实测更正**：`skill_share`（+50c +5e）**可以纯 API 完成**，判据链四步：

1. **accept 报名**：不 accept 时上游 `target=0`，行为事件不计分；accept 后 `target` 变 1
   （面板此前把它当 manual，连报名都没做，目标数一直是 0，看起来像「不可做」）。
2. **取真实市场技能**：`POST /portal/operation-platform/market/skill/list`
   （GET 该路径返回 404 page not found，**必须 POST**）；编造的 `skillId` 不计数。
3. **服务端创建分享链接**：`POST /api/v2/open-platform/asset-shares/c2c/channels`，
   返回 `channel_id` / `share_code`。
   ⚠️ 请求体里的 `asset_ref` 必须是 **JSON 字符串**且键名为 `asset_id`：
   写成对象 `{"assetId":…}` 或键名用 `assetId`，上游一律拒为「请求参数不合法」（code 100001）。
4. **上报判据事件**：`skill_share_channel_action`，且只认 **`action=wechat` +
   `panelAction=success`**（`panelAction=click` 实测不计分），`id` 填第 3 步的 `channel_id`。

**实测结果**：两个国服账号（畈子喜欢吃 / 寻）`skill_share` 均从 `0/1` → `1/1 completed`，
面板自动领奖 **各 +50 分 +5 能**（新二进制实测：`ok=true, credit=50, energy=5, claimed=true`）。

**改动**：

- `internal/upstream/market_skill.go`（新）：`MarketSkillList` / `CreateSkillShareChannel` /
  `DesktopSkillShareChannelAction`，文件头记录上述实测口径与踩坑。
- `internal/upstream/device.go`（新）：`RealDeviceIDs()` 读本机客户端真实 `machineId` / `qimei36`，
  判据事件额外用真实设备指纹再报一份（任务 target=1，重复上报不重复计分）。
- `internal/upstream/market_skill_test.go`（新）：锁死 `asset_ref` 形态与 `wechat/success` 口径。
- `internal/panel/skillshare.go`（新）+ `autotask.go` 注册 `skill_share` 自动动作。
- 扫描待办：国服 `skill_share` 从 manual 桶迁到可自动执行桶（实测扫描 manual_count 9 → 7）。

**剩余人工项**：国际版 5 项 checklist（需在 WorkBuddy 客户端完成，见 v1.15.10-panel 条目）
+ 国服 `wb_wechat_oa_subscribe_task`（需在微信关注公众号并满 24 小时，服务端按真实关注状态判定）。

---

## 2026-11-17（v1.15.10-panel）

### 修复：国际版成长路径「上报了但没反应」——事件通道曾被上游静默丢弃

**旧结论（2026-10-15，原文保留）**：「WorkBuddy 国际版：4 项动作均上报成功（`status=done`），
但任务进度未变化，仍未点亮。国际版 checklist 与国内口径判据不同，网关代做不了。」

**⚠️ 2026-11-17 实测更正（推翻上面结论里的「上报成功」前提）**：

- 旧结论把「HTTP 200」当成了「事件被上游接收」，但**国际版 report 端点对事件有强校验**：
  缺 `reportDelay` 或 `timestamp` 直接 **400 丢弃**：
  `{"code":10001,"msg":"10001:event missing both reportDelay and timestamp fields"}`。
  面板的桌面对话事件链（5.5.6 schema）恰好两个字段都没有 —— 所谓「上报成功」其实是
  **从未被接收**。国服（codebuddy.cn）对此宽容，所以旧版本只在国服实测过，国际版全被丢弃。
- 已修：`upstream.desktopFingerprint` 补齐 `reportDelay`（语义＝上报时刻 − 事件时刻，
  当场立即上报即为 0）与 `timestamp`；新增回归测试锁死这两个字段。
- 事件口径改为**客户端原样**（从国际版客户端 `app.asar` 实测逆向得到）：
  - checklist 的**任务码本身就是客户端 eventCode**：`skill_installed` / `expert_summoned` /
    `template_used` / `wechat_linked`；
  - 必须带业务字段，否则不计分：`skill_installed→{name,skillId,skillVersion}`、
    `expert_summoned→{id,name,expertTitle,type,version}`、
    `template_used→{template_id,task_mode}`（注意是**下划线**）、
    `chat_response_complete→{conversationId,requestId,artifactType}`；
  - **双通道**：客户端同时打 `/v2/report`（标准事件，msg=OK）与 `/llm/data/report`
    （模型事件，msg=success）。面板新增 `ReportDesktopEventAll`，global 账号走双通道，
    国服保持单通道（国服没有该端点，硬打会 404 噪音）。
- **实测现状（诚实结论）**：按上述客户端口径上报后，两条通道**均返回成功**，但
  checklist 的 `status` 仍为 `available`，`/v2/activity/growth/profile` 仍
  `completed=0 / total=5 / first_visit=true` —— 服务端对国际版 checklist 的计分**另有判据**，
  纯 API 点不亮。国际版**也没有** accept / claim / complete 端点（全部 404 或
  `400 invalid request`），即完成判定纯在服务端。
- 故面板**不再对国际版 checklist 承诺「一键完成」**：新增 `client` 分类（与 `auto` 区分），
  扫描结果里标「需在 WorkBuddy 客户端完成」并**逐项给出操作指引**；用户在客户端做完，
  面板回读自动核销。执行结果一律 `unverified`（**绝不谎报 done**），并写明原因
  （「已上报未计分」/「未取到真实业务 ID」）。

### 修复：国际版对话按 realm 分流 + 桌面对话事件头统一

- `upstream.DesktopChatWithExpert` 按 `a.IsGlobal()` 分流：国际版用 `default-model` +
  英文提示词；出站头改走 `ChatHeaders`（统一会话头族），`X-IDE-Version` 取实际客户端版本，
  不再硬编码 `5.5.6`。

### 其他

- 签到池操作列 / 表头对齐修复，弹窗与设置页冗余文案精简（`internal/panel/app.js`、
  `internal/panel/index.html`）。

## 2026-11-17（v1.15.9-panel）

### 新增：Google Gemini 官方 API Key 通道

- 新增 `internal/gemini`：把 **Google 官方 API Key** 接入网关，模型名带 `gemini:` 前缀
  （例如 `gemini:gemini-3-flash-preview`），同一套 `/v1/chat/completions` 端点直接调用。
- 上游 `generativelanguage.googleapis.com/v1beta/openai` **本身就是 OpenAI 兼容**，故只做
  「改写 model + 带 key 转发 + 流式透传」，不做协议翻译。
- 密钥文件 `data/gemini_api_keys.json`（不入库）：按 `groups` 分组，每组一个模型前缀，
  便于把「订阅」与「按量」彻底隔离；组内多 key **轮换**，401/402/429 自动换下一把。
- 分流位置在 `chatCompletions` 的 **realm 判断与选号之前**，按原始 model 字符串判定，
  不会误伤 `cn:` / `global:` / `trae:` / `minimax:` 等既有前缀。

### 出站代理：Gemini 必须走代理（实测更正）

- ⚠️ **2026-11-17 实测更正**：本通道最初实现为「显式直连（`Proxy: nil`）」，理由是
  「本机系统代理常拒连」。**该结论方向反了**，实测数据：
  - 直连 `generativelanguage.googleapis.com` → **25s 超时**；
  - 走 `http://127.0.0.1:10808` → **683ms 返回 HTTP 200**。
  ⇒ Gemini 通道**必须走代理**，"直连"才是会挂掉的那条路。
- 代理地址解析优先级：**配置项 `proxy_url` → 环境变量 `HTTPS_PROXY`/`HTTP_PROXY` →
  Windows 注册表系统代理**（`ProxyEnable=1` + `ProxyServer`）。
- 刻意**不用** Go 的 `http.ProxyFromEnvironment`：它只读环境变量、**不读注册表**，
  而服务由计划任务拉起时环境变量常被清空，结果就是静默直连卡死（旧版 502 的真正成因）。
- ⚠️ 旧实现里「默认客户端必须直连」的单元测试已随之作废，原文保留在
  `internal/gemini/gemini_test.go` 并就地标注更正。

### 实测结论（2026-11-17，真打上游）

- `/v1/models` 出现 **64 个** `gemini:` 条目；对话实测 HTTP 200：
  `gemini-3-flash-preview` / `gemini-3.5-flash` / `gemini-3.1-flash-lite` 均返回真实内容；
- 流式 `stream:true` → `Content-Type: text/event-stream`，SSE 帧与 `[DONE]` 正常；
- 免费层配额边界：Pro 系（`gemini-3.1-pro-preview` 等）返回 **429**（额度为 0，约 5h 重置）；
  `gemini-2.5-flash` / `gemini-2.5-pro` 已被 Google 对新用户下线（**404**）。

### 文档

- README 新增「🤖 Google Gemini 官方 API Key 通道」专节（配置 / 出站代理 / 调用 / 已知边界），
  并同步平台覆盖表、核心能力表、架构图与「关键断言 ↔ 代码出处」。

## 2026-11-15（v1.15.9-panel，同版本续记）

### Qoder 设备身份：自动分配，不用手动逐号配

- 新增 `device_mode=auto`（**默认值**）：号池里每个账号自动带一条出口配置，**用户零操作**；
  浏览器 profile 目录也自动生成（按邮箱隔离的短哈希），不再手填。
- **auto 展开为「真实设备」** —— 2026-11-15 实测更正：auto 原先是「号池首个号 real、其余
  按邮箱派生」，但派生身份会被上游按设备维度过滤掉每日额度（实测：某账号生效身份为派生时
  面板 `pending` 领不到，同一账号改成真实设备后**立刻 +100 Credits**）。
  ⇒ **能领到 > 身份不同**；派生模式降级为手动高级选项，面板上标注了风险。
- 存量迁移：老版本把每个号都写成 `real`；判定「用户从未手动改过」（没配代理 + profile 目录
  就是自动生成的那个）的条目自动迁移到 `auto`，配了代理/改过目录名的保持不动。
- 新增 `POST /panel/api/ide/qoder/egress/reset-auto`（一键恢复默认设备身份，代理保留）
  + 面板「全部恢复自动」按钮 + 账号卡片上的「真实设备 / 派生设备 / 自动」标签。

### Qoder pending 判定：三层证据

1. 客户端身份能看到今天的额度 ⇒ 「客户端能看到、面板查不到 ⇒ 设备身份不一致」；
2. **真实设备身份**能看到、当前身份看不到 ⇒ 「上游按当前这套设备身份过滤掉了本号」；
3. 两套环境码 + 真实身份都看不到 ⇒ 「上游确实没给本号下发」。

第 2 层是新增的：派生/手动配置出问题时会立刻说清原因，而不是让用户去猜。

### 其它

- 领养任务：前置上报失败不再当日封印（一次网络抖动不该把 +300 Credits 推迟一整天）。
- 面板 API 缓存：handler panic 不再让缓存条目永久挂死（后续请求全部卡住、只能重启进程）。
- Qoder 探测不再触发 refresh（避免轮换并烧掉号池里的 refresh_token）。
- **文档脱敏**：README / CHANGELOG / 分发说明 / 代码注释里的真实邮箱一律改为不可逆短码
  （`账号 45da34` / `账号 569875`）。

## 2026-11-11

### Qoder 每日额度

- 修复「已下发被误判未下发」：面板不再把 runtime-info 环境码固定为国际版 3，改为按账号自动尝试 3 / 0；哪个环境能看到当天 `CLAIM_BENEFIT`，就用哪个环境领取。
- 每个账号确认可用的环境码写入 `qoder_accounts.json` 的 `runtime_env`，后续扫描与签到直接复用，不再重复探测。
- `runtime-info.exe` 结果增加 10 分钟缓存，减少每次扫描重复启动进程（单次约 3-4 秒）。
- 签到成功日志改为 `<邮箱> 邮箱实测成功：+N Credits（环境码、活动、领取时刻）`，直接写明是哪一个邮箱，不用含糊指代。

### 积分构成

- Trae / Qoder 积分构成改为与 WorkBuddy 一致的条形卡片：大号剩余额度、构成条、图例。
- Trae 同类积分包合并为一段（如「签到奖励 x29」），避免几十个日包把图例撑爆。
- 签到奖励、最近到期 / 不过期信息以文字展示在卡片底部；Qoder 显示 `+100 Credits/日` 与上游到期时间（9999-12-31 视为不过期）。
- Qoder 不再只显示当前登录号，改为号池逐号查询并展示。

### 核账记录

- 两个 Qoder 账号 2026-11-11 均从上游拿到独立 grant：
  - 账号 45da34：`01a1c862-5fec-750c-9f58-6a3802f5d057`，+100 Credits，10:14:12。
  - 账号 569875：`01a1caf0-f1fe-76b5-9ea0-38b3dacb2cf0`，+100 Credits，22:09:10。
- 附加包额度：账号 45da34 2100 / 已用 0 / 剩余 2100；账号 569875 1500 / 已用 558 / 剩余 942。
