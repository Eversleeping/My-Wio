import { currentLocale } from "./i18n";

export function shortSHA(value: string) {
  return value ? value.slice(0, 8) : "-";
}

export function formatDate(value: string) {
  return new Intl.DateTimeFormat(currentLocale(), { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

export function formatTime(value: string) {
  return new Intl.DateTimeFormat(currentLocale(), { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value));
}

export function relative(value: string) {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(currentLocale(), { numeric: "auto" });
  const ranges: Array<[number, Intl.RelativeTimeFormatUnit]> = [[60, "second"], [60, "minute"], [24, "hour"], [7, "day"], [4.345, "week"], [12, "month"], [Infinity, "year"]];
  let duration = seconds;
  for (const [amount, unit] of ranges) {
    if (Math.abs(duration) < amount) return formatter.format(Math.round(duration), unit);
    duration /= amount;
  }
  return formatDate(value);
}
