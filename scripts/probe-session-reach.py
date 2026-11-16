#!/usr/bin/env python3
"""Report session access without reading file contents or printing paths or secrets.

Run from a CLI tool process, using explicit paths when an installation differs
from the defaults. Socket checks connect and close without sending any data.
Missing resources are reported separately from denied access.

Read checks open and close regular files or directories without reading them.
Write checks report access permissions only: they do not create or modify files
and cannot establish that a filesystem sandbox allows a write.
"""

import argparse
import os
from pathlib import Path
import re
import socket
import stat


def file_access(label, path):
    if path is None:
        print(label + "\tuntested\tuntested")
        return
    try:
        mode = path.stat().st_mode
    except FileNotFoundError:
        print(label + "\tabsent\tabsent")
        return
    except (OSError, ValueError):
        print(label + "\tno\tno")
        return
    if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
        print(label + "\tuntested\tuntested")
        return
    try:
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NONBLOCK", 0))
        os.close(descriptor)
        readable = "yes"
    except (OSError, ValueError):
        readable = "no"
    try:
        writable = "yes" if os.access(path, os.W_OK) else "no"
    except (OSError, ValueError):
        writable = "no"
    print(label + "\t" + readable + "\t" + writable)


def socket_access(label, path):
    try:
        path.stat()
    except FileNotFoundError:
        result = "absent"
    except (OSError, ValueError):
        result = "no"
    else:
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.settimeout(1)
                connection.connect(str(path))
            result = "yes"
        except (OSError, ValueError):
            result = "no"
    print(label + "\t" + result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config-dir", type=Path, default=Path("/etc/wio-agent"))
    parser.add_argument("--state-dir", type=Path, default=Path("/var/lib/wio-agent"))
    parser.add_argument("--home-dir", type=Path, default=Path.home())
    parser.add_argument("--codex-home", type=Path)
    parser.add_argument("--claude-home", type=Path)
    parser.add_argument("--gemini-home", type=Path)
    parser.add_argument("--codex-key", type=Path)
    parser.add_argument("--git-credentials", type=Path)
    parser.add_argument("--other-workspace", type=Path)
    parser.add_argument("--outside-dir", type=Path, default=Path("/usr"))
    parser.add_argument("--docker-socket", type=Path, default=Path("/var/run/docker.sock"))
    parser.add_argument("--helper-socket", type=Path, default=Path("/run/wio-prerequisites/helper.sock"))
    parser.add_argument("--peer-pid", type=int, help="an existing peer CLI process; only check access")
    parser.add_argument("--control-plane-pid", type=int, help="the control-plane process; only check access")
    parser.add_argument("--environment-names", action="store_true", help="also list variable names, never values")
    args = parser.parse_args()

    print("file_resource\treadable\twrite_permission")
    file_access("agent_registration", args.config_dir / "config.json")
    file_access("codex_key", args.codex_key or args.config_dir / "codex.key")
    file_access("claude_managed_profile", args.state_dir / "claude/api.json")
    file_access("gemini_managed_profile", args.state_dir / "gemini/api.json")
    file_access("git_credentials", args.git_credentials or args.state_dir / ".git-credentials")
    file_access("home_git_credentials", args.home_dir / ".git-credentials")
    file_access("other_workspace", args.other_workspace)
    for engine in ("codex", "claude", "gemini"):
        engine_home = getattr(args, engine + "_home") or args.home_dir / ("." + engine)
        file_access(engine + "_native_history", engine_home)
    file_access("parent_process_arguments", Path("/proc") / str(os.getppid()) / "cmdline")
    file_access("parent_process_initial_environment", Path("/proc") / str(os.getppid()) / "environ")
    peer = Path("/proc") / str(args.peer_pid) if args.peer_pid else None
    control = Path("/proc") / str(args.control_plane_pid) if args.control_plane_pid else None
    file_access("peer_process_arguments", peer / "cmdline" if peer else None)
    file_access("peer_process_initial_environment", peer / "environ" if peer else None)
    file_access("control_plane_initial_environment", control / "environ" if control else None)
    file_access("system_path", args.outside_dir)

    print("socket_resource\tconnectable")
    socket_access("docker", args.docker_socket)
    socket_access("prerequisite_helper", args.helper_socket)
    print("environment_resource\tname_present")
    for label, names in (
        ("control_plane_secret_names", ("WIO_MASTER_KEY", "WIO_DATABASE_URL", "WIO_SETUP_TOKEN")),
        ("codex_key_name", ("WIO_CODEX_API_KEY",)),
        ("gemini_key_names", ("GEMINI_API_KEY", "GOOGLE_API_KEY")),
        ("claude_key_names", ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")),
    ):
        print(label + "\t" + ("yes" if any(name in os.environ for name in names) else "no"))
    if args.environment_names:
        print("environment_variable_names")
        for name in sorted(os.environ):
            # An invalid name supplied by an unusual launcher must not inject
            # extra output lines or terminal escapes into this report.
            if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
                print(name)


if __name__ == "__main__":
    main()
