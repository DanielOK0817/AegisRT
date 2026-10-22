"""Render verified measured data only; every matrix configuration is included."""
import argparse
from collections import defaultdict
import csv
import json
import hashlib
from pathlib import Path
import statistics

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager
import numpy as np

plt.rcParams.update({"font.family":"sans-serif", "font.sans-serif":["Microsoft YaHei","Arial","DejaVu Sans"],
    "font.size":7,"axes.titlesize":8,"axes.labelsize":7,"xtick.labelsize":6,"ytick.labelsize":6,
    "axes.spines.right":False,"axes.spines.top":False,"axes.linewidth":0.6,
    "legend.frameon":False,"pdf.fonttype":42,"svg.fonttype":"none","savefig.facecolor":"white"})
COLORS=["#7C8188","#BA8552","#3E7E85"]
MARKERS=["o","s","^"]


def table(path):
    with path.open(encoding="utf-8-sig",newline="") as stream:return list(csv.DictReader(stream))


def export(fig,path):
    fig.savefig(str(path)+".svg")
    fig.savefig(str(path)+".pdf")
    fig.savefig(str(path)+".png",dpi=600)
    fig.savefig(str(path)+".tiff",dpi=600,pil_kwargs={"compression":"tiff_lzw"})
    plt.close(fig)


def points(ax,rows,configs,keys,group,levels,metric,labels,log=False):
    all_min=[]
    for index,level in enumerate(levels):
        selected={tuple(row[k] for k in keys):row for row in rows if row[group]==level}
        values=[float(selected[c][metric+"_median"]) for c in configs]
        low=[float(selected[c][metric+"_min"]) for c in configs]
        high=[float(selected[c][metric+"_max"]) for c in configs]
        all_min.extend(low)
        x=np.arange(len(configs))+(index-1)*0.18
        ax.errorbar(x,values,yerr=[np.array(values)-low,np.array(high)-values],fmt=MARKERS[index],
            markersize=2.8,elinewidth=0.6,capsize=1.5,color=COLORS[index],label=labels[index],alpha=0.95)
    if log and min(all_min)>0:ax.set_yscale("log")
    ax.set_xlim(-0.6,len(configs)-0.4)
    ax.grid(axis="y",color="#E5E7E9",linewidth=0.5)
    ax.set_axisbelow(True)


