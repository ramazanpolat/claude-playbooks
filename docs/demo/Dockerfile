# The recorder and the machine it records, in one image: VHS (with its
# ttyd, ffmpeg and browser), plus cpb, a stand-in claude, and a user whose
# home looks like a real machine's. Nothing touches the recording host.
FROM ghcr.io/charmbracelet/vhs:v0.12.1
RUN useradd -m -d /home/you -s /bin/bash you && chown -R you /usr/local/bin && \
    echo 'PS1="\u@mac:\w\$ "' >> /home/you/.bashrc
COPY cpb-linux /usr/local/bin/cpb
COPY claude-shim /usr/local/bin/claude
RUN chmod +x /usr/local/bin/cpb /usr/local/bin/claude
