import { type ComponentPropsWithoutRef, type RefObject } from "react";
import { X } from "lucide-react";
import { ComposerImagePicker } from "../components/ComposerImagePicker";
import { useI18n } from "../i18n";

export type ComposerImage = { id: string; dataURL: string };
export function ComposerAttachments({ images, busy, onRemove }: { images: ComposerImage[]; busy: boolean; onRemove: (id: string) => void }) {
  const { t } = useI18n();
  return images.length ? <div className="composer-images">{images.map(image => <figure key={image.id}><img src={image.dataURL} alt="" /><button type="button" aria-label={t("common.close")} disabled={busy} onClick={() => onRemove(image.id)}><X size={13} /></button></figure>)}</div> : null;
}

type InputProps = ComponentPropsWithoutRef<"textarea"> & {
  inputRef: RefObject<HTMLTextAreaElement>; imageDisabled: boolean;
  onImages: (files: File[]) => Promise<void>;
  onSlashKey: (event: React.KeyboardEvent<HTMLTextAreaElement>) => boolean | undefined | null;
};
export function ComposerInput({ inputRef, imageDisabled, onImages, onSlashKey, ...props }: InputProps) {
  return <div className="composer-input"><ComposerImagePicker disabled={imageDisabled} onSelect={onImages} /><textarea {...props} ref={inputRef}
    onPaste={event => {
      const files = Array.from(event.clipboardData.items).filter(item => item.type.startsWith("image/")).map(item => item.getAsFile()).filter((file): file is File => file !== null);
      if (files.length) { event.preventDefault(); void onImages(files); }
    }}
    onKeyDown={event => {
      if (onSlashKey(event)) return;
      if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); }
    }} />
  </div>;
}
