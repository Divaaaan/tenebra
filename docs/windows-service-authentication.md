# Windows service authentication

The desktop opens the control pipe with the exact client mask `0x120083`
and identification-only impersonation. Before sending a request, Rust checks
the pipe server PID against the running, own-process LocalSystem Tenebra
service, reads the registered image and actual process image, and repeats
the PID/status check while retaining the process handle.

On Windows, a LocalSystem process can inherit a DACL that denies ordinary
users even `PROCESS_QUERY_LIMITED_INFORMATION`. A pipe connection and SCM
queries can therefore succeed while the process-image authentication fails
with access denied. This was reproduced in a clean Windows guest.

Before constructing the daemon, listening, or reporting Running, the service
adds one non-inheritable `INTERACTIVE` (`S-1-5-4`) grant of exactly `0x1000`
to its own process DACL. It preserves existing ACEs, owner, group, SACL and
protection flags; it verifies the complete resulting DACL. Read, merge,
write or readback failures abort startup. Null or invalid DACLs are rejected.
Existing deny entries remain authoritative and are never removed to bypass
a stricter policy.

This permits process metadata queries, including the executable path. It
does not grant process memory access, handle duplication, termination,
suspension, injection, token access or ACL changes. The ACL exists only for
the current service process and is recreated on each start. It changes no
machine-wide policy and does not weaken the desktop's server checks.

The unit tests manipulate in-memory security descriptors only. Native
acceptance must separately verify the installed service from the ordinary
console-user token and from an elevated installer token. Source checks and
an SCM Running state alone do not prove the connection works.

References: Microsoft documents the
[process access rights](https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights),
[ACL merge behavior](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setentriesinaclw),
and [handle-based security updates](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setsecurityinfo).
