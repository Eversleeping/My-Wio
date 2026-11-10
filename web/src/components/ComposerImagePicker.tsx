import { useRef } from "react";
import { ImagePlus } from "lucide-react";
import { useI18n } from "../i18n";

export function ComposerImagePicker({ disabled, onSelect }: { disabled: boolean; onSelect: (files: File[]) => Promise<void> }) {
  const { t } = useI18n();
  const input = useRef<HTMLInputElement>(null);
  return <span className="composer-image-picker">
    <input ref={input} className="hidden-input" type="file" accept="image/png,image/jpeg,image/webp" multiple disabled={disabled} aria-label={t("codex.attachImage")} onChange={event => {
      const files = Array.from(event.currentTarget.files ?? []);
      // Let a screenshot be selected again after removing it from the draft.
      event.currentTarget.value = "";
      if (files.length) void onSelect(files);
    }} />
    <button type="button" className="icon-button" aria-label={t("codex.attachImage")} title={t("codex.attachImage")} disabled={disabled} onClick={() => input.current?.click()}><ImagePlus size={20} /></button>
  </span>;
}
