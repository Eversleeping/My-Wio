import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import FileDiffCode from "./FileDiffCode";

describe("FileDiffCode", () => {
  test("renders collapsed context and paired changed lines", () => {
    render(<FileDiffCode content={"@@ -56,2 +56,2 @@\n-old value\n+new value\n context\n"} language="text" unchangedLabel={count => `${count} unchanged`} />);
    expect(screen.getByText("55 unchanged")).toBeInTheDocument();
    expect(screen.getByText("old value")).toBeInTheDocument();
    expect(screen.getByText("new value")).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(3);
  });
});
