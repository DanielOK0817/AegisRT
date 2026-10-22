# AegisRT系统架构

## 定位与边界

AegisRT是从零实现的通用单节点用户态Agent Runtime。它不是工作流框架、容器编排器或Linux内核调度器。上层应用负责说明任务内容与依赖关系，AegisRT负责决定Agent任务何时运行、获得多少资源，以及失败后如何恢复。

当前守护进程的主要执行路径如下：

```text
本地Agent注册表+受限令牌文件+持久化模式
  -> 守护进程启动与回环地址监听
  -> 认证后的任务图/Context API
  -> TaskGraph依赖解锁
  -> DAG-DRCFS调度与多资源准入
  -> ContextVM组装
  -> Worker Manager启动本地进程Agent
  -> 严格解析Agent标准输出
  -> ContextChannel发布产物
  -> Runtime完成、失败或取消任务
  -> 安全事件投影与检查点持久化
```

可选Linux隔离路径如下：

```text
Worker Manager
  -> CgroupIsolation
  -> cgroup v2驱动
  -> cpu.max/memory.max/pids.max
```

守护进程在`/console/`内嵌框架无关的Web Console。控制台是同源静态客户端，不构成第二套控制平面。HTML、CSS和ES Module资源直接编译进Go二进制文件，部署到openEuler时不依赖Node、包管理器、CDN或额外文件服务。登录令牌只保存在页面模块内存中，不写入浏览器持久化存储。

控制台根据通用Agent和Task DTO渲染数据，按`workflow_id`分组，依据`dependencies`计算拓扑结构。界面中的资源数值明确表示调度器预留量，不冒充实时内核利用率；Context复用计数属于逻辑结构指标，不冒充模型厂商的物理KV Cache命中。服务端字符串均通过DOM文本节点插入。服务同时限制回环地址监听、Host为`localhost`或回环IP，并设置严格CSP，以降低DNS Rebinding风险。

## 核心抽象

- `AgentSpec`：定义可复用Agent执行器、能力、策略与默认资源。
- `TaskSpec`：定义带依赖、优先级、输入和幂等键的可调度DAG节点。
- `ResourceVector`：统一描述CPU、内存、LLM并发槽、Token预算和命名工具槽。
- `AgentAttempt`：记录一次具体Worker分配、租约、检查点、重试编号和终态结果。

## 守护进程与控制循环

守护进程监听前会验证受限Bearer Token文件、不可变本地Agent注册表和持久化模式。必须且只能选择`-state-file <path>`或`-ephemeral`之一，配置错误时安全退出。由于当前服务不终止TLS，HTTP只监听回环地址。健康、就绪和不敏感Runtime计数允许匿名读取；任务提交、查询、取消与Context操作必须认证。

`internal/agentconfig`从有界本地JSON文件加载不可变Agent注册表，拒绝未知字段以及通过请求注入远程命令或环境变量。它支持字符串时长、进程入口解析、Agent根目录约束，并在openEuler启动时检查文件权限。

`internal/runservice`负责有限并发tick、唤醒合并、正常取消和服务计量。`internal/controlplane`中的Coordinator持有不可变Agent注册表，执行每个Agent的并发上限，每次tick调度一个任务，解析Context，调用本地Worker Manager，发布验证后的输出，并最终调用`Runtime.Complete`、`Runtime.Fail`或`Runtime.Cancel`。HTTP层没有任意命令、参数、环境变量、手动tick或手工完成接口。

成功进程的标准输出必须先通过协议解析，完整产物封装也必须先发布，Attempt才能成功。Context解析、Worker执行和产物发布共享一个总超时，取Task与Agent上限中的较短者。

排队任务取消会同步完成。活动任务取消先记录并持久化取消意图，再取消Attempt上下文并返回`202`；在Worker退出前，任务仍保留资源预留，避免资源超分。守护进程关闭不等同于用户取消：活动工作会中断并释放资源，在剩余尝试次数允许时重新排队，否则进入失败。当前版本尚未实现失败或取消依赖向后继任务传播终态，后继任务会停留在`WAITING_DEPENDENCY`。

## 执行器与资源控制

`internal/executor`提供有界本地进程执行器。它验证请求，不经隐式Shell启动命令，显式构造环境变量，支持超时和取消，限制标准输出/错误捕获大小，并在所有终止路径调用准备与清理钩子。Linux子进程位于新进程组中，取消或主进程正常退出时会清理后台后代进程。

