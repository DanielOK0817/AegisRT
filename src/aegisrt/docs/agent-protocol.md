# 进程型Agent接入协议

本文定义当前本地`process` Agent的接入约定。它是本地执行边界，不是HTTP接口，也不是通用远程Agent SDK。

## 标准输入

AegisRT从Agent注册表启动不可变的本地入口程序，并向标准输入写入一个JSON对象：

```json
{
  "task_id": "analyze-1",
  "workflow_id": "workflow-1",
  "attempt_id": "attempt-1",
  "attempt_number": 1,
  "idempotency_key": "analyze-1-key",
  "goal": "Analyze the supplied input",
  "context_ref": "manifest-sha256",
  "context": "assembled Context text"
}
```

如果没有选择Context，请求中会省略`context_ref`与`context`。外部副作用仍遵循至少一次语义；Agent需要自行去重时，应组合使用`idempotency_key`、`attempt_id`与`attempt_number`。

`context_ref`标识本次任务筛选后的输入视图。输入来自本任务显式绑定的页面、允许读取的直接依赖产物及本任务自己的已发布产物，不自动包含同Agent的全部历史。HTTP元数据查询返回的主体总表ETag可能与这个输入视图标识不同。

主服务可选择`whitespace-v1`或`unicode-segment-v1`本地预算计数，并可通过`-context-compression deterministic-extractive-v1`启用冷页面文本截取及持久缓存。默认不压缩。生成摘要保留来源权限和引用，缓存按源页面及预算区分；两种计数都不是模型实际用量，文本截取也不保证语义完整。

## 标准输出

执行成功时，标准输出只允许以下两种形式：

1. 空内容或仅含空白字符，表示没有可传递产物；
2. 一个且仅一个UTF-8 JSON对象，版本必须为`aegisrt.agent-output/v1`。

显式表示无产物的格式如下：

```json
{"version":"aegisrt.agent-output/v1","artifacts":[]}
```

需要向工作流传递结果时，可以输出：

```json
{
  "version": "aegisrt.agent-output/v1",
  "artifacts": [
    {
      "name": "analysis",
      "content": "Deterministic analysis result",
      "visibility": "workflow"
    }
  ]
}
```

解码器会拒绝未知字段、重复JSON键、多个JSON值、非法UTF-8、缺失或为`null`的`artifacts`、被截断的标准输出，以及混入标准输出的非JSON日志。Agent诊断信息必须写入标准错误。产物正文不会复制到Runtime的安全事件投影中。

## 产物字段

| 字段 | 必填 | 含义 |
|---|---:|---|
| `name` | 是 | 当前输出封装内稳定且唯一的产物名称 |
| `content` | 是 | 作为Context页保存的非空UTF-8文本 |
| `visibility` | 是 | 可取`workflow`或`agent` |
| `recipients` | 否 | 由直接后继任务表示的Agent ID；省略表示所有当前及未来符合条件的主体 |
| `pin` | 否 | 只在生产者主体预算中固定其引用 |
| `cold` | 否 | 将所有已投递引用标记为冷数据 |

`agent`可见性表示产物仅供生产任务使用，不能同时指定接收方。工作流产物始终附加到生产者。显式接收方必须是生产任务在DAG中的直接后继任务所代表的Agent ID，Agent不能指定无关主体。页面按`(workflow_id, agent_id)`存储，执行输入同时按任务筛选；同一Agent的其他任务必须具有相应直接依赖且满足接收方限制，才能从该产物获得输入引用。

Runtime会对Context存储中内容相同的页进行去重，同时在输出记录中保留产物名称、生产者、可见性、接收方、保留策略、页面ID、Token计数和投递目标来源。

## 协议边界

| 边界 | 当前上限 |
|---|---:|
| 捕获的标准输出 | 256 KiB |
| 每个输出封装的产物数 | 32 |
| 产物名称 | 64字节 |
| 产物正文 | 64 KiB，同时受Context页面和Token配置限制 |
| 显式接收方 | 32 |
| Task、工作流、生产者和接收方ID | 128字节 |

