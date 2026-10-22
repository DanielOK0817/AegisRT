# openEuler部署

在可写的源码副本中执行scripts/openeuler/preflight.sh --json检查平台。build.sh使用本地Go工具链，构建aegisrt与aegisbench；SHA256SUMS采用two-line清单，共两项。源代码导出无Git记录时，构建清单将版本标为source-export，精确身份另见提交文件清单。

```bash
bash scripts/openeuler/build.sh
cd dist/openeuler
sha256sum --check --strict SHA256SUMS
cd ../..
sudo bash scripts/openeuler/install.sh --agents-file /absolute/path/agents.json
```

首次安装必须显式提供适合目标机器的Agent注册表。安装器创建服务账号、凭据和通用systemd单元，不默认装载业务示例，不自动启动服务。检查注册表后再执行systemctl enable --now aegisrt.service。

verify-platform.sh只核验平台、健康接口和cgroup委派，业务验收单独执行。微舆九任务入口见[正式演示说明](../../../../demo/README.md)。

静态单元aegisrt-bench-f4-smoke.service与aegisrt-bench-resource-smoke.service为static基准服务，均设置Restart=no，数据目录为/var/lib/aegisrt-bench。安装不自动启动基准，需根据目标机资源条件显式运行。平台检查通过不代表真实业务性能对照通过。
