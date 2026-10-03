import { useDeferredValue, useMemo } from "react";
import zxcvbn from "zxcvbn";

const labels = ["很弱", "弱", "一般", "强", "很强"];

export default function PasswordStrength({ value, username, name }: { value: string; username?: string; name?: string }) {
  const password = useDeferredValue(value);
  const score = useMemo(() => zxcvbn(password.slice(0, 256), [username ?? "", name ?? ""].filter(Boolean)).score, [password, username, name]);
  return <div className="font-body text-xs" role="status">
    <span>密码强度：{labels[score]}</span>
    <meter className="ml-2" min={0} max={4} value={score} aria-label="密码强度" />
    <p className="mt-1 text-neutral-500">根据常见密码与模式估计，不代表已检查泄露数据库。</p>
  </div>;
}
