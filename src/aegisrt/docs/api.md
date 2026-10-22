# HTTP控制API

控制API统一位于`/v1`。守护进程启动时必须且只能选择`-state-file <path>`或`-ephemeral`之一。文件模式持久化Runtime可调度状态，并将Context写入同级`<state-file>.context`文件；临时模式在重启后主动丢弃这些状态。EventLog在两种模式下都只存在于当前进程。

HTTP接口只接收任务图和与任务绑定的Context意图，不允许客户端提交可执行路径、进程参数、环境变量、Context所有权或来源引用。进程Agent的标准输入/输出约定与HTTP接口分离，见[进程型Agent接入协议](agent-protocol.md)。客户端不能通过控制API直接提交Agent产物。

## 持久化模式

`-state-file`适用于需要任务边界恢复的部署。Runtime检查点包含TaskGraph状态、调度分配/预留账本、DAG-DRCFS公平状态、就绪时间、Attempt计数和活动用户取消意图。文件使用严格JSON解码、版本化校验封装、配置指纹、单写者锁与原子替换。损坏、不兼容或已被其他进程锁定的文件会使启动安全失败。

`<state-file>.context`包含不可变页面、去重内容块、Manifest历史与Head、任务输入引用、追加/投递幂等账本，以及受限的Agent输出记录。该文件拥有独立写锁与原子全量快照提交，并绑定Runtime实例ID、Agent ID、Token计数器、摘要策略和Context限制。12 MiB提交预算超限时返回`507`，并保持上一代健康状态。

Context检查点现为format-v2，任务输入归属独立于可淘汰的幂等账本持久化，并保存计数器、摘要器身份及摘要来源索引。format-v1缺少完整的任务输入归属，早期format-v2也可能缺少计量身份；新版均返回需要迁移的错误，不自动猜测或导入主体历史。当前未提供旧Context的自动转换器；`-migrate-legacy-state`只处理下文所述的旧Runtime，不会绕过这项检查。测试新版可使用新的独立状态文件，既有成对状态文件应保留。

`-ephemeral`只用于明确允许重启丢失全部任务的场景。临时模式下Context和幂等账本只存在于内存，EventLog在任何模式下都不持久化。

Runtime v1检查点只有在文件模式显式指定`-migrate-legacy-state`时才允许迁移。迁移会升级Runtime并创建空Context Head，因为v1没有保存Context正文。当前格式Runtime缺少、损坏或绑定不匹配的Context文件时会安全失败，迁移参数不能重建这些数据。迁移前应同时备份两个状态文件。

两个状态文件都可能包含明文应用数据。Unix下写入文件使用仅所有者可访问权限，但AegisRT不提供文件加密，Windows权限语义也较弱。当前存储是单节点快照机制，不是分布式数据库或跨主机迁移方案。

## 认证矩阵

| 接口 | 认证要求 |
|---|---|
| `GET /`、`GET /console` | 匿名重定向到`/console/` |
| `GET /console/`及固定控制台资源 | 匿名静态页面 |
| `GET /healthz` | 匿名，仅回环地址 |
| `GET /readyz` | 匿名，仅回环地址 |
| `GET /v1/runtime` | 匿名，仅回环地址 |
| `GET /v1/agents` | Bearer Token |
| `GET /v1/events` | Bearer Token |
| `GET /v1/tasks` | Bearer Token |
| `POST /v1/task-graphs/patches` | Bearer Token与JSON Content-Type |
| `POST /v1/tasks/{taskID}/cancel` | Bearer Token与JSON Content-Type |
| `GET /v1/tasks/{taskID}/output-summary` | Bearer Token |
| `GET /v1/tasks/{taskID}/context` | Bearer Token |
| `POST /v1/tasks/{taskID}/context/pages` | Bearer Token、JSON Content-Type、`Idempotency-Key`与`If-Match` |

