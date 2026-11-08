import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, test, vi } from "vitest";
import { APIError, post } from "./api";
import { LoginScreen, SetupScreen, type AuthMode } from "./AuthScreens";
import { I18nProvider } from "./i18n";

vi.mock("./api", async importOriginal => ({ ...await importOriginal<typeof import("./api")>(), post: vi.fn() }));

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
  await user.type(screen.getByLabelText("设置令牌"), "setup-token-value");
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
  await user.type(screen.getByLabelText("设置令牌"), "  setup-token-value  ");
  await user.click(screen.getByRole("radio", { name: /账号 \+ 固定密码 每次登录/ }));
  await user.type(screen.getByLabelText("密码"), "correct-horse-battery-staple");
  await user.type(screen.getByLabelText("确认密码"), "correct-horse-battery-staple");
  await user.click(screen.getByRole("button", { name: "创建管理员" }));

  expect(await screen.findByRole("heading", { name: "管理员已就绪" })).toBeInTheDocument();
  expect(post).toHaveBeenCalledWith("/setup", {
    setup_token: "setup-token-value",
    username: "admin",
    auth_mode: "password",
    password: "correct-horse-battery-staple"
  });
});

test("requires the one-time setup token and explains where to find it", async () => {
  window.localStorage.setItem("wio_language", "en");
  const user = userEvent.setup();
  renderSetup();
  const token = screen.getByLabelText("Setup token");
  expect(token).toBeRequired();
  expect(token).toHaveAccessibleDescription(/docker compose logs controlplane.*WIO_SETUP_TOKEN/);
  await user.click(screen.getByRole("button", { name: "Create administrator" }));
  expect(post).not.toHaveBeenCalled();
});

test("shows a rejected setup token clearly and lets the administrator retry", async () => {
  window.localStorage.setItem("wio_language", "en");
  const user = userEvent.setup();
  vi.mocked(post).mockRejectedValueOnce(new APIError(403, "invalid setup token")).mockResolvedValueOnce({ username: "admin", auth_mode: "totp", totp_uri: "otpauth://totp/Wio:admin?secret=ABC", totp_secret: "ABC", recovery_codes: ["code-1"] });
  renderSetup();
  await user.type(screen.getByLabelText("Setup token"), "wrong-token");
  await user.click(screen.getByRole("button", { name: "Create administrator" }));

  expect(await screen.findByText("Setup token rejected: invalid setup token")).toBeInTheDocument();
  const token = screen.getByLabelText("Setup token");
  expect(token).toHaveAttribute("aria-invalid", "true");
  expect(token).toHaveFocus();
  expect(post).toHaveBeenLastCalledWith("/setup", { setup_token: "wrong-token", username: "admin", auth_mode: "totp" });

  await user.clear(token);
  await user.type(token, "correct-token");
  expect(token).not.toHaveAttribute("aria-invalid");
  await user.click(screen.getByRole("button", { name: "Create administrator" }));
  expect(await screen.findByRole("heading", { name: "Authenticator setup" })).toBeInTheDocument();
  expect(post).toHaveBeenLastCalledWith("/setup", { setup_token: "correct-token", username: "admin", auth_mode: "totp" });
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
