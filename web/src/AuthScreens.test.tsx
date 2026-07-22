import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { post } from "./api";
import { LoginScreen, SetupScreen, type AuthMode } from "./AuthScreens";
import { I18nProvider } from "./i18n";

vi.mock("./api", () => ({ post: vi.fn() }));

beforeEach(() => {
  window.localStorage.clear();
  vi.mocked(post).mockReset();
});

function renderSetup() {
  return render(<I18nProvider><SetupScreen onReady={vi.fn()} /></I18nProvider>);
}

function renderLogin(authMode: AuthMode) {
  return render(<I18nProvider><LoginScreen authMode={authMode} onLogin={vi.fn()} /></I18nProvider>);
}

test("switches between all administrator authentication modes", async () => {
  const user = userEvent.setup();
  renderSetup();

  expect(screen.getByRole("radio", { name: /账号 \+ 动态验证码或恢复码/ })).toBeChecked();
  expect(screen.queryByLabelText("密码")).not.toBeInTheDocument();

  await user.click(screen.getByRole("radio", { name: /账号 \+ 固定密码 每次登录/ }));
  expect(screen.getByLabelText("密码")).toBeInTheDocument();
  expect(screen.getByLabelText("确认密码")).toBeInTheDocument();

  await user.click(screen.getByRole("radio", { name: /账号 \+ 固定密码 \+ 动态验证码或恢复码/ }));
  expect(screen.getByLabelText("密码")).toBeInTheDocument();
  expect(screen.getByLabelText("确认密码")).toBeInTheDocument();
});

test("validates matching passwords before creating the administrator", async () => {
  const user = userEvent.setup();
  renderSetup();
  await user.click(screen.getByRole("radio", { name: /账号 \+ 固定密码 每次登录/ }));
  await user.type(screen.getByLabelText("密码"), "correct-horse-battery-staple");
  await user.type(screen.getByLabelText("确认密码"), "different-password-value");
  await user.click(screen.getByRole("button", { name: "创建管理员" }));

  expect(screen.getByText("两次输入的密码不一致。")).toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

test("submits password mode and shows the completion state", async () => {
  const user = userEvent.setup();
  vi.mocked(post).mockResolvedValue({ username: "admin", auth_mode: "password" });
  renderSetup();
  await user.click(screen.getByRole("radio", { name: /账号 \+ 固定密码 每次登录/ }));
  await user.type(screen.getByLabelText("密码"), "correct-horse-battery-staple");
  await user.type(screen.getByLabelText("确认密码"), "correct-horse-battery-staple");
  await user.click(screen.getByRole("button", { name: "创建管理员" }));

  expect(await screen.findByRole("heading", { name: "管理员已就绪" })).toBeInTheDocument();
  expect(post).toHaveBeenCalledWith("/setup", {
    username: "admin",
    auth_mode: "password",
    password: "correct-horse-battery-staple"
  });
});

test.each([
  ["password", true, false],
  ["totp", false, true],
  ["password_totp", true, true]
] as const)("renders required login fields for %s mode", (authMode, passwordVisible, codeVisible) => {
  renderLogin(authMode);
  expect(Boolean(screen.queryByLabelText("密码"))).toBe(passwordVisible);
  expect(Boolean(screen.queryByLabelText("动态验证码或恢复码"))).toBe(codeVisible);
});
