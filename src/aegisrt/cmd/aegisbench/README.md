# aegisbench实验证据采集说明

`scheduler`命令可在Windows上执行确定性冒烟实验和固定配置的正式实验。`context`与`fault`用于采集功能性冒烟证据。这些原有命令生成匿名运行编号，并使用校验和封存结果。`fault-kernel`与`resource-kernel`只允许由openEuler上的委派服务调用，在其他环境中会安全拒绝执行。

## 本地通信测量

`communication`测量持久化Context产物发布、多接收者读取和首次依赖补发。使用构建脚本绑定源码哈希及二进制身份，并保存逐次原始JSON、环境、分配量、检查点增长和正确性结果：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File experiments/communication/run.ps1
```

默认输出到`experiments/communication/evidence`。固定矩阵包含256B、4KiB、32KiB，1或4个接收方，1或4个并发调用；每组预热1轮、测量3轮，每轮4次操作。已加载读取的单次样本包含32次重复读取，原始延迟需除以`read_passes`才能得到一次完整接收方读取的平均耗时。若直接运行未绑定源码身份的二进制，该命令会拒绝采集。

这项命令保存可复核的本地探索测量，尚未接入原有正式实验的封存清单。结果不包含HTTP传输、进程启动、运行时完成协议或模型调用，不构成历史基线对照和性能提升证明。详见`docs/m9-communication-20260922.md`。

## 调度冒烟实验

```powershell
go run ./cmd/aegisbench scheduler `
  --profile smoke `
  --output experiments/raw `
  --seeds 104729,104743 `
  --repetitions 1
```

该命令执行基于确定性逻辑时钟的`S1-smoke`调度工作负载。

## S1/S2/S3调度正式实验

```powershell
go run ./cmd/aegisbench scheduler `
  --profile formal `
  --output experiments/raw
```

正式配置固定使用3个预热种子、30个测量种子、1次重复以及平衡后的配对顺序，所有工作负载定义也保持不变，不接受种子数或重复次数覆盖。封存结果包含240行测量记录、任务级重算trace、规范输入、预热事件和`scheduler-analysis.json`。分析文件给出配对bootstrap置信区间与秩二列相关效应量。所有时间指标均来自确定性逻辑时钟，不能解释为主机或内核实际运行时间。

## ContextVM冒烟实验

```powershell
go run ./cmd/aegisbench context `
  --output experiments/raw `
  --seeds 104729,104743 `
  --repetitions 1
```

该命令执行两组配对功能实验：

- `C1-smoke`对比每个任务单独复制的Store与共享ContextVM Store，用于验证内容块存储计量和ACL保持。该实验不会减少组装后的派发输入，也不对LLM Token、模型厂商Prompt Cache、堆内存或RSS节省作出结论。
- `C2-smoke`对比完整逻辑组装与ContextVM限额组装。处理组保留固定页，并确定性丢弃无法纳入预算的非固定页。实验关闭摘要功能，因此不属于语义压缩，`summary_pages`始终为零。

Context证据包含`context.csv`、`context-trial-traces.jsonl`、`workload.json`、`context-input.json`、`events.jsonl`、`environment.json`、`manifest.json`和`SHA256SUMS`。计数器使用确定性且与模型厂商无关的`whitespace-v1`，所得数值不是任何模型Tokenizer的测量结果。

## 故障隔离冒烟实验

```powershell
go run ./cmd/aegisbench fault `
  --output experiments/raw `
  --seeds 104729 `
  --repetitions 1
```

Windows功能配置覆盖确定性Agent退出、截止时间隔离，以及进程内服务和状态文件的正常重新打开。该实验不是外部守护进程强制终止实验，也不是cgroup清理实验。

仅openEuler可用的命令约定如下：

```bash
aegisbench fault-kernel --profile smoke --output /var/lib/aegisrt-bench/raw \
  --seeds 104729 --repetitions 1
