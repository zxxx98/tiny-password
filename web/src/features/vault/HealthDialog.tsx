import { useState } from "react";
import { Button } from "../../design-system/Button";
import { Dialog } from "../../design-system/Dialog";
import type { HealthReport } from "./types";

const reasons: Record<string, string> = { weak: "弱密码", reused: "重复密码", expired: "密码已过期" };

export function HealthDialog({ report, onClose, onOpen }: { report: HealthReport; onClose: () => void; onOpen: (id: string) => void }) {
  const [filter, setFilter] = useState("");
  const items = (report.items ?? []).filter((item) => !filter || item.reasons.includes(filter));
  return <Dialog open title="密码健康" onClose={onClose} description="弱密码按少于 12 个字符识别；重复和过期检测覆盖你可读取的登录条目。">
    <div className="space-y-4 p-4">
      <div className="flex flex-wrap gap-2">
        <Button variant={filter === "" ? "primary" : "secondary"} onClick={() => setFilter("")}>全部问题</Button>
        {Object.entries(reasons).map(([key, label]) => <Button key={key} variant={filter === key ? "primary" : "secondary"} onClick={() => setFilter(key)}>{label} · {report[key as "weak" | "reused" | "expired"]}</Button>)}
      </div>
      {items.length === 0 ? <p className="font-body text-sm">没有此类问题。</p> : <ul className="divide-y divide-divider">
        {items.map((item) => <li key={item.item_id} className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div><p className="font-body font-semibold">{item.title || "登录条目"}</p><p className="font-body text-xs text-accent">{item.reasons.map((reason) => reasons[reason] ?? reason).join(" · ")}</p></div>
          <Button variant="secondary" onClick={() => onOpen(item.item_id)}>查看条目</Button>
        </li>)}
      </ul>}
    </div>
  </Dialog>;
}
