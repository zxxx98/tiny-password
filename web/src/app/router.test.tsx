import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
  consumeNavigationState,
  navigate,
  navigateWithState,
  replace,
  setNavigationGuard,
  usePath,
} from "./router";

function PathReader() {
  return <output data-testid="path">{usePath()}</output>;
}

afterEach(() => {
  window.history.replaceState({}, "", "/");
});

describe("router navigation", () => {
  it("pushes route metadata and replaces without growing history", () => {
    const startLength = window.history.length;
    navigate("/vault/item-1", { source: "list", modal: "item" });
    expect(window.location.pathname).toBe("/vault/item-1");
    expect(window.history.length).toBe(startLength + 1);
    expect(window.history.state).toEqual({ source: "list", modal: "item" });

    replace("/vault", { source: "direct" });
    expect(window.location.pathname).toBe("/vault");
    expect(window.history.length).toBe(startLength + 1);
    expect(window.history.state).toEqual({ source: "direct" });
  });

  it("keeps generator handoff in memory instead of browser history", () => {
    navigateWithState("/vault", { kind: "new-login", password: "secret" });
    expect(window.history.state).toEqual({});
    expect(consumeNavigationState()).toEqual({ kind: "new-login", password: "secret" });
    expect(window.history.state).not.toHaveProperty("password");
  });

  it("updates path subscribers after push and pop navigation", async () => {
    render(<PathReader />);
    navigate("/vault/item-2");
    expect(await screen.findByTestId("path")).toHaveTextContent("/vault/item-2");
    window.history.back();
    await waitFor(() => expect(screen.getByTestId("path")).not.toHaveTextContent("/vault/item-2"));
  });

  it("lets the current page block a push navigation", () => {
    const release = setNavigationGuard(() => false);
    navigate("/generator");
    expect(window.location.pathname).toBe("/");
    release();
  });
});
