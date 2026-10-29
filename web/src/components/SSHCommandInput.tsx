import { useId, useState } from "react";
import { useI18n } from "../i18n";
import { parseSSHCommand, type SSHConnection } from "../sshCommand";

export function SSHCommandInput({ onApply }: { onApply: (connection: SSHConnection) => void }) {
  const { t } = useI18n();
  const id = useId();
  const [command, setCommand] = useState("");
  const [invalid, setInvalid] = useState(false);
  const apply = () => {
    const connection = parseSSHCommand(command);
    setInvalid(!connection);
    if (connection) onApply(connection);
  };
  return <div className="ssh-command-field">
    <label className="label" htmlFor={id}>{t("server.sshCommand")}</label>
    <div className="ssh-command-input"><input id={id} value={command} placeholder="ssh -p 2222 user@ssh.instabox.sh" spellCheck={false} autoCapitalize="none" autoComplete="off" aria-describedby={`${id}-help${invalid ? ` ${id}-error` : ""}`} aria-invalid={invalid || undefined} onChange={event => { setCommand(event.target.value); setInvalid(false); }} onKeyDown={event => { if (event.key === "Enter") { event.preventDefault(); apply(); } }} /><button type="button" className="secondary-button" disabled={!command.trim()} onClick={apply}>{t("server.sshCommandApply")}</button></div>
    <p id={`${id}-help`} className="muted">{t("server.sshCommandHelp")}</p>
    {invalid && <p id={`${id}-error`} className="error-banner" role="alert">{t("server.sshCommandInvalid")}</p>}
  </div>;
}
