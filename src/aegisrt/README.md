# AegisRT运行时

当前应用为微舆九任务协作。运行时负责依赖解锁、调度、公平记账、多资源准入、Context权限与传递、进程生命周期、持久状态和任务边界恢复。

构建、运行与完整测试入口见[提交根目录说明](../../README.md)。

- [系统架构](docs/architecture.md)
- [HTTP API](docs/api.md)
- [Agent协议](docs/agent-protocol.md)
- [微舆接入](integrations/bettafish/README.md)
- [openEuler部署](deploy/openeuler/README.md)
- [实验与验证材料](../../tests/README.md)

cmd/aegisrt为服务程序；cmd/aegisbench为通用基准工具；internal为运行时实现；integrations/bettafish为微舆业务接入与监控台。evidence中的少量历史记录用于回归夹具，不是当前应用展示入口。
