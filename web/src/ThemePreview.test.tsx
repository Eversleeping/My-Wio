import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import FileDiffCode from "./FileDiffCode";
import FilePreviewCode from "./FilePreviewCode";
import { I18nProvider } from "./i18n";
import { ThemeProvider, ThemeSwitch } from "./theme";

function contrast(foreground: string, background: [number, number, number]) {
  const rgb = foreground.match(/\d+/g)?.map(Number);
  if (!rgb || rgb.length < 3) throw new Error(`Unexpected color: ${foreground}`);
  const luminance = (channels: number[]) => {
    const [red, green, blue] = channels.map(value => {
      const channel = value / 255;
      return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
  };
  const values = [luminance(rgb), luminance(background)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  document.body.removeAttribute("data-theme");
});

test("switches readable syntax highlighting in both file and diff previews", async () => {
  localStorage.setItem("wio_language", "en");
  const user = userEvent.setup();
  render(<I18nProvider><ThemeProvider><ThemeSwitch />
    <FilePreviewCode content={'const value = "hello";'} language="typescript" />
    <FileDiffCode content={'@@ -1 +1 @@\n-const value = "old";\n+const value = "new";'} language="typescript" unchangedLabel={String} />
  </ThemeProvider></I18nProvider>);

  const keywords = () => screen.getAllByText("const");
  expect(keywords()).toHaveLength(3);
  for (const keyword of keywords()) expect(contrast(getComputedStyle(keyword).color, [9, 12, 18])).toBeGreaterThan(4.5);

  await user.click(screen.getByRole("button", { name: "Light theme" }));
  await waitFor(() => expect(localStorage.getItem("wio_theme")).toBe("light"));
  for (const keyword of keywords()) expect(contrast(getComputedStyle(keyword).color, [251, 252, 251])).toBeGreaterThan(4.5);
});
