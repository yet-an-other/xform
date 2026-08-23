import { Card } from "@/components/ui/card";

interface MetricCardProps {
  title: string;
  value: string;
  detail: string;
  percent?: number; // omitted for pure readouts (host uptime) — no bar
}

export function MetricCard({ title, value, detail, percent }: MetricCardProps) {
  return (
    <Card className="min-h-[190px] gap-0 p-6 max-sm:min-h-[170px] max-sm:p-5">
      <div className="flex items-baseline justify-between gap-4">
        <h2 className="text-muted-foreground text-xs font-bold tracking-[0.13em] uppercase">
          {title}
        </h2>
        <span className="font-mono text-2xl font-semibold tracking-tight">{value}</span>
      </div>
      {percent !== undefined ? (
        <div
          aria-label={`${title} usage`}
          aria-valuemax={100}
          aria-valuemin={0}
          aria-valuenow={Math.round(percent)}
          className="bg-secondary mt-9 mb-4 h-1.5 overflow-hidden rounded-full"
          role="progressbar"
        >
          <span
            className="from-meter-start to-meter-end shadow-primary/30 block h-full rounded-full bg-gradient-to-r shadow-[0_0_14px] transition-[width] duration-300 motion-reduce:transition-none"
            style={{ width: `${percent}%` }}
          />
        </div>
      ) : null}
      <p className="text-muted-foreground mt-auto text-sm">{detail}</p>
    </Card>
  );
}
