# 调度竞争实验

evidence.zip保留完整matrix、原始事件、任务时间记录、配置、指标和当时source源码。18轮正式测量、3轮预热。归档内保留当时的contention实验二进制以核对哈希，不作为当前应用入口。归档中的3份实验脚本与当前源码不同，均按原哈希保留。

从提交根目录解压到外部目录后运行：

```powershell
python -B tests/extract_evidence.py --kind scheduler --output ../scheduler-evidence
python -B ../scheduler-evidence/matrix/source/experiments/workflow-contention/verify.py ../scheduler-evidence/matrix
```

原始源码清单可能包含未参与本次负载的历史模块身份，属于测量来源记录，不是正式应用入口。
