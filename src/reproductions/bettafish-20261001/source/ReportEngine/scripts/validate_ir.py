#!/usr/bin/env python3
"""
IR 文档验证工具。

命令行工具，用于：
- 扫描指定 JSON 文件中的所有图表和表格
- 报告结构问题和数据缺失
- 支持自动修复常见问题
- 支持批量处理

使用方法:
    python -m ReportEngine.scripts.validate_ir chapter-030-section-3-0.json
    python -m ReportEngine.scripts.validate_ir *.json --fix
    python -m ReportEngine.scripts.validate_ir ./output/ --recursive --fix --verbose
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple
from dataclasses import dataclass, field

# 添加项目根目录到路径
project_root = Path(__file__).parent.parent.parent
if str(project_root) not in sys.path:
    sys.path.insert(0, str(project_root))

from loguru import logger

from ReportEngine.utils.chart_validator import (
    ChartValidator,
    ChartRepairer,
    ValidationResult,
)
from ReportEngine.utils.table_validator import (
    TableValidator,
    TableRepairer,
    TableValidationResult,
)


@dataclass
class BlockIssue:
    """单个 block 的问题"""
    block_type: str
    block_id: str
    path: str
    errors: List[str] = field(default_factory=list)
    warnings: List[str] = field(default_factory=list)
    is_fixable: bool = False


@dataclass
class DocumentReport:
    """文档验证报告"""
    file_path: str
    total_blocks: int = 0
    chart_count: int = 0
    table_count: int = 0
    wordcloud_count: int = 0
    issues: List[BlockIssue] = field(default_factory=list)
    fixed_count: int = 0

    @property
    def has_issues(self) -> bool:
        return len(self.issues) > 0

    @property
    def error_count(self) -> int:
        return sum(len(issue.errors) for issue in self.issues)

    @property
    def warning_count(self) -> int:
        return sum(len(issue.warnings) for issue in self.issues)


class IRValidator:
    """IR 文档验证器"""

    def __init__(
        self,
        chart_validator: Optional[ChartValidator] = None,
        table_validator: Optional[TableValidator] = None,
        chart_repairer: Optional[ChartRepairer] = None,
        table_repairer: Optional[TableRepairer] = None,
    ):
        self.chart_validator = chart_validator or ChartValidator()
        self.table_validator = table_validator or TableValidator()
        self.chart_repairer = chart_repairer or ChartRepairer(self.chart_validator)
        self.table_repairer = table_repairer or TableRepairer(self.table_validator)

    def validate_document(
        self,
        document: Dict[str, Any],
        file_path: str = "<unknown>",
    ) -> DocumentReport:
        """
        验证整个文档。

        Args:
            document: IR 文档数据
            file_path: 文件路径（用于报告）

        Returns:
            DocumentReport: 验证报告
        """
        report = DocumentReport(file_path=file_path)

        # 遍历所有章节
        chapters = document.get("chapters", [])
        for chapter_idx, chapter in enumerate(chapters):
            if not isinstance(chapter, dict):
                continue

            chapter_id = chapter.get("chapterId", f"chapter-{chapter_idx}")
            blocks = chapter.get("blocks", [])

            self._validate_blocks(
                blocks,
                f"chapters[{chapter_idx}].blocks",
                chapter_id,
                report,
            )

        return report

    def _validate_blocks(
        self,
        blocks: List[Any],
        path: str,
        chapter_id: str,
        report: DocumentReport,
    ):
        """递归验证 blocks 列表"""
        if not isinstance(blocks, list):
            return

        for idx, block in enumerate(blocks):
            if not isinstance(block, dict):
                continue

            report.total_blocks += 1
            block_path = f"{path}[{idx}]"
            block_type = block.get("type", "")
            block_id = block.get("widgetId") or block.get("id") or f"block-{idx}"

            # 根据类型验证
            if block_type == "widget":
                widget_type = (block.get("widgetType") or "").lower()
                if "chart.js" in widget_type:
                    report.chart_count += 1
                    self._validate_chart(block, block_path, block_id, report)
                elif "wordcloud" in widget_type:
                    report.wordcloud_count += 1
                    self._validate_wordcloud(block, block_path, block_id, report)

            elif block_type == "table":
                report.table_count += 1
                self._validate_table(block, block_path, block_id, report)

            # 递归处理嵌套 blocks
            nested_blocks = block.get("blocks")
            if isinstance(nested_blocks, list):
                self._validate_blocks(nested_blocks, f"{block_path}.blocks", chapter_id, report)

            # 处理 table rows 中的 blocks
            if block_type == "table":
                rows = block.get("rows", [])
                for row_idx, row in enumerate(rows):
                    if isinstance(row, dict):
                        cells = row.get("cells", [])
                        for cell_idx, cell in enumerate(cells):
                            if isinstance(cell, dict):
                                cell_blocks = cell.get("blocks", [])
                                self._validate_blocks(
                                    cell_blocks,
                                    f"{block_path}.rows[{row_idx}].cells[{cell_idx}].blocks",
                                    chapter_id,
                                    report,
                                )

            # 处理 list items 中的 blocks
            if block_type == "list":
                items = block.get("items", [])
                for item_idx, item in enumerate(items):
                    if isinstance(item, list):
                        self._validate_blocks(
                            item,
                            f"{block_path}.items[{item_idx}]",
                            chapter_id,
                            report,
                        )

    def _validate_chart(
        self,
        block: Dict[str, Any],
        path: str,
        block_id: str,
        report: DocumentReport,
    ):
        """验证图表"""
        result = self.chart_validator.validate(block)

        if not result.is_valid or result.warnings:
            issue = BlockIssue(
                block_type="chart",
                block_id=block_id,
                path=path,
                errors=result.errors,
                warnings=result.warnings,
                is_fixable=result.has_critical_errors(),
            )
            report.issues.append(issue)

    def _validate_table(
        self,
        block: Dict[str, Any],
        path: str,
        block_id: str,
        report: DocumentReport,
    ):
        """验证表格"""
        result = self.table_validator.validate(block)

        if not result.is_valid or result.warnings or result.nested_cells_detected:
            issue = BlockIssue(
                block_type="table",
                block_id=block_id,
                path=path,
                errors=result.errors,
                warnings=result.warnings,
                is_fixable=result.nested_cells_detected or result.has_critical_errors(),
            )

            # 添加嵌套 cells 警告
            if result.nested_cells_detected:
                issue.warnings.insert(0, "检测到嵌套 cells 结构（LLM 常见错误）")

            # 添加空单元格信息
            if result.empty_cells_count > 0:
                issue.warnings.append(
                    f"空单元格数量: {result.empty_cells_count}/{result.total_cells_count}"
                )

            report.issues.append(issue)

    def _validate_wordcloud(
        self,
        block: Dict[str, Any],
        path: str,
        block_id: str,
        report: DocumentReport,
    ):
        """验证词云"""
        errors: List[str] = []
        warnings: List[str] = []

        # 检查数据结构
        data = block.get("data")
        props = block.get("props", {})

        words_found = False
        words_count = 0

        # 检查各种可能的词云数据路径
        data_paths = [
            ("data.words", data.get("words") if isinstance(data, dict) else None),
            ("data.items", data.get("items") if isinstance(data, dict) else None),
            ("data", data if isinstance(data, list) else None),
            ("props.words", props.get("words") if isinstance(props, dict) else None),
            ("props.items", props.get("items") if isinstance(props, dict) else None),
            ("props.data", props.get("data") if isinstance(props, dict) else None),
        ]

        for path_name, value in data_paths:
            if isinstance(value, list) and len(value) > 0:
                words_found = True
                words_count = len(value)

                # 验证词云项格式
                for idx, item in enumerate(value[:5]):  # 只检查前5个
                    if isinstance(item, dict):
                        word = item.get("word") or item.get("text") or item.get("label")
                        weight = item.get("weight") or item.get("value")
                        if not word:
                            warnings.append(f"{path_name}[{idx}] 缺少 word/text/label 字段")
                        if weight is None:
                            warnings.append(f"{path_name}[{idx}] 缺少 weight/value 字段")
                    elif not isinstance(item, (str, list, tuple)):
                        warnings.append(f"{path_name}[{idx}] 格式不正确")

                break

        if not words_found:
            errors.append("词云数据缺失：未在 data.words, data.items, props.words 等路径找到有效数据")
        elif words_count == 0:
            warnings.append("词云数据为空")

        if errors or warnings:
            issue = BlockIssue(
                block_type="wordcloud",
                block_id=block_id,
                path=path,
                errors=errors,
                warnings=warnings,
                is_fixable=False,  # 词云数据缺失通常无法自动修复
            )
            report.issues.append(issue)

    def repair_document(
        self,
        document: Dict[str, Any],
        report: DocumentReport,
    ) -> Tuple[Dict[str, Any], int]:
        """
        修复文档中的问题。

        Args:
            document: IR 文档数据
            report: 验证报告

        Returns:
            Tuple[Dict[str, Any], int]: (修复后的文档, 修复数量)
        """
        fixed_count = 0

        # 遍历所有章节
        chapters = document.get("chapters", [])
        for chapter in chapters:
            if not isinstance(chapter, dict):
                continue

            blocks = chapter.get("blocks", [])
            chapter["blocks"], chapter_fixed = self._repair_blocks(blocks)
            fixed_count += chapter_fixed

        return document, fixed_count

    def _repair_blocks(
        self,
        blocks: List[Any],
    ) -> Tuple[List[Any], int]:
        """递归修复 blocks 列表"""
        if not isinstance(blocks, list):
            return blocks, 0

        fixed_count = 0
        repaired_blocks: List[Any] = []

        for block in blocks:
            if not isinstance(block, dict):
                repaired_blocks.append(block)
                continue

            block_type = block.get("type", "")

            # 修复表格
            if block_type == "table":
                result = self.table_repairer.repair(block)
                if result.has_changes():
                    block = result.repaired_block
                    fixed_count += 1
                    logger.info(f"修复表格: {result.changes}")

            # 修复图表
            elif block_type == "widget":
                widget_type = (block.get("widgetType") or "").lower()
                if "chart.js" in widget_type:
                    result = self.chart_repairer.repair(block)
                    if result.has_changes():
                        block = result.repaired_block
                        fixed_count += 1
                        logger.info(f"修复图表: {result.changes}")

            # 递归处理嵌套 blocks
            nested_blocks = block.get("blocks")
            if isinstance(nested_blocks, list):
                block["blocks"], nested_fixed = self._repair_blocks(nested_blocks)
                fixed_count += nested_fixed

            # 处理 table rows 中的 blocks
            if block_type == "table":
                rows = block.get("rows", [])
                for row in rows:
                    if isinstance(row, dict):
                        cells = row.get("cells", [])
                        for cell in cells:
                            if isinstance(cell, dict):
                                cell_blocks = cell.get("blocks", [])
                                cell["blocks"], cell_fixed = self._repair_blocks(cell_blocks)
                                fixed_count += cell_fixed

            # 处理 list items 中的 blocks
            if block_type == "list":
                items = block.get("items", [])
                for i, item in enumerate(items):
                    if isinstance(item, list):
                        items[i], item_fixed = self._repair_blocks(item)
                        fixed_count += item_fixed

            repaired_blocks.append(block)

        return repaired_blocks, fixed_count


def print_report(report: DocumentReport, verbose: bool = False):
    """打印验证报告"""
    print(f"\n{'=' * 60}")
    print(f"文件: {report.file_path}")
    print(f"{'=' * 60}")

    print(f"\n📊 统计:")
    print(f"  - 总 blocks: {report.total_blocks}")
    print(f"  - 图表数量: {report.chart_count}")
    print(f"  - 表格数量: {report.table_count}")
    print(f"  - 词云数量: {report.wordcloud_count}")

    if report.has_issues:
        print(f"\n⚠️  发现 {len(report.issues)} 个问题:")
        print(f"  - 错误: {report.error_count}")
        print(f"  - 警告: {report.warning_count}")

        if verbose:
            for issue in report.issues:
                print(f"\n  [{issue.block_type}] {issue.block_id}")
                print(f"    路径: {issue.path}")
                if issue.errors:
                    for error in issue.errors:
                        print(f"    ❌ {error}")
                if issue.warnings:
                    for warning in issue.warnings:
                        print(f"    ⚠️  {warning}")
                if issue.is_fixable:
                    print(f"    🔧 可自动修复")
    else:
        print(f"\n✅ 未发现问题")

    if report.fixed_count > 0:
        print(f"\n🔧 已修复 {report.fixed_count} 个问题")


def validate_file(
    file_path: Path,
    validator: IRValidator,
    fix: bool = False,
    verbose: bool = False,
) -> DocumentReport:
    """验证单个文件"""
    try:
        with open(file_path, "r", encoding="utf-8") as f:
            document = json.load(f)
    except json.JSONDecodeError as e:
        logger.error(f"JSON 解析错误: {file_path}: {e}")
        report = DocumentReport(file_path=str(file_path))
        report.issues.append(BlockIssue(
            block_type="document",
            block_id="root",
            path="",
            errors=[f"JSON 解析错误: {e}"],
        ))
        return report
    except Exception as e:
        logger.error(f"读取文件错误: {file_path}: {e}")
        report = DocumentReport(file_path=str(file_path))
        report.issues.append(BlockIssue(
            block_type="document",
            block_id="root",
            path="",
            errors=[f"读取文件错误: {e}"],
        ))
        return report

    # 验证文档
    report = validator.validate_document(document, str(file_path))

    # 修复问题
    if fix and report.has_issues:
        fixable_issues = [i for i in report.issues if i.is_fixable]
        if fixable_issues:
            logger.info(f"尝试修复 {len(fixable_issues)} 个问题...")
            document, fixed_count = validator.repair_document(document, report)
            report.fixed_count = fixed_count

            if fixed_count > 0:
                # 保存修复后的文件
                backup_path = file_path.with_suffix(f".bak{file_path.suffix}")
                try:
                    # 创建备份
                    import shutil
                    shutil.copy(file_path, backup_path)
                    logger.info(f"已创建备份: {backup_path}")

                    # 保存修复后的文件
                    with open(file_path, "w", encoding="utf-8") as f:
                        json.dump(document, f, ensure_ascii=False, indent=2)
                    logger.info(f"已保存修复后的文件: {file_path}")
                except Exception as e:
                    logger.error(f"保存文件失败: {e}")

    return report


def main():
    """主函数"""
    parser = argparse.ArgumentParser(
        description="IR 文档验证工具",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  %(prog)s chapter-030-section-3-0.json
  %(prog)s *.json --fix
  %(prog)s ./output/ --recursive --fix --verbose
        """,
    )
    parser.add_argument(
        "paths",
        nargs="+",
        help="要验证的 JSON 文件或目录",
    )
    parser.add_argument(
        "-r", "--recursive",
        action="store_true",
        help="递归处理目录",
    )
    parser.add_argument(
        "-f", "--fix",
        action="store_true",
        help="自动修复常见问题",
    )
    parser.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="显示详细信息",
    )
    parser.add_argument(
        "--no-color",
        action="store_true",
        help="禁用彩色输出",
    )

    args = parser.parse_args()

    # 配置日志
    logger.remove()
    if args.verbose:
        logger.add(sys.stderr, level="DEBUG")
    else:
        logger.add(sys.stderr, level="INFO")

    # 收集文件
    files: List[Path] = []
    for path_str in args.paths:
        path = Path(path_str)
        if path.is_file():
            if path.suffix.lower() == ".json":
                files.append(path)
        elif path.is_dir():
            if args.recursive:
                files.extend(path.rglob("*.json"))
            else:
                files.extend(path.glob("*.json"))
        else:
            # 可能是 glob 模式
            import glob
            matched = glob.glob(path_str)
            for m in matched:
                mp = Path(m)
                if mp.is_file() and mp.suffix.lower() == ".json":
                    files.append(mp)

    if not files:
        print("未找到 JSON 文件")
        sys.exit(1)

    print(f"找到 {len(files)} 个文件")

    # 创建验证器
    validator = IRValidator()

    # 验证文件
    total_issues = 0
    total_fixed = 0
    reports: List[DocumentReport] = []

    for file_path in files:
        report = validate_file(file_path, validator, args.fix, args.verbose)
        reports.append(report)
        total_issues += len(report.issues)
        total_fixed += report.fixed_count

        if args.verbose or report.has_issues:
            print_report(report, args.verbose)

    # 打印总结
    print(f"\n{'=' * 60}")
    print("总结")
    print(f"{'=' * 60}")
    print(f"  - 文件数: {len(files)}")
    print(f"  - 问题总数: {total_issues}")
    if args.fix:
        print(f"  - 已修复: {total_fixed}")

    # 返回适当的退出码
    if total_issues > 0 and total_fixed < total_issues:
        sys.exit(1)
    sys.exit(0)


if __name__ == "__main__":
    main()