令牌来自启动参数`-auth-token-file`。缺失、格式错误、重复或不正确的Authorization头返回`401`，不会回显令牌。写操作要求`Content-Type: application/json`并拒绝未知JSON字段。

当前凭据是守护进程级管理员凭据，不是终端用户身份、租户边界或单任务能力令牌。Context接口中的`taskID`用于从不可变Task确定主体与输入归属。执行时按任务筛选输入，持有该管理员凭据的客户端仍可操作不同任务，不能把输入筛选当作HTTP多租户认证。

内嵌控制台不会自动获得启动令牌。用户输入值只保存在模块内存中，并通过同源Authorization头发送。真实守护进程还会拒绝Host不是`localhost`或回环IP的请求并返回`421`。

## Runtime状态

`GET /healthz`报告进程健康。`GET /readyz`只有在组件已启动、检查点健康且事件观察器没有错误时才返回就绪。持久化失败会使Runtime进入不安全状态，返回`503`和`{"status":"not_ready"}`。

`GET /v1/runtime`返回版本、启动时间和非敏感任务/资源计数。`stats`在策略、图版本、任务数、就绪数、容量与分配量之外还包含：

| 字段 | 含义 |
|---|---|
| `durable` | 是否使用Runtime检查点 |
| `checkpoint_healthy` | 持久化变更是否仍然安全 |
| `generation` | 内存接受的最新检查点代次 |
| `last_checkpoint_at` | 对应UTC时间 |
| `pending_completion_count` | 已保存结果、正在等待发布或完成确认的任务数 |

启用Context控制后，响应还包含`context_stats`：

| 字段 | 含义 |
|---|---|
| `durable` | Context是否使用独立检查点 |
| `checkpoint_healthy` | Context持久化变更是否安全 |
| `generation`、`last_checkpoint_at` | Context代次与时间 |
| `manifest_count`、`head_count` | Manifest与当前主体Head数量 |
| `ledger_entries` | 持久化幂等记录数 |
| `output_count`、`artifact_count` | 任务输出记录与产物数量 |
| `store` | 页面、内容块、字节和Token的安全聚合计数 |
| `counter_identity`、`summarizer_identity` | 本地预算计数及压缩实现的版本身份 |
| `summary_count` | 当前持久摘要缓存记录数量 |

进程内候选事务保留Store访问计数，重启时从持久内容重建，不能作为跨重启累计实验序列。性能结论应使用原始实验数据。接口不返回任务目标、进程输出、Context正文或产物正文。

响应还包含`event_history`，其`retention`为`process_memory`、`ordering`为`append_id`，同时提供`stream_id`和`started_at`。事件源支持统计时，`window`包含`capacity`、`retained`、`first_id`、`last_id`、`total_appended`和`evicted`；ID及累计数以十进制字符串返回。该窗口与任务、Context统计分别读取，不保证跨模块原子快照。已准入、运行状态和待确认完成应分别解释，运行状态不等于存活进程数。

## 查询Agent目录

```text
GET /v1/agents
Authorization: Bearer <token>
```

响应按ID稳定排序，包含Agent ID、显示名称、Runtime类型、能力、资源请求、并发上限和执行超时。接口不会暴露`entrypoint`、参数、环境变量、本地路径或凭据，也不能修改或热重载注册表。

## 查询有界终态事件

```text
GET /v1/events?after_id=0&limit=100
Authorization: Bearer <token>
```

接口只接受`after_id`、`limit`和`stream_id`。未知、重复、格式错误、带符号、非十进制、溢出或过长参数返回`400`。`after_id`默认值为`0`；`limit`默认值为`100`，允许范围为1至256。

初次请求只有在`after_id=0`时才可省略`stream_id`。后续非零游标必须携带上一次响应中的`stream_id`。事件ID和游标ID均为十进制JSON字符串，避免JavaScript丢失64位整数精度。客户端使用`next_after_id`继续读取；`has_more=true`表示已有下一页。

公开事件类型仅包括：

