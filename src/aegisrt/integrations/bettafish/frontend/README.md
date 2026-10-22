# AegisRT运行监控台

界面使用React、Vite与shadcn/ui。依赖版本由package-lock.json记录，组件来源见source-provenance.json。

在外部源码副本中运行npm ci和npm run build，产物为相邻presentation目录的index.html、runtime.js与runtime.css。后端通过同源/api/aegisrt/ui/提供静态文件。

页面展示真实任务状态、已保存内容和进程资源；逻辑Token与供应商计费Token分别记录。报告只有通过后端校验后才能打开。界面不通过计时器生成任务进度或研究内容。
