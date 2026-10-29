import { expect, test } from "vitest";
import { parseSSHCommand } from "./sshCommand";

test.each([
  ["ssh -p 2222 ibssh_example@ssh.instabox.sh", { host: "ssh.instabox.sh", port: 2222, user: "ibssh_example" }],
  [" ssh   user@host.example.com  -p2222 ", { host: "host.example.com", port: 2222, user: "user" }],
  ["ssh -l ubuntu -p 2200 192.0.2.10", { host: "192.0.2.10", port: 2200, user: "ubuntu" }],
  ["ssh.exe -p '2222' \"user@ssh.instabox.sh\"", { host: "ssh.instabox.sh", port: 2222, user: "user" }],
  ["ssh -lubuntu host.example.com", { host: "host.example.com", port: 22, user: "ubuntu" }],
  ["ssh root@[2001:db8::1]", { host: "2001:db8::1", port: 22, user: "root" }],
  ["ssh host.example.com", { host: "host.example.com", port: 22 }],
])("reads the connection fields from %s", (command, expected) => {
  expect(parseSSHCommand(command as string)).toEqual(expected);
});

test.each([
  "", "ssh", "ssh -p", "ssh -l", "ssh -p 0 user@host", "ssh -p 65536 user@host", "ssh -p 22.5 user@host",
  "ssh -p abc user@host", "ssh -p 22 -p 23 user@host", "ssh -l alice bob@host", "ssh user@host extra-host",
  "ssh -J jump user@host", "ssh -i ~/.ssh/id_rsa user@host", "ssh -o Port=2222 user@host", "scp user@host",
  "ssh user@host && echo test", "ssh user@host;", "ssh user@host pwd", "ssh 'user@host", "ssh user@host\"",
  "ssh @host", "ssh user@@host", "ssh user@host:2222", "ssh user@-host", "ssh user@[not-ip]",
])("does not silently reinterpret an unsupported or incomplete command: %s", command => {
  expect(parseSSHCommand(command)).toBeNull();
});