`internal/worker`目前只支持`runtime=process`的Agent。它校验Agent与Task关系，通过标准输入写入结构化任务请求，选择更短的Agent/Task超时，并可调用隔离钩子。该组件是本地内部执行边界，不是远程Worker API。

`internal/resourcecontrol`已经实现：

- 向cgroup v2文件接口写入CPU、内存和PID限制；
- 通过`cgroup.procs`附加进程；
- 使用Linux clone-into-cgroup在子进程执行前完成原子放置；
- 优先使用`cgroup.kill`清理后代，失败时使用有界`cgroup.procs`回退；
- 精确、非递归地清理Attempt目录；
- 解析CPU、内存cgroup统计和CPU、内存、I/O PSI。

驱动只管理给定根目录下的`aegisrt/<runtime-namespace>/<attempt-id>`子树，并严格验证标识符，防止路径穿越。持久化模式根据规范状态文件路径派生稳定Runtime命名空间，使重启恢复可以精确清理旧Attempt cgroup；临时模式每次生成随机命名空间。

Windows测试验证映射、解析、参数校验、生命周期清理接线与Linux交叉编译，但不能证明真实内核控制器可用性。CPU节流、OOM、PID限制、委派、内核观测清理与PSI采集需在启用cgroup v2的openEuler主机上验证。当前Windows实现没有使用Job Object隔离完整进程树。

## ContextVM与持久化Context

`internal/contextvm`实现逻辑上下文核心：

- 使用SHA-256派生ID的不可变页面；
- system、workflow、agent和ephemeral安全域；
- 工作流和Agent访问控制；
- 带不可变版本的共享/私有清单；
- 带来源关系的写时复制替换；
- Token预算下的固定页与冷数据处理；
- 精确引用去重与复用计量；
- 通过注入接口实现的可选摘要；
- 隔离模型SDK的有界Tokenizer与Summarizer适配器；
- 按Provider、Model、Tenant命名空间分区的逻辑Prompt前缀观测。

前缀观测器不保存Prompt正文，只在同一命名空间内比较有序片段摘要与Token计数，并以有界LRU淘汰命名空间。它始终记录`physical_cache_observed=false`。逻辑命中只表示具备复用候选，不证明模型厂商已接受、计费或物理缓存该前缀；当前也未接入真实LLM网关。

`internal/contextstate`在同一仓库中管理页面Store、Manifest历史与当前Head、有界幂等账本，以及独立受限的任务输出记录。相同正文只存储一次内容块，但ACL页面元数据仍保持独立身份。每次变更都先克隆候选状态、验证并持久化完整检查点，再发布到内存。明确的保存失败保留上一代；无法确定是否落盘的失败会发布候选但使就绪状态失效，避免猜测磁盘代次。12 MiB编码前检查会在超限时拒绝写入，不污染健康状态。

Context检查点位于独立的`<state-file>.context`文件，拥有自己的单写者锁，并同时绑定Context策略指纹和Runtime持久实例ID。即使Agent配置与限制相同，从另一个Runtime复制的Context文件也会被拒绝。恢复时重新校验哈希、Token、ACL、Manifest来源、Head、配额、幂等结果与规范序列状态。

Context与Runtime是两个独立文件，不宣称跨文件原子事务。`ContextControl`先持久化主体Head，再提交Runtime任务补丁；普通补丁拒绝时只删除刚创建且尚未变化的Head，提交结果不确定时保留Context提交。系统依靠写入顺序、幂等、实例绑定和安全失败恢复一致性。

Worker通过结构化标准输入接收选中Manifest引用和组装文本，Context不会放入环境变量或安全事件载荷。若调度完成后、Worker启动前Context仓库失效，`RequeueUnstarted`会释放预留、恢复Attempt计数并把任务重新设为就绪，不消耗重试预算。

当前持久化实现有意保持有界，但尚不是可扩展对象存储：每次写入都会重写完整JSON快照，保留全部不可变Manifest版本，且没有垃圾回收或目录型CAS。文件是受操作系统权限保护的明文，校验和只能检测完整性，不能加密或认证。`WhitespaceCounter`只用于确定性测试与模拟，不是模型Tokenizer。系统不宣称控制真实Provider KV Cache。