def main():
    parser=argparse.ArgumentParser();parser.add_argument("run",type=Path)
    parser.add_argument("--communication-run",type=Path);args=parser.parse_args()
    analysis=args.run/"analysis";verified=json.loads((analysis/"verification.json").read_text())
    passes=json.loads((args.run/"run.json").read_text(encoding="utf-8")).get("compression_read_passes",1)
    if not verified["passed"] or verified["pilot"]:raise RuntimeError("only verified formal data can be plotted")
    target=analysis/"figures";target.mkdir(exist_ok=True)
    font_manager.findfont("Microsoft YaHei",fallback_to_default=False)
    communication_analysis=(args.communication_run/"analysis") if args.communication_run else analysis
    comm_verified=json.loads((communication_analysis/"verification.json").read_text(encoding="utf-8"))
    if not comm_verified["passed"] or comm_verified["pilot"]:raise RuntimeError("communication data not verified")
    comm=table(communication_analysis/"communication-summary.csv")
    configs=[(str(size),str(r),str(c)) for size in (256,4096,32768) for r in (1,4) for c in (1,4)]
    labels=[f"{int(s)/1024:g}/{r}/{c}" for s,r,c in configs]
    fig,axes=plt.subplots(3,2,figsize=(7.204724,7.874016))  # 183 mm by 200 mm
    fig.subplots_adjust(left=0.11,right=0.98,top=0.89,bottom=0.14,hspace=0.8,wspace=0.32)
    fig.suptitle("通信版本对照：延迟与分配开销",fontsize=11,y=0.982)
    for index,(operation,title) in enumerate([("output_publish","产物发布"),("context_load","已加载读取"),("context_hydrate","首次依赖补发")]):
        rows=[r for r in comm if r["operation"]==operation]
        for col,(metric,ylabel) in enumerate([("latency_ms","延迟（ms）"),("alloc_kib","每次操作分配量（KiB）")]):
            ax=axes[index,col]
            points(ax,rows,configs,["payload_bytes","recipients","concurrency"],"variant",["A","B","C"],metric,
                ["A：原版*","B：重建消融版","C：当前版"],log=True)
            ax.set_title(title,loc="left",pad=8);ax.set_ylabel(ylabel)
            ax.set_xticks(range(12),labels,rotation=65,ha="right")
            ax.text(-0.18,1.08,chr(97+index*2+col),transform=ax.transAxes,fontweight="bold",fontsize=9)
    handles,names=axes[0,0].get_legend_handles_labels()
    fig.legend(handles,names,loc="upper center",bbox_to_anchor=(0.53,0.956),ncol=3,fontsize=7)
    fig.text(0.5,0.045,"横轴：正文KiB/接收方数/并发数。点：六轮中位数；误差线：完整范围。",ha="center",fontsize=6.5)
    fig.text(0.5,0.022,"*原版未通过同Agent任务隔离；A与B/C不能作为同等正确性下的单机制比较。",ha="center",fontsize=6.5)
    export(fig,target/"communication-comparison")

    comp=table(analysis/"compression-summary.csv")
    keys=["corpus","words","fraction_eighths"]
    configs=[(corpus,str(words),str(frac)) for corpus in ("english","mixed") for words in (256,2048) for frac in (1,4,8)]
    labels=[f"{'英' if c=='english' else '混'}/{w}/{int(f)*100/8:g}%" for c,w,f in configs]
    fig,axes=plt.subplots(2,2,figsize=(7.204724,6.692913))  # 183 mm by 170 mm
    fig.subplots_adjust(left=0.10,right=0.98,top=0.86,bottom=0.23,hspace=0.85,wspace=0.32)
    fig.suptitle("本地压缩消融：信息保留与重复计算",fontsize=11,y=0.982)
    modes=["off","no-cache","cache"];names=["关闭压缩","禁用缓存命中","持久缓存"]
    panels=[("retained_fraction","保留计数量/源计数量","cold",False),
            ("facts_fraction","三项事实标记保留率","cold",False),
            ("latency_ms","重复读取延迟（ms）","warm",True),
            ("summary_calls_per_read","每次重复读取的摘要计算次数","warm",False)]
    for index,(metric,ylabel,phase,log) in enumerate(panels):
        ax=axes.flat[index];rows=[r for r in comp if r["phase"]==phase]
        points(ax,rows,configs,keys,"mode",modes,metric,names,log=log)
        ax.set_ylabel(ylabel);ax.set_xticks(range(12),labels,rotation=65,ha="right")
        if not log:ax.set_ylim(-0.05,1.1)
        ax.text(-0.16,1.09,chr(97+index),transform=ax.transAxes,fontweight="bold",fontsize=9)
    handles,names=axes[0,0].get_legend_handles_labels()
    fig.legend(handles,names,loc="upper center",bbox_to_anchor=(0.52,0.948),ncol=3,fontsize=7)
    fig.text(0.5,0.065,"横轴：语料/生成单元数/预算比例。每组配置独立建库六轮。",ha="center",fontsize=6.3)
    fig.text(0.5,0.043,f"每轮8组×{passes}次重复读取先归一化，再取轮内均值；点为六轮中位数，线为完整范围。",ha="center",fontsize=6.3)
    fig.text(0.5,0.022,"本地计数与人工事实标记不代表真实模型用量、语义质量或业务效果。",ha="center",fontsize=6.3)
    export(fig,target/"compression-ablation")
    sources={"communication_run":str(communication_analysis.parent),"compression_run":str(args.run),
        "communication_csv_sha256":hashlib.sha256((communication_analysis/"communication-summary.csv").read_bytes()).hexdigest(),
        "compression_csv_sha256":hashlib.sha256((analysis/"compression-summary.csv").read_bytes()).hexdigest(),
        "plot_script_sha256":hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
    (target/"figure-sources.json").write_text(json.dumps(sources,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
    print(target)


if __name__=="__main__":main()
