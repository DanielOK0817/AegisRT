# Context通信与压缩实验

evidence.zip包含run下的原始测量、Context状态及构建源码，根目录的冻结配置与源码归档，tools下的独立分析与负面对照工具；归档内保留四个被核验器要求的实验二进制，仅用于核对测量身份，不作为当前运行入口。648次正式通信测量及压缩记录按原日期保存，零时长和缺失派生值保持原样。

```powershell
python -B tests/extract_evidence.py --kind context --output ../context-evidence
python -B ../context-evidence/tools/analyze.py ../context-evidence/run
python -B ../context-evidence/tools/verify_checks.py ../context-evidence/run
```

分析写入外部副本，不改提交包。CPU、时钟与实验版本按归档记录解释，不将历史结果视为当前版本的实测。
