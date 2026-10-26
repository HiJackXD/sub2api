# 账号 User-Agent 过滤

## 目标

部分上游账号只接受 Claude Code、Codex、OpenCode 这类常见 harness。Python、HTTP 库和其他自动化脚本打到这些账号上时，上游容易触发风控。

管理员可以给单个上游账号配置可选的 User-Agent 白名单或黑名单。不匹配的请求不使用该账号，改由同组其他账号承接。组内没有任何账号接受该客户端时，请求以 403 结束，且不会发往上游。

## 范围

规则挂在上游账号（`Account`）上，不挂在分组或用户 API Key 上。所有平台的账号都可以配置。

本功能不替换现有限制：

- 分组 `claude_code_only` 仍在选号前执行。
- 账号 `codex_cli_only` 仍在账号被选中之后做硬拒绝，不因本规则而改成换号。

出站 User-Agent 伪装和账号 `header_overrides` 保持原样。判定只看客户端入站的 `User-Agent` 头。

## 数据

规则存在 `accounts.extra`，不新增列，不新增接口。创建、编辑、批量编辑沿用现有账号 API，由服务端校验。

| 字段 | 含义 |
|---|---|
| `ua_filter_mode` | `off`、`allow`、`block`。缺省与 `off` 都表示关闭。 |
| `ua_filter_patterns` | 字符串数组。只在 `allow` 或 `block` 时保留。 |

关闭时保存会删除这两个键，即使请求里带了名单。其他 `extra` 键不动。

`allow` 与 `block` 互斥，一个账号只有一种模式。

### 保存校验

模式不是 `off`、`allow`、`block` 时返回 400。

`allow` 或 `block` 时，规范化之后必须至少有一条，否则 400。规范化规则：

- 去掉每条的首尾空白，丢掉空串。
- 按不区分大小写去重，保留第一次出现的原始大小写。
- 以 Unicode 码点计，每条长度 3–128。
- 最多 50 条。
- 拒绝含有 ASCII 控制字符（U+0000–U+001F、U+007F）的条目。

管理端 API 使用现有错误信封，说明是 `ua_filter_mode` 或 `ua_filter_patterns` 不合法。

已写入数据库、但名单为空或子串无效的脏数据，在请求判定时按不通过处理。模式字符串无法识别、同时子串列表有效时，按黑名单处理。

## 管理端

创建账号、编辑账号、批量编辑三个界面都提供：

- 模式：关闭、白名单、黑名单。批量编辑另有「不修改」，且为默认值。
- 三个快捷项。勾选只负责把对应字面量加入或移出名单，库里不存预设 ID。
- 自定义条目。
- 最终将保存的子串列表。

快捷项展开为：

| 快捷项 | 写入的子串 |
|---|---|
| Claude Code | `claude-cli/` |
| Codex | `codex_cli_rs/`、`codex-tui/`、`codex_vscode/`、`codex_vscode_copilot/`、`codex_app/`、`codex_chatgpt_desktop/`、`codex_atlas/`、`codex_exec/`、`codex_sdk_ts/`、`codex desktop` |
| OpenCode | `opencode/` |

Codex Desktop 使用 `codex desktop`，不使用带尾随空格的 `codex `，也不使用裸 `codex`。保存时的首尾空白裁剪会把 `codex ` 收成裸 `codex`，从而放行任意含有该词的客户端。`codex desktop` 经裁剪后仍然是 Desktop 家族的子串。

界面在提交前做与服务端相同的非空和长度校验。服务端校验是最终保证。

## 判定

纯函数：给定账号和本次入站 User-Agent，返回该账号能否承接这次请求。

- 模式缺省或 `off`：通过，包括空 User-Agent。
- 模式为 `allow` 或 `block`，且 User-Agent 缺失、空串或去掉首尾空白后为空：不通过。
- 比较前将 User-Agent 与每条子串去掉首尾空白并转为小写。命中关系是子串包含，多条之间为「任一命中」。
- `allow`：至少命中一条才通过。
- `block`：命中任意一条则不通过；一条都未命中才通过。
- 模式不是 `off`、`allow`、`block`，但子串列表有效：按黑名单处理。命中任一子串则不通过，未命中则通过。例如模式误存为 `deny`、子串为 `python-requests` 时，`python-requests/2.32` 不通过，`curl/8.0` 和 `claude-cli/2.1.1` 通过。
- 模式不是已知值且没有有效子串，或模式为 `allow`、`block` 但子串无效：不通过。

没有入站 User-Agent 上下文的内部调用（用量刷新、后台探测）不调用该判定。用户请求的网关在选号前写入上下文：头不存在时写入空串，并标记「这是用户请求」。只有带该标记时，选号才套用规则。

