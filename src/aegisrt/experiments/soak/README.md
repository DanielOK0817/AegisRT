# 持续进程负载

本程序通过生产Coordinator、Runtime Core、Worker和持久化状态执行真实子进程。子进程只读取请求并等待固定时间，因此它是合成负载，不是业务应用，不调用模型，也不测试内核资源限制。

## 固定设计

- 四个Worker；总容量1000millicpu、256MiB；每任务请求250millicpu、64MiB。
- short工作流进程等待1秒，long等待3秒；两者优先级相同。
- 每工作流未完成任务少于16个时再追加16个，持续到达600秒，再排空全部任务。
- 每次任务转换使用真实检查点，事件保存进程起止时间；每秒记录Runtime账本、Go堆和goroutine数量。
- 分别使用`dag-drcfs`与`dag-drcfs-time`，顺序运行，测量期间不并发构建或跑其他实验。
- 这是闭环饱和到达，策略不同会导致提交总任务数不同。比较固定窗口占用分配，不声称同一有限任务序列的整体加速。

## 复现

```powershell
go build -o <output-directory>/soak.exe ./experiments/soak
<output-directory>/soak.exe -duration 10m -policy dag-drcfs -output <new-share-directory>
<output-directory>/soak.exe -duration 10m -policy dag-drcfs-time -output <new-time-directory>
python experiments/soak/verify.py <new-share-directory> <new-time-directory>
```

输出目录必须不存在。记录源码清单、归档和二进制SHA256，以及Go版本和主机信息。一次短预检不能并入正式负载统计；代码变更后重新测量必须使用新目录。

`verify.py`独立读取原始事件，对观察窗口两端裁剪进程区间，重算两个工作流累计经过时间的Jain指数，并核对峰值并发、任务与事件的一一对应、最终任务成功状态、资源归零、无待完成记录和检查点校验和。它不把summary中的成功布尔值当成唯一依据。

## 结论范围

占用时间表示声明容量被执行任务占用的经过时间，sleep进程本身几乎不消耗CPU，不能用本实验声称实际CPU消耗公平。每策略一次运行只能提供该负载的描述性结果，不给总体显著性或置信区间。

Runtime仍保留任务历史且有4096任务上限，本入口没有清理任务历史或突破容量限制。运行时长应与任务总量匹配；超出上限的运行属于容量拒绝，不能改成静默丢历史。600秒持续到达及排空不能替代多日运行、多轮负载、真实业务或高并发可靠性验收。