- `run.succeeded`
- `run.failed`
- `run.canceled`
- `run.timed_out`
- `run.interrupted`

事件载荷可能包含策略、退出码、超时/取消标志、Attempt状态、排队与执行时间，以及数字Context指标。新增`scheduling`包含优先级、继承优先级、关键路径、解锁数量、工作流已结算与在途份额、主导资源占比；`lifecycle`记录实际存在的准备、启动、等待完成时间，阶段为`before_start`、`process_started`或`process_exited`。`output`仅在完成成功且有合法产物信封时返回`artifact_count`和`content_bytes`。接口不会返回任务目标、Context正文、Agent产物、标准输出、标准错误、原始错误、调度原因、命令、参数、环境变量、文件路径或Bearer Token。

`queue_ms`表示任务从`READY`到真实进程启动的时间，包括调度等待、Context解析、请求构造和隔离准备；`run_ms`从真实进程启动到Attempt结束计算。没有创建Attempt或没有真实启动时间的失败不提供`timing`，但保留其他合法事件字段。

事件查询响应明确返回`retention: process_memory`和`ordering: append_id`。事件ID按观察器追加顺序分配，不保证并发任务的墙钟或业务因果顺序。新增阶段字段是终态时汇总的证据，没有生成实时逐阶段事件。重启不补造历史，启动恢复的结果应结合持久任务状态及产物摘要查询。

历史被环形缓冲淘汰时，响应仍为`200`，但`gap=true`并给出`gap_from_id`和`gap_through_id`。每次守护进程启动都会生成新的128位`stream_id`；旧流游标返回`409 stale_event_cursor`，客户端应从新流的`after_id=0`重新开始。

| 状态码 | 含义 |
|---|---|
| `200` | 当前事件流的有界页面 |
| `400` | 严格查询无效或非零游标缺少`stream_id` |
| `401` | 管理员令牌缺失或错误 |
| `405` | 方法不是`GET` |
| `409` | 事件流过期或游标超前 |
| `500` | 已脱敏的读取或安全投影失败 |
| `503` | 未配置有效EventSource |

该接口是易失观测状态上的轮询接口，不是持久日志、ACK消息总线、SSE或跨主机时间线。

## 原子提交任务图补丁

```text
POST /v1/task-graphs/patches
Authorization: Bearer <token>
Content-Type: application/json
```

```json
{
  "tasks": [
    {
      "id": "analyze",
      "workflow_id": "demo",
      "agent_id": "example-agent",
      "goal": "Analyze the supplied work item",
      "priority": 2,
      "resources": {
        "cpu_milli": 300,
        "memory_mib": 128,
        "llm_slots": 1,
        "token_budget": 4000
      },
      "max_attempts": 1,
      "timeout_ms": 60000,
      "idempotency_key": "demo-analyze"
    }
  ]
}
```

补丁要么整体成功，要么整体失败。同一请求内重复ID、已有ID的规格冲突、新旧ID混合、依赖缺失、跨工作流依赖、环、无效Task、未知本地Agent、超过Agent策略的资源请求，以及无法装入Runtime容量的任务都会被拒绝。新任务也不能依赖已取消任务或其后继任务，因为这类依赖无法再全部成功；FAILED状态仍允许恢复，不据此一律拒绝追加。CPU或内存请求为零时继承Agent本地默认值。

当请求中每个ID都已存在，且全部Task规格与已保存规格一致时，任务图将请求视为重放。依赖顺序及空工具资源映射的表示差异不构成规格冲突，任务幂等键仍参与规格比较。重放不改变生命周期、执行次数、就绪时间或图版本，不新写Runtime检查点，也不重新准备Context或用后来增长的固定上下文重新校验已接纳任务；重启后仍适用。该规则按任务规格判定，不另设补丁幂等键，也不保证Agent外部业务副作用恰好执行一次。

