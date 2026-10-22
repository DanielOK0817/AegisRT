# 微舆真实应用运行验证

话题为2026年爱知·名古屋亚运会中国代表队成绩。运行编号为bf-entry-20261006-213814-6990a77bf8。

## 材料与结果

- evidence.zip：运行采样、原始任务快照、各研究任务结果与请求预算、报告内容、证据审核及验证记录。
- run-validation.json：初次运行的九项任务及46次模型请求记录。
- latest.json：复用8项研究与讨论结果完成报告的运行状态，报告阶段调用模型2次。
- verification.json：报告校验通过，包含7个章节和15个段落；完成后四类资源分配量均归零。
- [运行观测](../../demo/index.html)与[原始报告](../../demo/asian-games/report.html)：离线查看入口。

原始阶段记录与报告恢复阶段分别保留。fresh_from_zero为false；归档保留初次报告任务的原始终态。aggregate_requests为此前录制和后续报告阶段的累计请求，不是这次报告阶段的请求数。

## 离线核验

在仓库根目录执行python -B tests/verify_evidence.py，校验归档、运行结果、报告哈希、章节数量、资源释放与说明书所用采样值。该检查不调用模型。

需要查看原始文件时，执行python -B tests/extract_evidence.py --kind asian-games --output ../asian-games-evidence。归档中的绝对路径用于记录原运行环境；离线检查使用包内相对路径。
