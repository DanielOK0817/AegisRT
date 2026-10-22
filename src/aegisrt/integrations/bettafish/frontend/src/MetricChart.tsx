export type Sample = {
  time: number;
  cpu: number | null;
  memory: number | null;
  tasks: Record<string, { cpu: number | null; memory: number | null }>;
};

export default function MetricChart({
  values,
  max = 100,
  unit = "%",
  compact = false,
}: {
  values: { time: number; value: number | null }[];
  max?: number;
  unit?: string;
  compact?: boolean;
}) {
  const end = values.at(-1)?.time ?? Date.now(),
    start = end - 60000;
  const samples = values.filter((v) => v.time >= start && v.value != null);
  const width = compact ? 320 : 440,
    height = compact ? 58 : 150;
  const y = (n: number) =>
    height - Math.max(0, Math.min(n / Math.max(max, 1), 1)) * height;
  const coords = samples.map(
    (v) => `${((v.time - start) / 60000) * width},${y(v.value!)}`,
  );
  const axis = (n: number) =>
    unit === "%"
      ? `${Math.round(n)}%`
      : n >= 1024 ** 3
        ? `${(n / 1024 ** 3).toFixed(1)} GB`
        : `${Math.round(n / 1024 ** 2)} MB`;
  return (
    <div className={`metric-chart ${compact ? "compact" : ""}`}>
      <div className="chart-axis">
        <span>{axis(max)}</span>
        <span>{axis(max / 2)}</span>
        <span>{unit === "%" ? "0%" : "0 MB"}</span>
      </div>
      <div className="chart-plot">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          aria-label="最近60秒实际采样趋势"
        >
          <path
            d={`M0 1H${width} M0 ${height / 2}H${width} M0 ${height - 1}H${width} M1 0V${height} M${width / 4} 0V${height} M${width / 2} 0V${height} M${(width * 3) / 4} 0V${height} M${width - 1} 0V${height}`}
            className="chart-grid"
          />
          {coords.length > 1 && (
            <>
              <polygon
                points={`${((samples[0].time - start) / 60000) * width},${height} ${coords.join(" ")} ${((samples.at(-1)!.time - start) / 60000) * width},${height}`}
                className="chart-area"
              />
              <polyline points={coords.join(" ")} className="chart-line" />
            </>
          )}
          {coords.length === 1 && (
            <circle
              cx={((samples[0].time - start) / 60000) * width}
              cy={y(samples[0].value!)}
              r="3"
              className="chart-point"
            />
          )}
        </svg>
        {!samples.length && <span className="chart-empty">等待实际采样</span>}
        <div className="chart-time">
          {!compact && (
            <>
              <span>60秒前</span>
              <span>45秒前</span>
              <span>30秒前</span>
              <span>15秒前</span>
            </>
          )}
          <span>{compact ? "最近60秒" : "现在"}</span>
        </div>
      </div>
    </div>
  );
}