## Agent输出与ContextChannel

本地进程边界使用严格标准输出协议：

```text
结构化标准输入
  -> 进程Agent
  -> aegisrt.agent-output/v1
  -> Worker协议验证
  -> Coordinator发布
  -> DAG授权的ContextChannel
  -> Context检查点
  -> Runtime完成任务
```

空标准输出表示无产物；非空标准输出必须是唯一、有界的协议封装，诊断日志只能写入标准错误。格式错误、未知字段、重复键、截断和超限输出都会在Attempt成功前被拒绝。原始Agent输出和产物正文不会复制到错误信息或事件投影。

`ContextControl`从运行中的Task派生生产者，并从TaskGraph直接后继派生接收主体集合。`agent`产物保持私有，`workflow`产物附加到生产者和经过DAG授权的后继Agent主体。所有CAS页面、Manifest版本、幂等记录、投递来源和任务输出记录在一次Context变更中提交。

每个成功任务都会记录输出标记，即使没有产物。后继任务要求每个依赖均存在输出标记，缺失时安全失败。记录可跨重启保存，使稍后加入的直接后继通过稳定投递键获取同一页面。该机制是持久化的DAG授权主体Context传递，不是通用ACK消息总线、任意Agent点对点通信或跨主机传输。

完整协议见[进程型Agent接入协议](agent-protocol.md)。

## 调度边界

应用负责任务语义与动态重规划，AegisRT负责依赖就绪、多资源准入、公平性、资源分配、派发、超时与恢复。系统已经实现DAG解锁、DAG-DRCFS、内存资源账本、本地进程派发、取消、Attempt总超时、持久化Attempt计数和任务边界重试。LLM调用不会被任意抢占，当前也不支持进程内Worker续跑。

恢复语义是“任务边界至少一次”。若进程在关闭或崩溃前已经产生外部副作用，重试可能再次产生相同副作用。`idempotency_key`、`attempt_number`和`attempt_id`帮助Agent或外部工具去重，但Runtime不宣称恰好一次执行。

## 事件、检查点与恢复

`internal/statefile`提供Runtime和Context仓库分别使用的本地文件存储机制。Runtime检查点包含TaskGraph规格与生命周期、图版本、调度容量与预留账本、DAG-DRCFS公平状态、就绪时间、Attempt计数和用户取消意图。检查点使用版本化、有大小限制的JSON封装与SHA-256校验，并绑定调度策略、容量、Agent注册表、Worker上限、cgroup根与隔离限制的配置指纹。

守护进程在生命周期内持有每个状态文件的跨进程独占锁。保存时先写入受限临时文件并同步，再原子替换目标文件；支持的平台还会同步父目录。替换前明确失败会回滚内存变更，替换后目录同步结果不确定会使Runtime停止就绪，依赖持久化的API返回`503 runtime_unavailable`。

Runtime v1迁移必须显式指定`-migrate-legacy-state`。迁移先升级Runtime并写入`legacy_context_migration_pending`标记，再建立空Context主体Head，最后通过第二次Runtime提交清除标记。迁移不会创造历史Context，也不是跨主机迁移协议。

正常关闭时，活动Attempt会中断并在预算允许时重新排队。异常退出后，只有恢复钩子成功销毁精确旧Attempt cgroup，活动检查点才会恢复；清理失败时守护进程安全退出，因此硬崩溃恢复依赖cgroup隔离。系统不支持活动检查点跨主机迁移。

`internal/eventlog`是有界、线程安全的进程内事件历史，提供单调ID、回放游标、明确丢失区间与载荷限制。安全投影只包含标识符、策略、退出状态、超时/取消标记及数字Context指标，不包含Context正文、进程输出、产物、原始错误、调度原因、命令参数或令牌。

认证接口`GET /v1/events`提供有界轮询。每次EventLog创建随机128位`stream_id`，使重启后的新事件流不会错误解释旧游标。EventLog不进入任何检查点，因此它不是持久日志、ACK消息总线、推送流或跨主机时间线。

详细设计决策见：

- [ADR-0003：任务边界文件恢复](adr/0003-task-boundary-file-recovery.md)
- [ADR-0004：持久化Context仓库](adr/0004-durable-context-repository.md)
- [ADR-0005：Agent输出与ContextChannel](adr/0005-agent-output-context-channel.md)