名称必须以ASCII字母或数字开头，后续只能使用字母、数字、`.`、`_`或`-`。标识符还允许使用`:`。同一输出封装内的名称必须唯一，接收方ID也不能重复。提供`recipients`时必须是非空数组，`null`和`[]`均非法。`pin`与`cold`不能同时为真。映射到同一Context页的两个产物不能要求互相冲突的保留策略。Runtime会先验证完整输出封装，再原子提交。

## 完成与投递语义

进程成功退出且清理完成后，AegisRT校验完整输出封装及Context发布约束，再持久保存包含任务ID、执行次数与原始产物的完成意图，然后发布产物，最后把任务标记为`SUCCEEDED`并删除完成意图。即使产物列表为空，也会写入一条持久化输出记录。预检拒绝时可正常失败；完成意图已保存后发生发布或最终确认故障时，保留结果及资源预留，暂停新任务接纳，重启时先补发同一份结果。不会通过重新执行Agent来生成已保存的结果。

工作流产物会附加到生产者，以及发布时已知的直接后继Task所代表的合格Agent主体。如果之后新增代表新主体的直接后继Task，即使Runtime已经重启，Context解析过程也会读取持久化输出记录，只附加一次同一CAS页面，并更新已投递目标来源。重放过程使用稳定的产物键和投递键。已经投递的目标可以继续使用旧清单前置条件；新投递目标必须与其当前Context头一致。

依赖解析采取安全失败策略。每个已完成依赖都必须拥有输出记录，包括无产物标记。缺失记录会被视为状态不一致，而不是静默提供空Context。

结果补发必须匹配已保存的任务、执行次数及输出内容；正文、可见性、接收方、保留标记或执行次数变化时会产生幂等冲突。完成意图建立前的崩溃仍可能导致任务恢复重跑，外部业务副作用仍需自行处理重复执行。产物恢复协议不保证外部副作用恰好执行一次。

## 最小Python Agent示例

```python
import json
import sys

request = json.load(sys.stdin)
print(f"processing {request['task_id']}", file=sys.stderr)
json.dump(
    {
        "version": "aegisrt.agent-output/v1",
        "artifacts": [
            {
                "name": "result",
                "content": "completed",
                "visibility": "workflow",
            }
        ],
    },
    sys.stdout,
)
```

Agent程序只需从标准输入读取请求、执行本地任务，并按上述格式将产物写入标准输出。运行日志应写入标准错误，不能与JSON结果混合。

## 当前限制

- 产物以组装后的文本形式交付。结构化名称与生产者来源会持久保存，但尚未渲染到Agent接收的Context正文中。
- 输出记录单独受`MaxLedgerEntries`限制，不会被淘汰，目前也没有TTL或基于可达性的垃圾回收。
- 投递保存CAS页面及目标记录；任务解析还会检查输入归属和依赖权限，不保证后续任务组装出的`context`正文一定包含该页。下游未固定引用仍受该任务Token预算和冷数据丢弃策略限制；`pin`固定生产者主体的存储引用，同Agent直接后继读取该引用时也会保留其标志。
- Context发布仍保存完整的有界检查点；晚到依赖读取在仓库锁内构造授权任务视图，不重复写入接收方主Manifest。首次生成摘要仍需持久化。这一优化不将结果通道变成具有持久确认的通用消息总线。
- Runtime与Context检查点共享Runtime实例绑定，但没有跨文件的统一代次或事务。若回滚导致输出标记丢失，系统会在后继任务解析依赖时发现，而不是在启动时发现。
- Context检查点现为format-v2。旧format-v1缺少完整任务输入归属，早期format-v2可能缺少计数器与摘要器身份；可使用`aegis-context-migrate`离线生成新文件。完整历史可以验证并恢复任务绑定，历史淘汰时必须提供明确归属映射。工具不从旧主体清单猜测任务权限或计量版本，详见[计量与迁移说明](context-management.md)。
- 该机制根据DAG和任务输入引用传递Context，不是带持久确认的任意Agent间消息，也不是跨主机传输。
- 产物正文是不可信的Agent生成文本。图授权不会为正文签名、清洗，也不能保护下游模型免受Prompt Injection影响。
- 当前公共HTTP接口不会返回输出记录或产物正文，只公开经过安全处理的聚合计数。
