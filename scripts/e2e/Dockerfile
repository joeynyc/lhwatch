FROM python:3.12-slim
RUN apt-get update && apt-get install -y --no-install-recommends openssh-server \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /bin/sh lab \
    && passwd -d lab \
    && mkdir -p /run/sshd /home/lab/.ssh \
    && chmod 700 /home/lab/.ssh \
    && chown -R lab:lab /home/lab/.ssh \
    && ssh-keygen -A
RUN printf '%s\n' \
    'Port 2222' \
    'ListenAddress 0.0.0.0' \
    'PasswordAuthentication no' \
    'KbdInteractiveAuthentication no' \
    'PermitEmptyPasswords no' \
    'PermitRootLogin no' \
    'UsePAM no' \
    'AllowUsers lab' \
    'PrintMotd no' \
    'LogLevel ERROR' > /etc/ssh/sshd_config
CMD ["/usr/sbin/sshd", "-D", "-e"]
