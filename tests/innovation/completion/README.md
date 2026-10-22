# 任务完成边界实验

evidence.zip包含runs下25轮进程故障原始数据，以及protocol下的采集与独立核验代码。各状态信封及校验和保留，用于检查任务重试、发布和恢复边界。

```powershell
python -B tests/extract_evidence.py --kind completion --output ../completion-evidence
python -B ../completion-evidence/protocol/verify.py ../completion-evidence/runs
```

该命令只核验已有数据，不启动故障实验。历史协议版本与当前源码分开记录。
