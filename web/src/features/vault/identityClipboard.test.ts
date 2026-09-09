import { describe, expect, it } from "vitest";
import { formatIdentityClipboard } from "./identityClipboard";

describe("formatIdentityClipboard", () => {
  it("formats the required three lines and excludes country", () => {
    expect(formatIdentityClipboard({
      name: "条目名称",
      full_name: " 张三 ",
      country: "中国",
      state: "广东省",
      city: "深圳市",
      district: "南山区",
      address_line: " 科技园科苑路 1 号 ",
      postal_code: "518000",
      phone: " +86 13800138000 ",
    })).toBe("姓名：张三\n地址：广东省 深圳市 南山区 科技园科苑路 1 号 518000\n联系电话：+86 13800138000");
  });

  it("trims values and turns line breaks into spaces without collapsing normal spaces", () => {
    expect(formatIdentityClipboard({
      name: "x",
      full_name: "  Alice\nSmith  ",
      address_line: "12  Main\r\nStreet",
      phone: " 00123 ext. 4\n ",
    })).toBe("姓名：Alice Smith\n地址：12  Main Street\n联系电话：00123 ext. 4");
  });

  it("keeps all labels when identity values are missing", () => {
    expect(formatIdentityClipboard({ name: "x" })).toBe("姓名：\n地址：\n联系电话：");
  });
});
