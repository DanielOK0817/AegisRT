# Context计量、压缩与迁移

守护进程默认使用whitespace-v1近似计数，支持通过context-counter选择unicode-segment-v1。两种本地计数用于运行时预算，不等于供应商计费Tokenizer。

context-compression默认none-v1；deterministic-extractive-v1提供确定性截取和持久摘要缓存。摘要与来源共同保存，计数器和摘要策略身份不一致时拒绝混用状态。截取不保证语义完整，也不构成模型Token节省的实测证据。

旧检查点需要迁移时，先停用原运行目录并保留副本，使用cmd/aegis-context-migrate工具在独立路径生成新文件。运行工具的-help查看输入、输出及任务绑定参数。完整历史用于验证归属，历史不足时须显式提供任务绑定，不能根据旧Agent主体推测权限。

迁移工具源码与测试位于[cmd/aegis-context-migrate](../cmd/aegis-context-migrate)。任务输入权限、检查点与压缩边界由当前测试验证；实验记录另见[Context实验](../../../tests/innovation/context/README.md)。
