# 完成提交边界实验

本实验测量任务被突然中断后，执行次数、持久产物、下游输入和资源预留是否一致。使用真实Coordinator、Worker进程、Runtime、ContextControl、ContextRepository和statefile组件；实验包装器在准确的生产调用边界阻塞，外部Python控制器发送硬终止，再启动新进程恢复。实验不调用模型。

| 故障位置 | 磁盘上的预期状态 | 恢复后的预期行为 |
|---|---|---|
| 无故障对照 | 上下游都成功 | 再打开不执行任务 |
| Agent执行中 | 上游RUNNING，没有完成意图或产物 | 清理登记的实验子进程，第二次执行上游 |
| 完成意图持久化后 | 上游RUNNING，保存完整输出，Context未发布 | 直接发布保存的正文，保持第一次尝试 |
| 产物持久化后 | 上游RUNNING，Context已有产物，成功尚未提交 | 幂等重放发布，成功提交，不重复执行Agent |
| 成功状态持久化后 | 上游成功、下游READY，下游尚未执行 | 执行下游一次，不重放上游 |

每轮保留硬终止前、恢复后、再次打开后的两个原始状态文件。独立verify.py先验证statefile的SHA-256包络，再读取任务状态、尝试计数、资源预留、完成意图、内容页、产物和真实Agent日志。检查下游输入与冻结研究正文完全一致，并通过生产存储写入日志核对产物只新增一次。实验代码不会输出自行声明通过的布尔值。

普通Windows主机或原生Linux运行：

```powershell
python experiments/completion-boundaries/run.py --output evidence/completion-boundaries-new-run --repeat 5
python experiments/completion-boundaries/verify.py evidence/completion-boundaries-new-run
```

输出目录必须不存在。运行器直接构建一个实验二进制并反复调用它，不启动HTTP监听，不接触现有Runtime、微舆服务、数据库或模型凭据。默认5种场景各5次，共25轮。可以通过`--points prepared published succeeded`只运行新补充的提交边界。

将Windows交叉编译结果部署到openEuler时，先用`GOOS=linux GOARCH=amd64 go build`生成二进制，将本目录脚本与源码、构建时的源文件SHA-256映射一起复制到独立目录，再执行：

```bash
python3 run.py --binary ./boundary-probe --source-manifest source-manifest.json --output ./evidence-native --repeat 5
python3 verify.py ./evidence-native
```

执行中故障只包含一个不创建后代的实验Agent。恢复适配器先核对PID的可执行文件路径和独立实验目录，再清理该进程；Linux接受已不执行的孤儿僵尸状态，由系统init负责回收。其余三个边界位于Worker进程已退出、输出已解析后的生产提交路径，恢复直接调用Coordinator.RecoverOutputs。

因此，结果支持生产组件的完成提交、幂等发布和下游激活正确性，不等价于完整HTTPdaemon硬重启、cgroup子进程树清理或模型服务恢复。那些能力应引用已有openEuler真实运行恢复证据。表中的恢复耗时包含新进程启动、两个状态仓库恢复和下游执行，只用于描述本实验，不作稳定性能优势结论。

既有补充依据包括`internal/controlplane/completion_recovery_test.go`的内存保存失败与确认丢失测试、`internal/runtimecore/completion_test.go`的完成意图恢复测试，以及`cmd/aegisrt/completion_recovery_test.go`的daemon启动发布恢复测试。本实验补充实际磁盘、真实Worker子进程、外部硬终止和独立状态核验。
