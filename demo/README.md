# AegisRT运行时演示

打开[index.html](index.html)，查看微舆真实应用中的任务依赖、进程CPU与内存、资源账本、Context传递及报告结果。应用话题为2026年爱知·名古屋亚运会中国代表队成绩，数据来自保存的真实运行采样，页面离线可用。

业务流程包含9项任务、16条依赖边。报告阶段复用8项研究与讨论结果完成7章报告，相关原始记录、报告恢复记录和逐文件校验入口见[验证材料](../tests/asian-games/README.md)。

## 运行当前代码

在仓库根目录执行python -B demo/run_live.py --output ../AegisRT-asian-games-work，可在外部新目录准备源码和亚运会样本、完成构建并生成启动计划，模型调用为0。添加--env-file及--execute才执行真实运行，单轮最多58次模型请求。依赖与完整命令见[根目录说明](../README.md)。
