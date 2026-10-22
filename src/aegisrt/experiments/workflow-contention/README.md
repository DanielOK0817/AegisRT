# 多工作流竞争实验

本实验用两个有限工作流比较FIFO、当前默认dag-drcfs及显式选择的dag-drcfs-time。每个工作流包含24个独立任务。长任务等待1200ms，短任务等待250ms，每个任务声明250mCPU和64MiB，平台总容量为1000mCPU和256MiB，运行4个并行Coordinator循环。任务运行同一个Go二进制的真实子进程，不调用模型。

同时到达场景在一次ApplyPatch中提交两个工作流，任务ID按长、短交错排序，避免FIFO的同时间ID决胜规则先排完一个类别。晚到短任务场景先提交长任务，再于试验开始后的100ms提交短任务。每种场景运行3个重复，每个重复完整运行3种策略，策略顺序循环轮换，第二场景反向轮换。

正式测量前，每个策略执行一个保留证据的独立短预热。每次测量都启动新进程并创建新的持久化状态；预热只统一二进制和文件缓存条件，不清空操作系统缓存。默认矩阵包含3次预热和18次测量，单次测量约9秒，整体约3分钟。其他实验必须在矩阵完成后再运行，避免资源干扰。

## 运行与核验

从aegisrt仓库根目录执行，输出目录必须不存在：

```powershell
python experiments/workflow-contention/run_matrix.py --output evidence/workflow-contention-<new-id>
python experiments/workflow-contention/verify.py evidence/workflow-contention-<new-id>
```

原生openEuler支持同样的命令。也可在Windows交叉编译Linux二进制，复制源代码树及脚本到openEuler后复用二进制：

```powershell
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -o <new-build-directory>/contention-linux-amd64 ./experiments/workflow-contention
```

```bash
python3 experiments/workflow-contention/run_matrix.py --binary /absolute/path/contention-linux-amd64 --output evidence/workflow-contention-<new-id>
python3 experiments/workflow-contention/verify.py evidence/workflow-contention-<new-id>
```

交叉编译的环境变量只应在专用构建进程中设置，或在构建后恢复。复用外部二进制时，入口保留该二进制的SHA256，并将源代码快照标记为执行时采集；构建对应关系由外部构建记录佐证。本机构建模式检查构建前后的源文件SHA256一致。

单次短预检示例：

```powershell
go build -o <new-build-directory>/contention.exe ./experiments/workflow-contention
<new-build-directory>/contention.exe --policy fifo --output evidence/contention-smoke-<new-id> --tasks-per-workflow 4 --long-duration 100ms --short-duration 40ms --short-arrival 30ms
python experiments/workflow-contention/verify.py evidence/contention-smoke-<new-id>
```

## 原始证据与指标

`config.json`保留完整TaskSpec、容量、Worker数量、到达间隔、工作时长和二进制SHA256。`events.jsonl`由CheckpointStore成功持久化后的观察器输出READY、ADMITTED、RUNNING、SUCCEEDED转换，保留核心提交时点、实际观察时点、单调时钟进度、持久化代数及容量占用。观察器不改变核心调度代码。核心提交时点可能来自锁取得之前的调用时点，排队指标使用成功提交后的观察时点。

`results.jsonl`保留生产Coordinator的每次TickResult，包括原始Decision及Executor的进程准备、启动确认、Wait完成和退出状态。Executor在`command.Start`返回后才记录启动确认时点，Linux子进程可能已经开始执行。每个任务产生确定性文本产物和含PID、子进程工作开始结束时点的元数据。核验器要求子进程元数据落在准备开始至Wait完成区间内，另行检查准备、启动确认及Wait的顺序，重算产物SHA256并比较输入、结果和最终持久化状态。`runtime.state`使用生产statefile的原子替换、校验和与独占写入路径。

P95使用最近秩法`ceil(0.95*n)`。主要等待指标`short_ready_to_start_p95_seconds`从READY提交观察算至Executor启动确认；另行报告至ADMITTED提交观察以及子进程读取输入后开始工作的等待时间。子进程工作开始可能早于父进程启动确认，两个指标分别报告。总完工时间从首次READY提交观察算至最后一个子进程Wait完成，吞吐量为完成任务总数除以该时间。两个工作流的完成时间分别从自身首次READY算起。

公平性观察窗口从两个工作流都已有READY任务的时点开始，到任一工作流最后一个子进程Wait完成的时点为止。窗口内两个工作流都存在已提交且未完成的需求，包含等待和运行状态。每个工作流的确认进程占用时间积分为各任务从Executor启动确认至Wait完成的区间与观察窗口交集的时长总和；各任务资源声明相同，其Jain指数反映共同需求期间的保留容量时间分配。另行报告完成任务数的Jain指数，二者含义不同。

每次测量以及每条转换记录均对照墙钟与单调时钟，允许偏差为5ms与相应单调时间的0.5%中的较大值，超过即拒绝本次证据。矩阵开始和结束的独立宿主机对照由正式运行者记录。

`metrics.csv`保留全部测量结果，`validation.json`报告每场景每策略3次结果的中位数及最小最大值，以及每次配对相对FIFO的所有变化，包括变差的指标。三个重复用于描述结果，不宣称统计显著性或置信区间。

## 结论边界

这是通过生产Coordinator运行的受控进程实验。FIFO可以直接使用已有scheduler.Policy，无需对生产调度器增加兼容适配。子进程通过sleep形成可重复的保留容量时长，不测CPU利用率，也不启用cgroup硬限制。两工作流内部为独立任务，本实验不验证DAG关键路径排序、模型输出质量或微舆业务性能。当前默认策略为dag-drcfs，dag-drcfs-time是显式选择的时长计费策略。
