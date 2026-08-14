FROM debian:bookworm-slim
RUN useradd -m -d /home/you -s /bin/bash you && chown -R you /usr/local/bin
COPY cpb-linux /usr/local/bin/claude-playbook
COPY claude-shim /usr/local/bin/claude
RUN chmod +x /usr/local/bin/claude-playbook /usr/local/bin/claude && \
    ln -s claude-playbook /usr/local/bin/cpb && \
    echo 'PS1="\u@mac:\w\$ "' >> /home/you/.bashrc
USER you
WORKDIR /home/you
ENV HOME=/home/you
CMD ["bash"]