每个补丁最多256个Task，整个图最多4096个Task、65536条依赖边，每个Task最多256项依赖。这些值是用于约束验证和调度开销的保守工程上限，并非压测结论。单批超限在Context准备前返回`422 task_limit_exceeded`，其余图规模限制由内核原子校验并返回同一错误码；被拒绝的追加不会留下部分任务。恢复检查点时也检查总节点数、总边数和单任务依赖数；历史状态若超过这些上限会明确拒绝加载，不会截断状态。`timeout_ms`必须为正数且不超过9223372036854，避免转换为内部纳秒时溢出。

成功响应保持`201 Created`和当前任务快照，包括已识别的重放。新任务由RunService接管调度、Context解析、Worker执行与终态迁移，客户端不得自行报告完成。

## 查询任务快照

```text
GET /v1/tasks
Authorization: Bearer <token>
```

响应包含不可变Task规格和当前生命周期状态。任务目标和幂等键可能包含应用敏感数据，因此必须认证。

## 查询任务输出摘要

```text
GET /v1/tasks/analyze/output-summary
Authorization: Bearer <token>
```

```json
{
  "record_count": 1,
  "artifact_count": 1
}
```

成功响应只包含一条持久化输出记录和产物数量，不包含产物名称、正文、哈希、接收方、页面ID、时间、进程身份或本地路径。幂等重放不会新增记录。`404`表示任务没有持久输出，`503`表示摘要Store不可用。

## 查询任务Context元数据

```text
GET /v1/tasks/analyze/context
Authorization: Bearer <token>
```

响应不包含Context正文，只返回绑定Task ID和当前主体Manifest元数据。Manifest ID也会以带引号的强`ETag`返回，追加页面前必须先读取该值。该元数据描述工作流/Agent的存储总表，并非执行时筛选后的任务输入快照。Task到达终态后仍可查询和追加，新增引用仍只绑定指定任务，不会授权该Agent的其他任务读取。

晚到任务读取依赖产物时会构造授权输入视图，不修改主Manifest和该接口的ETag。已持久化的输出记录与任务依赖共同决定这部分可见内容，不能只看主Manifest页数推断任务能否获得依赖结果。

## 追加Context页面

```text
POST /v1/tasks/analyze/context/pages
Authorization: Bearer <token>
Content-Type: application/json
Idempotency-Key: analyze-input-1
If-Match: "<64-lowercase-hex-manifest-id>"
```

```json
{
  "content": "The input that this Agent may consume",
  "visibility": "agent",
  "pin": false,
  "cold": false
}
```

`visibility`只能是`workflow`或`agent`。所有权由不可变Task派生，客户端不能提供Owner ID。`pin`和`cold`可省略但不能同时为真。请求上限为128 KiB，正文上限为64 KiB和8192个所选计数器预算单位；Manifest上限为128页和65536个逻辑Token。固定内容必须满足绑定Task及共享该主体的活动Task的最小Token预算。

追加页面会在同一检查点提交中绑定指定Task的输入引用。执行时只组装该任务显式追加的页面、同工作流直接依赖中允许当前Agent读取的共享产物，以及该任务自己的已发布产物。同Agent其他任务的输入或无依赖产物不会自动进入当前输入。`workflow`允许内容寻址复用，不代表工作流内所有任务自动获得读取引用；相同内容显式追加给另一任务时可以复用物理页面而新增任务绑定。绑定重启后保留，不受幂等账本淘汰影响。

Manifest变化时返回`201`和新ETag。持久化幂等重放或相同标志的页面已存在时返回`200`。响应包含`task_id`、`page_id`、`token_count`、`manifest_id`、`revision`、`manifest_changed`与`idempotent_replay`，不会回显正文。同一`Idempotency-Key`用于不同请求返回`409`；原始键与原始载荷重放时，即使旧`If-Match`已经过期仍可成功。新写入使用旧ETag返回`412`，客户端应重新读取后再提交。

