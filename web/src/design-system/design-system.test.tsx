import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "./Button";
import { Field } from "./Field";
import { ConfirmDialog } from "./Dialog";
import { ErrorSummary, Loading, StatusBanner } from "./Status";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Button", () => {
  it("renders the newsprint primary style with a 44px touch target", () => {
    render(<Button>保存</Button>);
    const button = screen.getByRole("button", { name: "保存" });
    expect(button).toHaveClass("min-h-[44px]", "min-w-[44px]", "bg-ink", "uppercase");
    expect(button).toHaveAttribute("type", "button");
  });

  it("renders secondary and ghost variants", () => {
    render(
      <>
        <Button variant="secondary">次要</Button>
        <Button variant="ghost">幽灵</Button>
      </>,
    );
    expect(screen.getByRole("button", { name: "次要" })).toHaveClass("border-ink");
    expect(screen.getByRole("button", { name: "幽灵" })).toHaveClass("hover:bg-divider");
  });

  it("is keyboard operable and fires on Enter", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(<Button onClick={onClick}>提交</Button>);
    await user.tab();
    expect(screen.getByRole("button", { name: "提交" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("disables interaction while pending", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button disabled onClick={onClick}>
        处理中
      </Button>,
    );
    expect(screen.getByRole("button", { name: "处理中" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "处理中" }));
    expect(onClick).not.toHaveBeenCalled();
  });
});

describe("Field", () => {
  it("associates a persistent label with its input", () => {
    render(<Field id="title" label="标题" />);
    expect(screen.getByLabelText("标题")).toBeInTheDocument();
  });

  it("wires hint and error into aria-describedby and marks invalid", () => {
    render(<Field id="name" label="名称" hint="不超过 256 字" error="名称必填" />);
    const input = screen.getByLabelText("名称");
    expect(input).toHaveAttribute("aria-describedby", "name-hint name-error");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("名称必填")).toHaveAttribute("id", "name-error");
    expect(screen.getByText("不超过 256 字")).toBeInTheDocument();
  });

  it("keeps the label available to assistive tech when hidden", () => {
    render(<Field id="secret" label="密码" hideLabel />);
    const input = screen.getByLabelText("密码");
    expect(input.id).toBe("secret");
    const label = document.querySelector('label[for="secret"]');
    expect(label).toHaveClass("sr-only");
  });
});

describe("ConfirmDialog", () => {
  it("renders nothing while closed", () => {
    render(<ConfirmDialog open={false} title="确认删除" onConfirm={() => {}} onCancel={() => {}} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("moves focus into the dialog on open and restores it on close", async () => {
    const onConfirm = vi.fn();
    const { rerender } = render(
      <>
        <Button>触发按钮</Button>
        <ConfirmDialog open={false} title="确认删除" onConfirm={onConfirm} onCancel={() => {}} />
      </>,
    );
    const trigger = screen.getByRole("button", { name: "触发按钮" });
    trigger.focus();
    expect(trigger).toHaveFocus();

    rerender(
      <>
        <Button>触发按钮</Button>
        <ConfirmDialog open={true} danger title="确认删除" description="此操作不可恢复。" onConfirm={onConfirm} onCancel={() => {}} />
      </>,
    );
    const dialog = screen.getByRole("dialog", { name: "确认删除" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveClass("border-t-accent");
    // Focus moved into the dialog (first focusable = the cancel button).
    const cancel = screen.getByRole("button", { name: "取消" });
    const confirm = screen.getByRole("button", { name: "确认" });
    expect([cancel, confirm, dialog]).toContain(document.activeElement);

    rerender(
      <>
        <Button>触发按钮</Button>
        <ConfirmDialog open={false} title="确认删除" onConfirm={onConfirm} onCancel={() => {}} />
      </>,
    );
    expect(trigger).toHaveFocus();
  });

  it("cancels on Escape and wraps Tab focus inside the dialog", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(<ConfirmDialog open={true} title="确认" onConfirm={() => {}} onCancel={onCancel} />);
    const dialog = screen.getByRole("dialog");
    await user.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalledTimes(1);
    // Tab cycling: focus must stay within the dialog's buttons.
    const cancel = screen.getByRole("button", { name: "取消" });
    cancel.focus();
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "确认" }));
    await user.tab();
    expect([cancel, dialog.querySelector("button")]).toContain(document.activeElement);
  });

  it("confirms on click", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(<ConfirmDialog open={true} title="确认" onConfirm={onConfirm} onCancel={() => {}} />);
    await user.click(screen.getByRole("button", { name: "确认" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

describe("Status primitives", () => {
  it("ErrorSummary renders an alert with the request id and hides when empty", () => {
    const { rerender } = render(<ErrorSummary message="加载失败" requestId="req-1" />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("加载失败");
    expect(alert).toHaveTextContent("request_id: req-1");
    rerender(<ErrorSummary message="" />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("ErrorSummary offers a 44px dismiss control", async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<ErrorSummary message="出错了" onDismiss={onDismiss} />);
    await user.click(screen.getByRole("button", { name: "关闭错误提示" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("StatusBanner and Loading expose polite status regions", () => {
    render(
      <>
        <StatusBanner>已保存</StatusBanner>
        <Loading label="正在加载条目" />
      </>,
    );
    const statuses = screen.getAllByRole("status");
    expect(statuses).toHaveLength(2);
    expect(statuses[0]).toHaveTextContent("已保存");
    expect(statuses[1]).toHaveTextContent("正在加载条目");
  });
});
