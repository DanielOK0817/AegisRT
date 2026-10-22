# 测试与验证材料

- [微舆真实应用运行验证](asian-games/README.md)：亚运会案例的运行采样、报告和验证记录。
- [三项机制实验](innovation/README.md)：调度、Context与完成边界的完整原始数据及协议。

在仓库根目录运行python -B tests/verify_evidence.py，可离线核验归档及主要统计量，不调用模型。运行python -B tests/run_bettafish_offline.py执行适配器和集成测试。

需要运行原始核验器时，先用python -B tests/extract_evidence.py --kind scheduler --output ../scheduler-evidence将材料解压到外部新目录，再按各材料说明运行。kind还支持context、completion、asian-games。

src/aegisrt/evidence中的历史夹具供源码测试读取，保持原字节。离线演示及应用结果以asian-games目录为入口。
