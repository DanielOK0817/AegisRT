# 微舆九任务接入

当前流程依次完成三路初始研究、汇总讨论、三路补充研究、最终讨论和综合报告，共九项任务。Query、Media和Insight保留独立研究角色，AegisRT负责依赖和进程执行。

runner.py构建任务图，adapter.py执行研究与反馈，comprehensive_report.py生成并校验报告，app_service.py提供应用接口，presentation_api.py和runtime_monitor.py提供状态、已保存内容及资源观测。frontend为当前监控台源码，presentation为其构建产物。

使用[提交包九任务入口](../../../../demo/README.md)。模型配置、样本、输出及二进制路径均由入口显式提供，真实调用不在包内写入私有配置。

runner.py默认完整规模的模型请求上限为223次。提交包demo/run_live.py选用小规模参数，单轮上限58次；两者的参数范围不同，实际调用数以各轮记录为准。

离线测试见[测试说明](../../../../tests/README.md)。真实案例与性能记录按原实验版本保存；replay分支只用于受控离线测试，不作为正式应用演示。
