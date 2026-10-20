# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's private vulnerability reporting: open the [Security tab](https://github.com/joeynyc/lhwatch/security) of `joeynyc/lhwatch` and choose **Report a vulnerability**. Do not open a public issue, pull request, or discussion for a suspected vulnerability.

Include the lhwatch version (`lhwatch --version`), the operating systems involved, and steps to reproduce. You will get a response in the advisory thread, and a fix will be coordinated there before details are made public.

## Scope

lhwatch runs commands on your machines and can stop processes, so these areas matter most:

- Construction of SSH commands and arguments from configuration, flags, and host names
- The remote helper (`internal/collector/helper.py`) and how its output is parsed
- Stopping and signalling processes, including revalidation of the target before a signal is sent
- OpenSSH control sockets and their per-user directory
- Port forwards opened from the dashboard

Issues in OpenSSH, Python, Docker, or the operating system itself should be reported to those projects.