## 调度

判定与模型、平台等资格条件放在同一次选号里。不通过的账号只对这一次请求排除：不修改账号状态，不计失败，不占用并发槽，也不向该账号发出上游请求。

用户请求会选中账号的路径都要接入，包括：

- `GatewayService` 里每一处用户请求选号循环，含粘性命中。
- OpenAI 调度器的候选过滤、会话粘性，以及 `previous_response_id` 粘连。
- `GeminiMessagesCompatService` 的选号与粘性命中。Antigravity 走该服务选号时同样适用。

粘性会话指向的账号若被规则挡住：这次不使用它，删除该会话的粘性绑定，再在其余账号中继续选。这次请求不把接替账号写成新的粘性绑定。同一会话的下一次请求重新选号。

`previous_response_id` 指向的账号若被规则挡住：这次不使用它，不删除已存储的响应映射，再继续选号。请求因此可能落到别的账号，后续按该账号没有这条上游响应处理。同一响应 ID 的下一次请求仍会先看到原映射，再因规则重新跳过。

候选集指这次选号在套用本规则和临时不可调度条件之前，已经按分组、平台、模型纳入的账号。`excludedIDs` 中的账号不在候选集里。

选号结果：

1. 有候选账号通过规则，并且满足原有调度条件：选中该账号。
2. 至少有一个候选账号通过规则，但这些账号都因限流、并发或其他既有原因不可用：返回现有的无可用账号错误。
3. 候选集里没有任何账号通过规则：返回 `ErrUserAgentNotAllowed`。
4. 在考虑本规则之前，候选集已经为空（例如没有支持该模型的账号）：返回现有的对应错误，而不是 `ErrUserAgentNotAllowed`。

## 错误与日志

`ErrUserAgentNotAllowed` 是可用 `errors.Is` 识别的哨兵错误。每个把选号失败写成 HTTP 响应的用户入口都先识别它，再落到通用的无可用账号处理。上游连接建立之前就返回。

HTTP 状态为 403。错误类型为 `forbidden_error`。文案固定为：

```text
No available account accepts this User-Agent
```

OpenAI 风格入口使用现有信封：

```json
{"error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}
```

Anthropic 风格入口使用现有信封：

```json
{"type":"error","error":{"type":"forbidden_error","message":"No available account accepts this User-Agent"}}
```

响应体不包含模式、子串或账号 ID。WebSocket 在升级或向上游连接之前以同样的 403 结束。

每跳过一个账号记一条 Info 日志，包含账号 ID、模式，以及 User-Agent。User-Agent 在码点边界截断到不超过 256 字节，空值记为空，换行替换为空格。该内容不进入客户端响应。

## 测试

纯函数单测：

- `off` 或缺省时，任意 User-Agent（含空串和空白）都通过。
- `allow` 在大小写不同、前后有空白时仍能命中；未命中、空串、空白都不通过。
- `block` 在命中时不通过；空串和空白不通过；未命中时通过。
- 模式无法识别且没有有效子串时不通过。模式无法识别但子串有效时，按黑名单处理。
- `codex desktop` 命中 `Codex Desktop/1.0`，裸 `codex` 不是预设条目。

保存校验单测：

- `allow` 或 `block` 在名单规范化后为空时返回 400。
- 短于 3、长于 128、超过 50 条、含控制字符时返回 400。
- 不区分大小写的重复项合并为一条，并保留首次出现的大小写。
- `off` 删除 `ua_filter_mode` 与 `ua_filter_patterns`。

调度单测分别覆盖 `GatewayService`、OpenAI 调度器、Gemini 选号：

- 受限账号被跳过，同组未设限账号承接，且没有向被跳过的账号发出上游请求。
- 粘性会话被规则挡住时，绑定被删除并改选其他合格账号。
- `previous_response_id` 被规则挡住时，本次改选其他账号，已存映射仍在。
- 候选账号全部不通过时返回 `ErrUserAgentNotAllowed`。
- 有账号通过规则但都不可调度时，仍返回原来的无可用账号错误。
- 候选集因模型或平台为空时，不返回 `ErrUserAgentNotAllowed`。
- 没有用户请求标记的内部调用不做这项过滤。

网关单测确认该哨兵被映射为 403，两种信封的类型都是 `forbidden_error`，文案为上面那一句，且响应体没有模式、子串或账号 ID。

## 非目标

- 不在分组或 API Key 上配置本规则。
- 不支持正则、前缀模式或完整相等。
- 不把预设 ID 存进数据库，也不在预设表更新时回溯修改已保存的子串。
- 不改变 `codex_cli_only` 和 `claude_code_only` 的现有拒绝方式。
- 不根据出站伪装后的 User-Agent 做判定。
