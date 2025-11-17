# Minimal image with git and ssh client
FROM alpine:3.20

RUN apk add --no-cache git openssh-client bash perl

# Create workspace and ssh dirs
RUN mkdir -p /workspace /root/.ssh /ssh
WORKDIR /workspace

# Copy entrypoint
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV GIT_SSH_COMMAND="ssh -i /ssh/id_osbits -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