aegisbench fault-kernel --profile formal --output /var/lib/aegisrt-bench/raw
```

`smoke`最多允许4个测量种子/重复组合且不执行预热。`formal`固定使用3组预热配对、`experiments/PLAN.md`定义的30个测量种子和1次重复。命令不接受任意PID、systemd单元、cgroup路径或守护进程可执行文件。Linux监督器与采集器已经实现并完成交叉验证；一组限时openEuler冒烟结果已由`evidence/openeuler`索引，正式配置尚未执行。

## 资源控制器冒烟实验

仅openEuler可用的固定命令如下：

```bash
aegisbench resource-kernel --profile smoke --output /var/lib/aegisrt-bench
```

命令针对固定工作量CPU配额饱和、禁用swap的内存上限和PID上限分别生成基线/处理配对。CPU结果按整个试次的平均millicpu报告，内存和PID结果按峰值报告。处理组必须观测到对应的内核节流、OOM或PID计数器变化。命令不接受任意cgroup路径或工作负载覆盖。单种子结果只作为机制有效性门禁，不能与正式R2/R3协议合并分析。

## 结果发布约定

输出参数必须指向已存在的父目录。采集器生成不透明的随机`run_id`，只向新建的暂存目录写入数据，完成后发布为`<output>/<run_id>`。如果暂存路径或最终路径已经存在，命令会拒绝覆盖。CLI只报告匿名`run_id`，不会打印父目录或最终发布路径。采集失败时，命令行只输出稳定状态，避免文件系统错误泄露本地路径；库调用方仍可接收底层错误以便本地诊断。库类型`RunSummary`保留真实目录供进程内调用。为限制内存占用和取消延迟，一次冒烟运行最多接受128个种子/重复组合，并在每个确定性试次之间检查中断状态。

调度实验包含`scheduler.csv`、`scheduler-task-traces.jsonl`、`workload.json`、`scheduler-input.json`、`events.jsonl`、`environment.json`、`manifest.json`和`SHA256SUMS`。规范工作负载与调度输入的原始字节会被保留，以便独立重算哈希。任务trace对每个试次记录4类严格白名单数据，也是重算聚合行的依据。

SHA-256可用于发现相对于已保存校验基线的普通文件改动，但结果目录不是WORM存储，目录所有者仍可修改或删除文件。如需更强的证据保管，应单独保存校验和或只读副本。

## 调度证据边界

- 任务持续时间与排队指标来自确定性离散事件逻辑时钟，只用于验证调度策略行为，不是Windows主机的墙钟性能数据。
- `critical_path_ms`表示预先声明的`beta-critical`片段从`READY`到终态的时间，并非整个试次的`makespan_ms`。
- 菱形工作负载有意只使用一个调度Worker槽，用于检验资源竞争下的执行顺序，不测量主机CPU饱和度。
- Worker槽已预留时出现的临时`ErrNoCandidate`属于排队，不是永久拒绝，因此S1中的`admission_rejections`保持为零。
- 清单和所有CSV行的平台均固定为`windows-functional`。该采集器不能生成`openeuler-kernel`证据，也不对cgroup、PSI或内核强制执行作出结论。
- `events.jsonl`使用进程单调时钟记录采集边界；UTC时间戳只用于追踪来源。

## 实验协议状态

调度冒烟命令输出`plan_id=s1-single-slot-critical-segment-smoke-v2`与`case_id=S1-smoke`。它会明确记录`warmup_pairs=0`，并允许较少的测量配对用于开发反馈，因此不能与`experiments/PLAN.md`规定的正式结果合并，也不能表述为正式实验已经完成。正式协议要求每个实验单元包含3组预热配对和至少30组测量配对。合成的`NOT_EXECUTED`示例仅供解析器测试，采集命令不会读取这些文件。

Context命令输出`plan_id=context-dedup-budget-drop-smoke-v1`与`case_ids=[C1-smoke,C2-smoke]`，并记录`warmup_pairs=0`。这些数据属于功能计量与不变量证据，不是正式性能实验结果。