| 状态码 | 含义 |
|---|---|
| `200` | 幂等重放或Manifest未变化 |
| `201` | 页面接受且Manifest前进 |
| `400` | JSON、ETag、Task绑定或幂等键无效 |
| `401` | 管理员令牌缺失或错误 |
| `403` | Context可见范围不允许 |
| `404` | Task不存在 |
| `409` | 幂等冲突或页面标志冲突 |
| `412` | Manifest ETag已过期 |
| `413` | 请求或正文超过字节上限 |
| `415` | 请求不是JSON |
| `422` | Context、Token、页面或固定内容配额失败 |
| `428` | 缺少唯一强`If-Match` |
| `500` | 已脱敏内部Context失败 |
| `503` | Context检查点不可用、无效或需要迁移 |
| `507` | 12 MiB Context检查点预算耗尽 |

默认守护进程使用确定性近似`whitespace-v1`；`-context-counter unicode-segment-v1`可选择区分汉字和符号的本地计数。两者均不等于模型Tokenizer。`-context-compression deterministic-extractive-v1`开启冷页面预算截取及持久摘要缓存，默认`none-v1`关闭。摘要与来源在候选事务中提交，重启后复用；计数器和摘要策略身份变化时拒绝混用旧状态。截取不保证语义完整，也不代表实际模型Token节省。当前没有物理KV Cache控制或后台Context垃圾回收，详见[本地计量与持久化说明](context-management.md)。

## 取消任务

```text
POST /v1/tasks/analyze/cancel
Authorization: Bearer <token>
Content-Type: application/json

{}
```

请求体必须是唯一的空JSON对象。

| 状态码 | 含义 |
|---|---|
| `200` | 排队任务同步取消，或任务已取消 |
| `202` | 已接受活动取消意图，Worker退出后进入`CANCELED` |
| `400` | JSON请求体无效 |
| `401` | Bearer Token缺失或错误 |
| `404` | Task不存在 |
| `409` | 已完成或失败的Task不能取消 |
| `415` | 请求不是JSON |
| `500` | 内部取消失败 |
| `503` | Runtime检查点不可用 |

活动任务在进程停止前保留资源预留，避免取消过程中发生资源超分。文件模式会先持久化用户取消意图，再向Worker发送取消信号，确保重启恢复不会把用户取消误认为守护进程关闭。

## Runtime不可用错误

任务图提交或取消的持久化失败返回：

```json
{
  "error": {
    "code": "runtime_unavailable",
    "message": "durable Runtime state is unavailable"
  }
}
```

内部路径与底层存储错误不会公开。读取接口可能继续返回最后一份内存快照用于诊断，但重启并恢复有效检查点前，调用方不能假设Runtime仍可安全推进。Context相关接口遵循相同脱敏规则，使用`context_unavailable`（`503`）；正常容量拒绝使用`context_capacity`（`507`），不会自动污染Context健康状态。

所有错误使用稳定封装：

```json
{
  "error": {
    "code": "patch_rejected",
    "message": "description"
  }
}
```

## 相对M0 API的破坏性变化

- 已删除`POST /v1/scheduler/ticks`，现在返回`404`。
- 已删除`POST /v1/tasks/{taskID}/completion`，现在返回`404`。
- `GET /v1/tasks`由匿名访问改为Bearer认证。
- 守护进程必须提供`-agents-file`与`-auth-token-file`，并拒绝非回环监听。
- 必须且只能选择`-state-file`或`-ephemeral`。
- 任务图补丁上限为256个Task，累计上限为4096个Task和65536条依赖边，单任务最多256项依赖。
- Runtime v1迁移必须显式指定`-migrate-legacy-state`。
- Context升级为format-v2；旧format-v1文件因缺少完整任务输入归属而要求迁移。`aegis-context-migrate`在离线副本上核验并转换，历史不足时要求显式归属映射，服务不会静默转换或覆盖旧文件。
- 非空进程Agent标准输出由自由文本改为严格`aegisrt.agent-output/v1`，诊断日志必须写入标准错误。
